// Package appsclient is agents' way into apps' internal Slack API: the
// service-token client the router and the surfaces service share.
//
// Everything Oryxa says in Slack, and everything it learns of a Slack
// delivery, goes through apps, which alone holds the bot's token. A service
// names an organization, a thread and a template, and apps checks the thread
// is one it saw, renders the message and posts it. This client is the calls
// with agents' own identity on them: a service token for the audience apps and
// the purpose slack, minted for each request, that lives a minute at most.
//
// It is written to the router's contract (slackrouter.Slack) and to the
// surfaces service's, which needs two things the router does not: where a post
// went, and to replace a message. What a failure means to a caller is settled
// here, once, so both handle it alike:
//
//   - what is not there, or is a conversation Slack will not describe, is
//     slackrouter.ErrGone and slackrouter.ErrUnreadable;
//   - a call apps understood and refused, that the same call would be refused
//     again (a thread it has not seen, an install that has ended, a request it
//     cannot read), is slackrouter.Permanent;
//   - a rate limit is honoured, a few times, for as long as apps says, and is
//     then an error that says how long to wait (RetryAfter);
//   - anything else did not complete, and is tried again by whoever called.
//
// Posts carry their idempotency key as given, so a call made again after an
// answer that never arrived sends nothing twice.
package appsclient

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/slackrouter"
	"github.com/Shubham04-oss/oryxa-go/internal/observability"
	"github.com/Shubham04-oss/oryxa-go/internal/serviceauth"
	"github.com/Shubham04-oss/oryxa-go/internal/workspace/runtime"
)

const (
	// Audience and Purpose are what every service token this client mints says:
	// apps is who it is for, and slack is what apps admits it for.
	Audience = "apps"
	Purpose  = "slack"

	inboundPath  = "/v1/internal/apps/slack/inbound/"
	profilesPath = "/v1/internal/apps/slack/profiles"
	channelsPath = "/v1/internal/apps/slack/channels/"
	postPath     = "/v1/internal/apps/slack/post"
	updatePath   = "/v1/internal/apps/slack/update"

	requestTimeout = 30 * time.Second
	// maxResponseBytes bounds what apps may answer with.
	maxResponseBytes = 1 << 20
	// maxProfiles is how many users apps reads in one profiles request.
	maxProfiles = 20

	// DefaultRetries is how many times a rate limited call is made again.
	DefaultRetries = 3
	// DefaultMaxWait bounds one wait for a rate limit to lift. A limit that asks
	// for longer is not waited out here: the error says how long, and the
	// caller's own retry does the waiting where it costs nothing.
	DefaultMaxWait = 5 * time.Second
	// defaultRetryAfter is the wait for a 429 that does not say.
	defaultRetryAfter = time.Second
)

// Signer proves which service is calling. serviceauth.Signer satisfies it.
type Signer interface {
	Token(ctx context.Context, audience, purpose string) (string, error)
}

// Config configures a Client.
type Config struct {
	// BaseURL is apps' own URL, which inside one process is its loopback
	// address. It must be HTTPS or loopback HTTP, the way every service-to-
	// service URL is, since a service token travels on it.
	BaseURL string
	Signer  Signer
	// HTTP is the client requests go out on. It is copied, and made to refuse
	// redirects: a service token is never sent anywhere its URL did not name.
	HTTP *http.Client
	// Retries is how many times a call answered 429 is made again, and MaxWait
	// how long one wait for it may be. Left zero, DefaultRetries and
	// DefaultMaxWait; a negative Retries makes no retry at all.
	Retries int
	MaxWait time.Duration
	// Sleep waits for d, or for ctx to end. Tests replace it.
	Sleep  func(ctx context.Context, d time.Duration) error
	Logger *slog.Logger
}

// Client calls apps' internal Slack API. It is safe for concurrent use.
type Client struct {
	baseURL string
	signer  Signer
	http    *http.Client
	retries int
	maxWait time.Duration
	sleep   func(ctx context.Context, d time.Duration) error
	logger  *slog.Logger
}

