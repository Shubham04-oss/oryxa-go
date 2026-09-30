package slackbot

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/Shubham04-oss/oryxa-go/apps/internal/domain"
	"github.com/Shubham04-oss/oryxa-go/internal/observability"
	"github.com/Shubham04-oss/oryxa-go/internal/safehttp"
	"github.com/Shubham04-oss/oryxa-go/internal/secrets"
)

// DefaultAPIBaseURL is Slack's Web API.
const DefaultAPIBaseURL = "https://slack.com/api"

const (
	// callTimeout bounds one Web API call.
	callTimeout = 15 * time.Second
	// maxAnswerBytes bounds what is read of one answer.
	maxAnswerBytes = 1 << 20
	// defaultRetryAfter is how long to wait when Slack rate limits a call and
	// does not say for how long.
	defaultRetryAfter = time.Second
)

// Doer sends one HTTP request. *http.Client is one.
type Doer interface {
	Do(*http.Request) (*http.Response, error)
}

// Error is Slack refusing a call: HTTP 200 and "ok": false, with a short
// error code such as channel_not_found. It never holds a token.
type Error struct {
	Method string
	Code   string
}

func (e *Error) Error() string { return "slack " + e.Method + ": " + e.Code }

// tokenDead says Slack is refusing the bot's token itself, which no retry
// mends: the install is over. not_authed is not among them: it says no token
// was sent, which is this code's fault and not the install's.
func (e *Error) tokenDead() bool {
	switch e.Code {
	case "invalid_auth", "token_revoked", "account_inactive", "token_expired":
		return true
	}
	return false
}

// RateLimitedError is Slack, or apps' own budget for Slack, asking to be
// called less often. It is domain.ErrRateLimited, and says for how long.
type RateLimitedError struct {
	Method     string
	RetryAfter time.Duration
}

func (e *RateLimitedError) Error() string {
	if e.Method == "" {
		return "too many messages for this organization; retry in " + e.RetryAfter.String()
	}
	return "slack " + e.Method + ": ratelimited; retry in " + e.RetryAfter.String()
}

func (e *RateLimitedError) Unwrap() error { return domain.ErrRateLimited }

// RetryAfterSeconds is the wait, in whole seconds and at least one, as
// Retry-After says it.
func (e *RateLimitedError) RetryAfterSeconds() int {
	return max(1, int((e.RetryAfter+time.Second-1)/time.Second))
}

// Client is the part of Slack's Web API the surface uses. It sends the bot's
// token as a bearer, never anywhere but to the API, and never says it in an
// error. Link unfurling is off in everything it posts, whatever the caller
// wants: there is no way to turn it on.
type Client struct {
	base string
	http Doer
}

// NewClient builds a client for base, "" being the real Slack. Any other base
// must be a loopback address, for a fake; doer, when given, is used as it is.
// Otherwise the real Slack is reached over https on the public internet only,
// and a loopback base is reached on loopback only, so neither can be turned on
// something else.
func NewClient(base string, doer Doer) (*Client, error) {
	base = strings.TrimRight(strings.TrimSpace(base), "/")
	if base == "" {
		base = DefaultAPIBaseURL
		if doer == nil {
			client := safehttp.NewClient(safehttp.HTTPSOnly(), safehttp.MaxRedirects(0), safehttp.Timeout(callTimeout))
			client.Transport = observability.Transport(client.Transport)
			doer = client
		}
		return &Client{base: base, http: doer}, nil
	}
	if err := CheckAPIBaseURL(base); err != nil {
		return nil, err
	}
	if doer == nil {
		doer = loopbackClient()
	}
	return &Client{base: base, http: doer}, nil
}

// CheckAPIBaseURL says whether raw may be APPS_SLACK_API_BASE_URL: an http or
// https URL on a loopback address, for a fake Slack in a test or a local run.
// Anything else would send the bot's token, and whatever the surface posts,
// somewhere it must not go.
func CheckAPIBaseURL(raw string) error {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return errors.New("the Slack API base URL must be an absolute URL with no credentials, query or fragment")
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return errors.New("the Slack API base URL must be http or https")
	}
	host := strings.ToLower(parsed.Hostname())
	if host == "localhost" {
		return nil
	}
	if address, err := netip.ParseAddr(host); err != nil || !address.IsLoopback() {
		return errors.New("the Slack API base URL must be on a loopback address, or empty for Slack itself")
	}
	return nil
}