// New returns a client for the apps at config.BaseURL.
func New(config Config) (*Client, error) {
	baseURL, err := runtime.ValidateAPIURL(strings.TrimSpace(config.BaseURL))
	if err != nil {
		return nil, fmt.Errorf("appsclient: apps' URL: %w", err)
	}
	if config.Signer == nil {
		return nil, errors.New("appsclient: a service token signer is required")
	}
	httpClient := config.HTTP
	if httpClient == nil {
		httpClient = &http.Client{Timeout: requestTimeout, Transport: observability.Transport(nil)}
	}
	copied := *httpClient
	copied.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }

	retries := config.Retries
	switch {
	case retries == 0:
		retries = DefaultRetries
	case retries < 0:
		retries = 0
	}
	maxWait := config.MaxWait
	if maxWait <= 0 {
		maxWait = DefaultMaxWait
	}
	sleep := config.Sleep
	if sleep == nil {
		sleep = sleepContext
	}
	logger := config.Logger
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	return &Client{baseURL: baseURL, signer: config.Signer, http: &copied, retries: retries, maxWait: maxWait, sleep: sleep, logger: logger}, nil
}

func sleepContext(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// ---- Errors ----

// Error is apps refusing a call, as it said so: the HTTP status, its error
// code, and its own words. The words are apps' and never hold what a caller
// sent.
type Error struct {
	Status  int
	Code    string
	Message string
}

func (e *Error) Error() string {
	text := "apps answered " + strconv.Itoa(e.Status)
	if e.Code != "" {
		text += " " + e.Code
	}
	if e.Message != "" {
		text += ": " + e.Message
	}
	return text
}

// RateLimitedError is apps, or Slack behind it, asking to be called less
// often, still, after this client waited as long as it was willing to. It says
// how long to wait, in the form slackrouter's worker reads.
type RateLimitedError struct {
	After time.Duration
}

func (e *RateLimitedError) Error() string {
	return "apps rate limited the call; retry in " + e.After.String()
}

// RetryAfter is how long apps asked to be left alone.
func (e *RateLimitedError) RetryAfter() time.Duration { return e.After }

// slackRefusals are the codes with which apps reports Slack refusing one
// particular message for a reason no retry changes: the conversation or the
// message is gone, the bot is not in it, or the person cannot be reached.
var slackRefusals = map[string]bool{
	"channel_not_found": true, "is_archived": true, "not_in_channel": true, "message_not_found": true,
	"cant_update_message": true, "edit_window_closed": true, "user_not_found": true, "user_disabled": true,
	"channel_is_archived": true, "cannot_dm_bot": true,
}

const slackRefusedPrefix = "slack refused the request: "

// classify turns apps' refusal into what a caller acts on.
func classify(refusal *Error) error {
	switch {
	case refusal.Status == http.StatusNotFound || refusal.Status == http.StatusGone:
		return fmt.Errorf("%w: %v", slackrouter.ErrGone, refusal)
	case refusal.Status == http.StatusUnprocessableEntity && refusal.Code == "channel_unreadable":
		return fmt.Errorf("%w: %v", slackrouter.ErrUnreadable, refusal)
	case refusal.Status == http.StatusConflict && refusal.Code == "not_connected":
		// The install has ended: nothing sent again works until the workspace
		// installs the app again.
		return slackrouter.Permanent(refusal)
	case refusal.Status == http.StatusConflict, refusal.Status == http.StatusUnauthorized, refusal.Status == http.StatusRequestTimeout,
		(refusal.Status >= http.StatusInternalServerError && refusal.Code != "slack_error"):
		// A key whose post is still being made, a token that could not be
		// checked just now, apps or a service behind it not answering: it may
		// work when tried again.
		return refusal
	case refusal.Code == "slack_error":
		code := strings.TrimSpace(strings.TrimPrefix(refusal.Message, slackRefusedPrefix))
		if slackRefusals[code] {
			return slackrouter.Permanent(refusal)
		}
		return refusal
	case refusal.Status >= http.StatusBadRequest && refusal.Status < http.StatusInternalServerError:
		// Understood and refused: a template that will not take those params, a
		// thread apps has not seen, a caller it does not admit for this purpose.
		return slackrouter.Permanent(refusal)
	}
	return refusal
}

// ---- Requests ----

// do makes one call, and honours a rate limit: when apps asks for a wait no
// longer than MaxWait it waits that long and makes the same call again, up to
// Retries times. A longer wait is not sat out here, where it would hold a
// caller's slot for nothing: the error says how long, and the caller's own
// retry does the waiting. Each attempt carries a token of its own. A body that
// decodes is decoded into out when out is set. route names the call in logs.
func (c *Client) do(ctx context.Context, route, method, path string, query url.Values, body any, out any) error {
	var encoded []byte
	if body != nil {
		var err error
		if encoded, err = json.Marshal(body); err != nil {
			return fmt.Errorf("appsclient: encode the request: %w", err)
		}
	}
	target := c.baseURL + path
	if len(query) > 0 {
		target += "?" + query.Encode()
	}
	for attempt := 0; ; attempt++ {
		refused, retryAfter, err := c.once(ctx, method, target, encoded, out)
		if err != nil {
			return err
		}
		if refused == nil {
			return nil
		}
		if refused.Status != http.StatusTooManyRequests {
			return classify(refused)
		}
		if attempt >= c.retries || retryAfter > c.maxWait {
			return &RateLimitedError{After: retryAfter}
		}
		c.logger.WarnContext(ctx, "apps rate limited a Slack call; trying again",
			"route", route, "retry_in", retryAfter.String(), "attempt", attempt+1)
		if err := c.sleep(ctx, retryAfter); err != nil {
			return err
		}
	}
}

// once makes a single attempt. It answers what apps refused with, when it
// refused, and the wait a 429 asked for; an error is what stopped the attempt
// from being made or read.
func (c *Client) once(ctx context.Context, method, target string, encoded []byte, out any) (*Error, time.Duration, error) {
	token, err := c.signer.Token(ctx, Audience, Purpose)
	if err != nil {
		return nil, 0, fmt.Errorf("appsclient: sign this service's token: %w", err)
	}
	var reader io.Reader
	if encoded != nil {
		reader = bytes.NewReader(encoded)
	}
	request, err := http.NewRequestWithContext(ctx, method, target, reader)
	if err != nil {
		return nil, 0, fmt.Errorf("appsclient: build the request: %w", err)
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set(serviceauth.Header, token)
	if encoded != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := c.http.Do(request)
	if err != nil {
		if ctx.Err() != nil {
			return nil, 0, ctx.Err()
		}
		// The address is apps' own and holds nothing a caller sent; a transport
		// error can carry the URL, which is why it is the one part kept.
		return nil, 0, fmt.Errorf("appsclient: apps did not answer: %w", scrubURL(err))
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes))
	if err != nil {
		return nil, 0, fmt.Errorf("appsclient: read apps' answer: %w", scrubURL(err))
	}
	if response.StatusCode == http.StatusOK {
		if out == nil {
			return nil, 0, nil
		}
		if err := json.Unmarshal(data, out); err != nil {
			return nil, 0, errors.New("appsclient: apps' answer could not be read")
		}
		return nil, 0, nil
	}
	refusal := &Error{Status: response.StatusCode}
	var failure struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal(data, &failure) == nil {
		refusal.Code, refusal.Message = failure.Error.Code, boundedMessage(failure.Error.Message)
	}
	return refusal, waitFrom(response.Header), nil
}

// waitFrom reads how long a 429 asked to wait: whole seconds, at least one.
func waitFrom(header http.Header) time.Duration {
	seconds, err := strconv.Atoi(strings.TrimSpace(header.Get("Retry-After")))
	if err != nil || seconds < 1 {
		return defaultRetryAfter
	}
	return time.Duration(seconds) * time.Second
}

// boundedMessage keeps apps' words short: they are put in errors and logs.
func boundedMessage(message string) string {
	message = strings.TrimSpace(message)
	if runes := []rune(message); len(runes) > 300 {
		message = string(runes[:300]) + "…"
	}
	return message
}

// scrubURL takes the URL out of a transport error, which is the only part of
// one that is not apps' answer.
func scrubURL(err error) error {
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		return urlErr.Err
	}
	return err
}

// ---- Inbound ----

type inboundView struct {
	ID             string    `json:"id"`
	OrganizationID string    `json:"organization_id"`
	TeamID         string    `json:"team_id"`
	Kind           string    `json:"kind"`
	ReceivedAt     time.Time `json:"received_at"`
	ChannelID      string    `json:"channel_id"`
	UserID         string    `json:"user_id"`
	MessageTS      string    `json:"message_ts"`
	ThreadTS       string    `json:"thread_ts"`
	BotUserID      string    `json:"bot_user_id"`
	Text           string    `json:"text"`
	ChannelType    string    `json:"channel_type"`
	ExtShared      bool      `json:"ext_shared"`
	ActionID       string    `json:"action_id"`
	ActionValue    string    `json:"action_value"`
}

// Inbound reads one verified inbound row by id. It is
// slackrouter.ErrGone once apps purged it.
func (c *Client) Inbound(ctx context.Context, id string) (slackrouter.Inbound, error) {
	if strings.TrimSpace(id) == "" {
		return slackrouter.Inbound{}, slackrouter.Permanent(errors.New("appsclient: an inbound row is read by its id"))
	}
	var view inboundView
	if err := c.do(ctx, "inbound", http.MethodGet, inboundPath+url.PathEscape(id), nil, nil, &view); err != nil {
		return slackrouter.Inbound{}, err
	}
	inbound := slackrouter.Inbound{
		ID: view.ID, OrganizationID: view.OrganizationID, TeamID: view.TeamID, Kind: view.Kind, ReceivedAt: view.ReceivedAt,
	}
	switch view.Kind {
	case "message":
		inbound.Kind = slackrouter.KindMessage
		inbound.Mention = &slackrouter.Mention{
			ChannelID: view.ChannelID, ChannelType: view.ChannelType, UserID: view.UserID, Text: view.Text,
			TS: view.MessageTS, ThreadTS: view.ThreadTS, BotUserID: view.BotUserID, SharedExternally: view.ExtShared,
		}
	case "action":
		inbound.Kind = slackrouter.KindAction
		inbound.Click = &slackrouter.Click{
			UserID: view.UserID, ChannelID: view.ChannelID, MessageTS: view.MessageTS, ActionID: view.ActionID, Value: view.ActionValue,
		}
	}
	return inbound, nil
}