// loopbackClient reaches loopback addresses and nothing else, whatever a
// hostname resolves to, and follows no redirect.
func loopbackClient() *http.Client {
	dialer := &net.Dialer{
		Timeout: 5 * time.Second,
		Control: func(_, address string, _ syscall.RawConn) error {
			host, _, err := net.SplitHostPort(address)
			if err != nil {
				return err
			}
			if parsed, err := netip.ParseAddr(host); err != nil || !parsed.IsLoopback() {
				return errors.New("slack: the API base URL reached an address that is not loopback")
			}
			return nil
		},
	}
	transport := &http.Transport{Proxy: nil, DialContext: dialer.DialContext, ResponseHeaderTimeout: callTimeout}
	return &http.Client{
		Transport:     observability.Transport(transport),
		Timeout:       callTimeout,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}

// answer is what every method's answer begins with.
type answer struct {
	OK    bool   `json:"ok"`
	Error string `json:"error"`
}

// call sends one request and answers the raw body of a successful answer. It
// turns Slack's refusals into Error, its rate limits into RateLimitedError,
// and anything it cannot get an answer from into domain.ErrUnavailable.
func (c *Client) call(ctx context.Context, method string, request *http.Request) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, callTimeout)
	defer cancel()
	response, err := c.http.Do(request.WithContext(ctx))
	if err != nil {
		if ctx.Err() != nil && !errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return nil, ctx.Err()
		}
		// The error is not quoted: a URL or a header in it may be a secret.
		return nil, fmt.Errorf("%w: slack %s did not answer", domain.ErrUnavailable, method)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, maxAnswerBytes))
	switch {
	case response.StatusCode == http.StatusTooManyRequests:
		return nil, &RateLimitedError{Method: method, RetryAfter: retryAfter(response.Header)}
	case response.StatusCode >= 500 || err != nil:
		return nil, fmt.Errorf("%w: slack %s answered %d", domain.ErrUnavailable, method, response.StatusCode)
	case response.StatusCode < 200 || response.StatusCode >= 300:
		return nil, &Error{Method: method, Code: "http_" + strconv.Itoa(response.StatusCode)}
	}
	var decoded answer
	if err := json.Unmarshal(body, &decoded); err != nil {
		return nil, fmt.Errorf("%w: slack %s answered something that is not JSON", domain.ErrUnavailable, method)
	}
	if decoded.OK {
		return body, nil
	}
	code := decoded.Error
	if code == "" {
		code = "unknown_error"
	}
	if code == "ratelimited" || code == "rate_limited" {
		return nil, &RateLimitedError{Method: method, RetryAfter: retryAfter(response.Header)}
	}
	return nil, &Error{Method: method, Code: truncate(code, 64)}
}

// retryAfter reads Retry-After as seconds. Slack always sends whole seconds;
// a missing or unreadable one is a second.
func retryAfter(header http.Header) time.Duration {
	if seconds, err := strconv.Atoi(strings.TrimSpace(header.Get("Retry-After"))); err == nil && seconds > 0 {
		return time.Duration(min(seconds, 3600)) * time.Second
	}
	return defaultRetryAfter
}

func truncate(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	return value[:limit]
}

// postJSON is a call with a JSON body and the bearer token, as the chat and
// conversation methods take.
func (c *Client) postJSON(ctx context.Context, method string, token secrets.Secret, body any) ([]byte, error) {
	encoded, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("encode slack %s: %w", method, err)
	}
	request, err := http.NewRequest(http.MethodPost, c.base+"/"+method, bytes.NewReader(encoded))
	if err != nil {
		return nil, fmt.Errorf("build slack %s: %w", method, err)
	}
	request.Header.Set("Content-Type", "application/json; charset=utf-8")
	request.Header.Set("Authorization", "Bearer "+token.Reveal())
	return c.call(ctx, method, request)
}

// AddReaction uses the bot token. Re-adding the same bot/emoji/message
// combination is a successful replay, as Slack keeps only one reaction.
func (c *Client) AddReaction(ctx context.Context, token secrets.Secret, channel, timestamp, name string) error {
	_, err := c.postJSON(ctx, "reactions.add", token, map[string]string{"channel": channel, "timestamp": timestamp, "name": name})
	var refused *Error
	if errors.As(err, &refused) && refused.Code == "already_reacted" {
		return nil
	}
	return err
}