// ---- Profiles ----

type profileView struct {
	UserID         string `json:"user_id"`
	TeamID         string `json:"team_id"`
	Email          string `json:"email"`
	EmailConfirmed bool   `json:"email_confirmed"`
	Name           string `json:"name"`
	// Title is the job title the person set in Slack. apps leaves it out for
	// one who set none.
	Title      string `json:"title"`
	IsGuest    bool   `json:"is_guest"`
	IsBot      bool   `json:"is_bot"`
	IsDeleted  bool   `json:"is_deleted"`
	IsStranger bool   `json:"is_stranger"`
}

// Profiles reads the profiles of some users of the organization's workspace,
// from apps' day of cache. A user Slack does not know is left out. apps names
// the workspace by the organization's install, so teamID is not sent.
func (c *Client) Profiles(ctx context.Context, organizationID, _ string, userIDs []string) ([]slackrouter.Profile, error) {
	seen := make(map[string]bool, len(userIDs))
	ids := make([]string, 0, len(userIDs))
	for _, id := range userIDs {
		if id = strings.TrimSpace(id); id != "" && !seen[id] {
			seen[id] = true
			ids = append(ids, id)
		}
	}
	profiles := make([]slackrouter.Profile, 0, len(ids))
	for start := 0; start < len(ids); start += maxProfiles {
		chunk := ids[start:min(start+maxProfiles, len(ids))]
		var view struct {
			Profiles []profileView `json:"profiles"`
		}
		body := map[string]any{"organization_id": organizationID, "user_ids": chunk}
		if err := c.do(ctx, "profiles", http.MethodPost, profilesPath, nil, body, &view); err != nil {
			return nil, err
		}
		for _, profile := range view.Profiles {
			profiles = append(profiles, slackrouter.Profile{
				UserID: profile.UserID, TeamID: profile.TeamID, Email: profile.Email, EmailConfirmed: profile.EmailConfirmed,
				Name: profile.Name, Title: profile.Title, Guest: profile.IsGuest, Bot: profile.IsBot, Deleted: profile.IsDeleted, Stranger: profile.IsStranger,
			})
		}
	}
	return profiles, nil
}

// ---- Channels ----

// ChannelDetails is a conversation as apps reads it from Slack now, with its
// name: what Channel reduces to what the router's policy needs.
type ChannelDetails struct {
	slackrouter.ChannelInfo
	// Name is the channel's name without its "#", empty for a conversation
	// that has none.
	Name string
}

type channelView struct {
	ChannelID    string `json:"channel_id"`
	Name         string `json:"name"`
	IsPrivate    bool   `json:"is_private"`
	IsShared     bool   `json:"is_shared"`
	IsMPIM       bool   `json:"is_mpim"`
	IsIM         bool   `json:"is_im"`
	HighPriority bool   `json:"high_priority"`
}