// get is a call whose arguments are its query, as the read methods take.
func (c *Client) get(ctx context.Context, method string, token secrets.Secret, query url.Values) ([]byte, error) {
	target := c.base + "/" + method
	if len(query) > 0 {
		target += "?" + query.Encode()
	}
	request, err := http.NewRequest(http.MethodGet, target, nil)
	if err != nil {
		return nil, fmt.Errorf("build slack %s: %w", method, err)
	}
	request.Header.Set("Authorization", "Bearer "+token.Reveal())
	return c.call(ctx, method, request)
}

// postForm is a call with a form body, as the OAuth methods take, whose
// client credentials go in HTTP Basic, where Slack asks for them.
func (c *Client) postForm(ctx context.Context, method string, token secrets.Secret, form url.Values, clientID string, clientSecret secrets.Secret) ([]byte, error) {
	request, err := http.NewRequest(http.MethodPost, c.base+"/"+method, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, fmt.Errorf("build slack %s: %w", method, err)
	}
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token.Reveal())
	}
	if clientID != "" {
		request.SetBasicAuth(clientID, clientSecret.Reveal())
	}
	return c.call(ctx, method, request)
}

// OAuthAccess is what Slack answers a code with: a bot install.
type OAuthAccess struct {
	AccessToken secrets.Secret
	TokenType   string
	// Scopes are what the install was granted.
	Scopes    []string
	BotUserID string
	AppID     string
	TeamID    string
	TeamName  string
	// Enterprise says the install is an Enterprise Grid organization's, which
	// v1 refuses.
	Enterprise bool
	// Rotating says the token expires and comes with a refresh token, which is
	// what token rotation does, and which v1 refuses.
	Rotating bool
}

// OAuthAccess exchanges the code an install's callback brought (oauth.v2.access).
func (c *Client) OAuthAccess(ctx context.Context, clientID string, clientSecret secrets.Secret, code, redirectURI string) (OAuthAccess, error) {
	body, err := c.postForm(ctx, "oauth.v2.access", "", url.Values{"code": {code}, "redirect_uri": {redirectURI}}, clientID, clientSecret)
	if err != nil {
		return OAuthAccess{}, err
	}
	var decoded struct {
		AccessToken string `json:"access_token"`
		TokenType   string `json:"token_type"`
		Scope       string `json:"scope"`
		BotUserID   string `json:"bot_user_id"`
		AppID       string `json:"app_id"`
		Team        struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"team"`
		Enterprise *struct {
			ID string `json:"id"`
		} `json:"enterprise"`
		IsEnterpriseInstall bool            `json:"is_enterprise_install"`
		RefreshToken        string          `json:"refresh_token"`
		ExpiresIn           json.RawMessage `json:"expires_in"`
	}
	if err := json.Unmarshal(body, &decoded); err != nil {
		return OAuthAccess{}, fmt.Errorf("%w: slack oauth.v2.access answered something unreadable", domain.ErrUnavailable)
	}
	access := OAuthAccess{
		AccessToken: secrets.Secret(decoded.AccessToken), TokenType: decoded.TokenType,
		BotUserID: decoded.BotUserID, AppID: decoded.AppID, TeamID: decoded.Team.ID, TeamName: decoded.Team.Name,
		Enterprise: decoded.IsEnterpriseInstall || (decoded.Enterprise != nil && decoded.Enterprise.ID != ""),
		Rotating:   decoded.RefreshToken != "" || (len(decoded.ExpiresIn) > 0 && string(decoded.ExpiresIn) != "null" && string(decoded.ExpiresIn) != "0"),
	}
	for _, scope := range strings.Split(decoded.Scope, ",") {
		if scope = strings.TrimSpace(scope); scope != "" {
			access.Scopes = append(access.Scopes, scope)
		}
	}
	return access, nil
}

// AuthTest is what auth.test says about a token.
type AuthTest struct {
	TeamID     string
	UserID     string
	Enterprise bool
}

// AuthTest asks who a token belongs to.
func (c *Client) AuthTest(ctx context.Context, token secrets.Secret) (AuthTest, error) {
	body, err := c.postJSON(ctx, "auth.test", token, struct{}{})
	if err != nil {
		return AuthTest{}, err
	}
	var decoded struct {
		TeamID              string `json:"team_id"`
		UserID              string `json:"user_id"`
		IsEnterpriseInstall bool   `json:"is_enterprise_install"`
	}
	if err := json.Unmarshal(body, &decoded); err != nil {
		return AuthTest{}, fmt.Errorf("%w: slack auth.test answered something unreadable", domain.ErrUnavailable)
	}
	return AuthTest{TeamID: decoded.TeamID, UserID: decoded.UserID, Enterprise: decoded.IsEnterpriseInstall}, nil
}

// Uninstall removes the app from the workspace its token belongs to
// (apps.uninstall), which is what disconnecting Slack in Oryxa asks for.
func (c *Client) Uninstall(ctx context.Context, token secrets.Secret, clientID string, clientSecret secrets.Secret) error {
	_, err := c.postForm(ctx, "apps.uninstall", token, url.Values{"client_id": {clientID}, "client_secret": {clientSecret.Reveal()}}, "", "")
	return err
}

// User is a person, as users.info says.
type User struct {
	ID     string
	TeamID string
	Name   string
	Email  string
	// EmailConfirmed is Slack's is_email_confirmed: whether the person has
	// confirmed the address, which is what makes it something to match a
	// member by. An answer that does not say is unconfirmed.
	EmailConfirmed bool
	Title          string
	Deleted        bool
	Bot            bool
	Guest          bool
	Stranger       bool
}

// User reads one person (users.info).
func (c *Client) User(ctx context.Context, token secrets.Secret, id string) (User, error) {
	body, err := c.get(ctx, "users.info", token, url.Values{"user": {id}})
	if err != nil {
		return User{}, err
	}
	var decoded struct {
		User struct {
			ID                string `json:"id"`
			TeamID            string `json:"team_id"`
			Name              string `json:"name"`
			RealName          string `json:"real_name"`
			Deleted           bool   `json:"deleted"`
			IsBot             bool   `json:"is_bot"`
			IsAppUser         bool   `json:"is_app_user"`
			IsRestricted      bool   `json:"is_restricted"`
			IsUltraRestricted bool   `json:"is_ultra_restricted"`
			IsStranger        bool   `json:"is_stranger"`
			IsEmailConfirmed  bool   `json:"is_email_confirmed"`
			Profile           struct {
				Email       string `json:"email"`
				DisplayName string `json:"display_name"`
				RealName    string `json:"real_name"`
				Title       string `json:"title"`
			} `json:"profile"`
		} `json:"user"`
	}
	if err := json.Unmarshal(body, &decoded); err != nil {
		return User{}, fmt.Errorf("%w: slack users.info answered something unreadable", domain.ErrUnavailable)
	}
	u := decoded.User
	name := firstNonEmpty(u.RealName, u.Profile.RealName, u.Profile.DisplayName, u.Name)
	return User{
		ID: u.ID, TeamID: u.TeamID, Name: name, Email: strings.TrimSpace(u.Profile.Email), EmailConfirmed: u.IsEmailConfirmed,
		Title: u.Profile.Title, Deleted: u.Deleted, Bot: u.IsBot || u.IsAppUser || u.ID == "USLACKBOT",
		Guest: u.IsRestricted || u.IsUltraRestricted, Stranger: u.IsStranger,
	}, nil
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

// Conversation is a channel or a direct message, as conversations.info says.
type Conversation struct {
	ID       string
	Name     string
	IM       bool
	MPIM     bool
	Private  bool
	Archived bool
	// Shared is every kind of sharing: with another organization through
	// Slack Connect, or across an Enterprise organization's workspaces.
	Shared bool
}

type conversationJSON struct {
	ID                 string `json:"id"`
	Name               string `json:"name"`
	IsIM               bool   `json:"is_im"`
	IsMPIM             bool   `json:"is_mpim"`
	IsPrivate          bool   `json:"is_private"`
	IsGroup            bool   `json:"is_group"`
	IsArchived         bool   `json:"is_archived"`
	IsShared           bool   `json:"is_shared"`
	IsExtShared        bool   `json:"is_ext_shared"`
	IsOrgShared        bool   `json:"is_org_shared"`
	IsPendingExtShared bool   `json:"is_pending_ext_shared"`
}

func (j conversationJSON) conversation() Conversation {
	return Conversation{
		ID: j.ID, Name: j.Name, IM: j.IsIM, MPIM: j.IsMPIM, Private: j.IsPrivate || j.IsGroup || j.IsIM || j.IsMPIM,
		Archived: j.IsArchived, Shared: j.IsShared || j.IsExtShared || j.IsOrgShared || j.IsPendingExtShared,
	}
}

// Conversation reads one channel (conversations.info).
func (c *Client) Conversation(ctx context.Context, token secrets.Secret, id string) (Conversation, error) {
	body, err := c.get(ctx, "conversations.info", token, url.Values{"channel": {id}})
	if err != nil {
		return Conversation{}, err
	}
	var decoded struct {
		Channel conversationJSON `json:"channel"`
	}
	if err := json.Unmarshal(body, &decoded); err != nil {
		return Conversation{}, fmt.Errorf("%w: slack conversations.info answered something unreadable", domain.ErrUnavailable)
	}
	return decoded.Channel.conversation(), nil
}

// Conversations lists the channels of the types the bot can see, one page at
// a time (conversations.list), and answers the cursor of the next.
func (c *Client) Conversations(ctx context.Context, token secrets.Secret, types, cursor string, limit int) ([]Conversation, string, error) {
	query := url.Values{"types": {types}, "exclude_archived": {"true"}, "limit": {strconv.Itoa(limit)}}
	if cursor != "" {
		query.Set("cursor", cursor)
	}
	body, err := c.get(ctx, "conversations.list", token, query)
	if err != nil {
		return nil, "", err
	}
	var decoded struct {
		Channels         []conversationJSON `json:"channels"`
		ResponseMetadata struct {
			NextCursor string `json:"next_cursor"`
		} `json:"response_metadata"`
	}
	if err := json.Unmarshal(body, &decoded); err != nil {
		return nil, "", fmt.Errorf("%w: slack conversations.list answered something unreadable", domain.ErrUnavailable)
	}
	channels := make([]Conversation, 0, len(decoded.Channels))
	for _, channel := range decoded.Channels {
		channels = append(channels, channel.conversation())
	}
	return channels, decoded.ResponseMetadata.NextCursor, nil
}

// OpenDM opens the bot's direct message with a person, and answers its
// conversation id (conversations.open).
func (c *Client) OpenDM(ctx context.Context, token secrets.Secret, userID string) (string, error) {
	body, err := c.postJSON(ctx, "conversations.open", token, map[string]any{"users": userID})
	if err != nil {
		return "", err
	}
	var decoded struct {
		Channel struct {
			ID string `json:"id"`
		} `json:"channel"`
	}
	if err := json.Unmarshal(body, &decoded); err != nil || decoded.Channel.ID == "" {
		return "", fmt.Errorf("%w: slack conversations.open answered no conversation", domain.ErrUnavailable)
	}
	return decoded.Channel.ID, nil
}

// Message is what to post. Blocks are Slack's Block Kit, built by a template
// and never by a caller; Text is what notifications and screen readers show.
type Message struct {
	Channel string
	// User makes it an ephemeral message, seen by that person alone.
	User     string
	ThreadTS string
	Text     string
	Blocks   []any
}

// Post posts a message (chat.postMessage), or an ephemeral one when it names
// a user (chat.postEphemeral), with link and media unfurling off. It answers
// the message's timestamp.
func (c *Client) Post(ctx context.Context, token secrets.Secret, message Message) (string, error) {
	body := map[string]any{
		"channel": message.Channel, "text": message.Text, "unfurl_links": false, "unfurl_media": false,
	}
	if len(message.Blocks) > 0 {
		body["blocks"] = message.Blocks
	}
	if message.ThreadTS != "" {
		body["thread_ts"] = message.ThreadTS
	}
	method := "chat.postMessage"
	if message.User != "" {
		method = "chat.postEphemeral"
		body["user"] = message.User
	}
	raw, err := c.postJSON(ctx, method, token, body)
	if err != nil {
		return "", err
	}
	var decoded struct {
		TS        string `json:"ts"`
		MessageTS string `json:"message_ts"`
	}
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return "", fmt.Errorf("%w: slack %s answered something unreadable", domain.ErrUnavailable, method)
	}
	if ts := firstNonEmpty(decoded.TS, decoded.MessageTS); ts != "" {
		return ts, nil
	}
	return "", fmt.Errorf("%w: slack %s answered no timestamp", domain.ErrUnavailable, method)
}

// Update replaces a message's text and blocks (chat.update). It always sends
// blocks, so what it replaces is replaced whole: a settled notice loses its
// buttons.
func (c *Client) Update(ctx context.Context, token secrets.Secret, channel, ts, text string, blocks []any) error {
	_, err := c.postJSON(ctx, "chat.update", token, map[string]any{"channel": channel, "ts": ts, "text": text, "blocks": blocks})
	return err
}

// OpenView opens a modal for the person whose click trigger is (views.open),
// which Slack takes once and for three seconds after the click. It needs no
// scope of its own: the bot's token is enough.
func (c *Client) OpenView(ctx context.Context, token secrets.Secret, trigger string, view map[string]any) error {
	_, err := c.postJSON(ctx, "views.open", token, map[string]any{"trigger_id": trigger, "view": view})
	return err
}