// ChannelDetails reads a conversation now, never from a cache. It is
// slackrouter.ErrGone when Slack does not know it and
// slackrouter.ErrUnreadable when Slack will not describe it to the bot.
func (c *Client) ChannelDetails(ctx context.Context, organizationID, channelID string) (ChannelDetails, error) {
	if strings.TrimSpace(channelID) == "" {
		return ChannelDetails{}, slackrouter.Permanent(errors.New("appsclient: a conversation is read by its id"))
	}
	var view channelView
	query := url.Values{"organization_id": {organizationID}}
	if err := c.do(ctx, "channel", http.MethodGet, channelsPath+url.PathEscape(channelID), query, nil, &view); err != nil {
		return ChannelDetails{}, err
	}
	return ChannelDetails{
		ChannelInfo: slackrouter.ChannelInfo{
			ID: view.ChannelID, Private: view.IsPrivate, Shared: view.IsShared, MPIM: view.IsMPIM, IM: view.IsIM, HighPriority: view.HighPriority,
		},
		Name: view.Name,
	}, nil
}

// Channel reads a conversation now, as slackrouter.Slack asks.
func (c *Client) Channel(ctx context.Context, organizationID, channelID string) (slackrouter.ChannelInfo, error) {
	details, err := c.ChannelDetails(ctx, organizationID, channelID)
	return details.ChannelInfo, err
}

// ---- Posting ----

// Posted is where a post went.
type Posted struct {
	ChannelID string `json:"channel_id"`
	// TS is the message's timestamp, which is what Update names it by. An
	// ephemeral message's cannot be updated.
	TS        string `json:"ts"`
	Ephemeral bool   `json:"ephemeral"`
	// Replayed says the idempotency key had been used, and this is where the
	// first post went: nothing new was sent.
	Replayed bool `json:"replayed"`
}

type postBody struct {
	OrganizationID string             `json:"organization_id"`
	IdempotencyKey string             `json:"idempotency_key"`
	Target         slackrouter.Target `json:"target"`
	Template       string             `json:"template"`
	Params         json.RawMessage    `json:"params"`
}

// Send posts a templated message and says where it went. Sending the same
// IdempotencyKey again sends nothing more: apps answers where the first one
// went. What the message says is decided in apps from the template and the
// params; a template it will not fill, and a thread or person it has not seen
// in a verified message, are slackrouter.Permanent.
func (c *Client) Send(ctx context.Context, post slackrouter.Post) (Posted, error) {
	params, err := encodeParams(post.Params)
	if err != nil {
		return Posted{}, err
	}
	var posted Posted
	body := postBody{
		OrganizationID: post.OrganizationID, IdempotencyKey: post.IdempotencyKey,
		Target: post.Target, Template: post.Template, Params: params,
	}
	if err := c.do(ctx, "post", http.MethodPost, postPath, nil, body, &posted); err != nil {
		return Posted{}, err
	}
	return posted, nil
}

// Post sends a message, as slackrouter.Slack asks: Send without where it went.
func (c *Client) Post(ctx context.Context, post slackrouter.Post) error {
	_, err := c.Send(ctx, post)
	return err
}

// Update names a message apps posted, and replaces it with another template's
// rendering, as a decided notice replaces its buttons.
type Update struct {
	OrganizationID string
	ChannelID      string
	TS             string
	Template       string
	Params         any
}

type updateBody struct {
	OrganizationID string          `json:"organization_id"`
	ChannelID      string          `json:"channel_id"`
	TS             string          `json:"ts"`
	Template       string          `json:"template"`
	Params         json.RawMessage `json:"params"`
}

// Update replaces a message apps posted. It is slackrouter.ErrGone when apps
// did not post that message (or its record is gone), and Permanent for one that
// cannot be updated, such as an ephemeral one. Doing it again with the same
// template and params leaves the message as it is.
func (c *Client) Update(ctx context.Context, update Update) error {
	params, err := encodeParams(update.Params)
	if err != nil {
		return err
	}
	body := updateBody{
		OrganizationID: update.OrganizationID, ChannelID: update.ChannelID, TS: update.TS,
		Template: update.Template, Params: params,
	}
	return c.do(ctx, "update", http.MethodPost, updatePath, nil, body, nil)
}

// encodeParams is a template's params as the JSON apps reads, or {} for none.
func encodeParams(params any) (json.RawMessage, error) {
	if params == nil {
		return json.RawMessage(`{}`), nil
	}
	encoded, err := json.Marshal(params)
	if err != nil {
		return nil, slackrouter.Permanent(fmt.Errorf("appsclient: encode the params: %w", err))
	}
	return encoded, nil
}
