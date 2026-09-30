package slackbot

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	appsv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/apps/v1"

	"github.com/Shubham04-oss/oryxa-go/apps/internal/domain"
	"github.com/Shubham04-oss/oryxa-go/internal/secrets"
)

// The internal API is what services with the purpose slack ask of the
// surface. It is the only way anything reaches Slack, and only apps knows the
// bot token: a service names an organization, a thread and a template, and
// apps checks that the thread is one it saw, renders the message, and posts it.

const (
	// profileLifetime is how long what Slack said about a person is kept.
	profileLifetime = 24 * time.Hour
	// maxProfiles bounds one profiles request.
	maxProfiles = 20
	// claimLease is how long a post being made is left alone before another
	// request may take it over: long enough for Slack to answer, short
	// enough that a maker that died does not strand its message.
	claimLease = 30 * time.Second
	// finishTimeout bounds recording what happened once Slack has answered.
	finishTimeout = 10 * time.Second
)

var (
	// ErrThreadUnseen means a post named a thread, or a person, that no
	// verified message or click of the organization's has been in: the
	// surface posts into conversations people opened with it and nowhere else.
	ErrThreadUnseen = fmt.Errorf("apps has not seen that conversation in a verified Slack message: %w", domain.ErrForbidden)
	// ErrChannelUnreadable means Slack would not describe a conversation to
	// the bot: its type needs a read scope the app does not ask for, which is
	// what a group message is.
	ErrChannelUnreadable = errors.New("slack would not describe that conversation to the bot")
)

// activeInstall is the organization's install, when it can post.
func (s *Service) activeInstall(ctx context.Context, organizationID string) (Install, error) {
	if strings.TrimSpace(organizationID) == "" {
		return Install{}, fmt.Errorf("an organization is required: %w", domain.ErrInvalid)
	}
	install, err := s.store.InstallForOrganization(ctx, organizationID)
	switch {
	case errors.Is(err, domain.ErrNotFound):
		return Install{}, fmt.Errorf("Slack is not installed for this organization: %w", domain.ErrNotConnected)
	case err != nil:
		return Install{}, err
	case !install.Active():
		return Install{}, fmt.Errorf("the organization's Slack install has ended: %w", domain.ErrNotConnected)
	}
	return install, nil
}

// slackFailed is what a caller is told of a call Slack refused. A token Slack
// says is dead ends the install, since nothing more will work through it.
func (s *Service) slackFailed(ctx context.Context, install Install, err error) error {
	var refused *Error
	if errors.As(err, &refused) && refused.tokenDead() {
		s.endDead(ctx, install)
		return fmt.Errorf("Slack refused the bot's token (%s), so the install has ended: %w", refused.Code, domain.ErrNotConnected)
	}
	return err
}

// endDead ends an install whose token Slack refused.
func (s *Service) endDead(ctx context.Context, install Install) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), finishTimeout)
	defer cancel()
	now := s.now().UTC()
	_, ended, err := s.store.EndInstall(ctx, install.TeamID, appsv1.SlackTokenInvalid, now, func(ended Install) (domain.Event, error) {
		return s.event(appsv1.SlackInstallChangedDetailType, appsv1.SlackInstallChanged{
			OrganizationID: ended.OrganizationID, TeamID: ended.TeamID, State: appsv1.SlackInstallEnded,
			Reason: appsv1.SlackTokenInvalid, ChangedAt: now,
		}, now)
	})
	s.tokens.forget(install.TeamID)
	switch {
	case err != nil:
		s.logger.ErrorContext(ctx, "end a Slack install whose token Slack refused", "team_id", install.TeamID, "error", err)
	case ended:
		s.logger.WarnContext(ctx, "slack refused the bot's token; the install ended", "organization_id", install.OrganizationID, "team_id", install.TeamID)
	}
}

// ---- Inbound records ----

// InboundView is an inbound record as a service reads it.
type InboundView struct {
	ID             string `json:"id"`
	OrganizationID string `json:"organization_id"`
	TeamID         string `json:"team_id"`
	// Kind is message for an @-mention and action for a click.
	Kind       string    `json:"kind"`
	ReceivedAt time.Time `json:"received_at"`
	ChannelID  string    `json:"channel_id"`
	// UserID is who wrote the message or clicked.
	UserID string `json:"user_id"`
	// MessageTS is the message's own timestamp, or the message a click was
	// on; ThreadTS is the root of its thread, where a reply goes.
	MessageTS string `json:"message_ts"`
	ThreadTS  string `json:"thread_ts"`
	// BotUserID is the bot in the workspace, whose mention starts the text.
	BotUserID string `json:"bot_user_id,omitempty"`

	// For a message: Slack's event id, the text as typed, the people it
	// mentions (the bot left out), and what Slack said of the channel.
	EventID     string   `json:"event_id,omitempty"`
	Text        string   `json:"text,omitempty"`
	Mentions    []string `json:"mentions,omitempty"`
	ChannelType string   `json:"channel_type,omitempty"`
	ExtShared   bool     `json:"ext_shared,omitempty"`

	// For a click: which button, and its value, which for Oryxa's notices is
	// an opaque ticket.
	ActionID    string `json:"action_id,omitempty"`
	ActionValue string `json:"action_value,omitempty"`
}

// Inbound reads a record of a verified delivery, until it is forgotten.
func (s *Service) Inbound(ctx context.Context, id string) (InboundView, error) {
	if !strings.HasPrefix(id, "asi_") || len(id) > 64 {
		return InboundView{}, fmt.Errorf("slack inbound record: %w", domain.ErrNotFound)
	}
	inbound, err := s.store.Inbound(ctx, id)
	if err != nil {
		return InboundView{}, err
	}
	view := InboundView{
		ID: inbound.ID, OrganizationID: inbound.OrganizationID, TeamID: inbound.TeamID, Kind: inbound.Kind,
		ReceivedAt: inbound.ReceivedAt, ChannelID: inbound.ChannelID, UserID: inbound.UserID, MessageTS: inbound.MessageTS,
		ThreadTS: inbound.ThreadTS, EventID: inbound.EventID, Text: inbound.Text, Mentions: inbound.Mentions,
		ChannelType: inbound.ChannelType, ExtShared: inbound.ExtShared, ActionID: inbound.ActionID, ActionValue: inbound.ActionValue,
	}
	if install, err := s.store.InstallByTeam(ctx, inbound.TeamID); err == nil {
		view.BotUserID = install.BotUserID
	}
	return view, nil
}

// ---- Profiles ----

// ProfileView is what Slack said about a person.
type ProfileView struct {
	UserID string `json:"user_id"`
	TeamID string `json:"team_id"`
	Email  string `json:"email"`
	// EmailConfirmed is Slack's is_email_confirmed: only a confirmed address
	// is one to match a person to a member by.
	EmailConfirmed bool   `json:"email_confirmed"`
	Name           string `json:"name"`
	Title          string `json:"title,omitempty"`
	// IsGuest is a guest of any kind; IsBot an app or a bot; IsDeleted a
	// person who has left; IsStranger someone from another workspace.
	IsGuest    bool      `json:"is_guest"`
	IsBot      bool      `json:"is_bot"`
	IsDeleted  bool      `json:"is_deleted"`
	IsStranger bool      `json:"is_stranger"`
	FetchedAt  time.Time `json:"fetched_at"`
}

// ProfilesView answers the people Slack knows, and the ids it does not.
type ProfilesView struct {
	Profiles []ProfileView `json:"profiles"`
	Missing  []string      `json:"missing"`
}

// Profiles answers people's profiles from the day's cache, asking Slack only
// for those it lacks (users.info).
func (s *Service) Profiles(ctx context.Context, organizationID string, userIDs []string) (ProfilesView, error) {
	install, err := s.activeInstall(ctx, organizationID)
	if err != nil {
		return ProfilesView{}, err
	}
	ids := make([]string, 0, len(userIDs))
	for _, id := range userIDs {
		switch {
		case !userIDPattern.MatchString(id):
			return ProfilesView{}, fmt.Errorf("%q is not a Slack user id: %w", truncate(id, 32), domain.ErrInvalid)
		case !slices.Contains(ids, id):
			ids = append(ids, id)
		}
	}
	if len(ids) == 0 || len(ids) > maxProfiles {
		return ProfilesView{}, fmt.Errorf("a profiles request names one to %d users: %w", maxProfiles, domain.ErrInvalid)
	}
	now := s.now().UTC()
	cached, err := s.store.Profiles(ctx, install.TeamID, ids, now.Add(-profileLifetime))
	if err != nil {
		return ProfilesView{}, err
	}
	byID := map[string]Profile{}
	for _, profile := range cached {
		byID[profile.UserID] = profile
	}
	view := ProfilesView{Profiles: make([]ProfileView, 0, len(ids)), Missing: []string{}}
	for _, id := range ids {
		profile, ok := byID[id]
		if !ok {
			var missing bool
			if profile, missing, err = s.fetchProfile(ctx, install, id, now); err != nil {
				return ProfilesView{}, err
			}
			if missing {
				view.Missing = append(view.Missing, id)
				continue
			}
		}
		view.Profiles = append(view.Profiles, ProfileView{
			UserID: profile.UserID, TeamID: profile.TeamID, Email: profile.Email, EmailConfirmed: profile.EmailConfirmed,
			Name: profile.Name, Title: profile.Title, IsGuest: profile.IsGuest, IsBot: profile.IsBot, IsDeleted: profile.IsDeleted, IsStranger: profile.IsStranger,
			FetchedAt: profile.FetchedAt,
		})
	}
	return view, nil
}

// fetchProfile asks Slack about one person and keeps the answer. A person
// Slack does not know is missing, and is not kept.
func (s *Service) fetchProfile(ctx context.Context, install Install, id string, now time.Time) (Profile, bool, error) {
	token, err := s.botToken(ctx, install)
	if err != nil {
		return Profile{}, false, err
	}
	user, err := s.api.User(ctx, token, id)
	var refused *Error
	switch {
	case errors.As(err, &refused) && (refused.Code == "user_not_found" || refused.Code == "user_not_visible"):
		return Profile{}, true, nil
	case err != nil:
		return Profile{}, false, s.slackFailed(ctx, install, err)
	}
	profile := Profile{
		TeamID: install.TeamID, UserID: id, Email: user.Email, EmailConfirmed: user.EmailConfirmed, Name: user.Name,
		Title: user.Title, IsGuest: user.Guest, IsBot: user.Bot, IsDeleted: user.Deleted,
		// A person of another workspace is a stranger to this one, however
		// Slack flags them.
		IsStranger: user.Stranger || (user.TeamID != "" && user.TeamID != install.TeamID),
		FetchedAt:  now,
	}
	if err := s.store.SaveProfile(ctx, profile); err != nil {
		return Profile{}, false, err
	}
	return profile, false, nil
}

// ---- Channels ----

// ChannelView is what Slack says a conversation is now, and whether an admin
// marked it high priority. It is read fresh every time: a channel that was
// private and unshared can be neither, and a bypass must see that.
type ChannelView struct {
	ChannelID string `json:"channel_id"`
	Name      string `json:"name,omitempty"`
	// IsPrivate is Slack's is_private, which a direct message and a group
	// message are as well: it is not enough to be private to be a channel.
	IsPrivate bool `json:"is_private"`
	// IsShared is every kind of sharing, with another organization or across
	// an Enterprise organization.
	IsShared     bool `json:"is_shared"`
	IsMPIM       bool `json:"is_mpim"`
	IsIM         bool `json:"is_im"`
	IsArchived   bool `json:"is_archived"`
	HighPriority bool `json:"high_priority"`
}

// Channel reads a conversation from Slack, now (conversations.info). Slack
// not describing it because the bot lacks the scope for its type is
// ErrChannelUnreadable; that is what a group message is to this app.
func (s *Service) Channel(ctx context.Context, organizationID, channelID string) (ChannelView, error) {
	install, err := s.activeInstall(ctx, organizationID)
	if err != nil {
		return ChannelView{}, err
	}
	if !channelIDPattern.MatchString(channelID) {
		return ChannelView{}, fmt.Errorf("%q is not a Slack conversation id: %w", truncate(channelID, 32), domain.ErrInvalid)
	}
	token, err := s.botToken(ctx, install)
	if err != nil {
		return ChannelView{}, err
	}
	conversation, err := s.api.Conversation(ctx, token, channelID)
	var refused *Error
	switch {
	case errors.As(err, &refused) && refused.Code == "channel_not_found":
		return ChannelView{}, fmt.Errorf("slack conversation: %w", domain.ErrNotFound)
	case errors.As(err, &refused) && refused.Code == "missing_scope":
		return ChannelView{}, ErrChannelUnreadable
	case err != nil:
		return ChannelView{}, s.slackFailed(ctx, install, err)
	}
	marked, err := s.store.IsHighPriority(ctx, install.OrganizationID, install.TeamID, channelID)
	if err != nil {
		return ChannelView{}, err
	}
	return ChannelView{
		ChannelID: conversation.ID, Name: conversation.Name, IsPrivate: conversation.Private, IsShared: conversation.Shared,
		IsMPIM: conversation.MPIM, IsIM: conversation.IM, IsArchived: conversation.Archived, HighPriority: marked,
	}, nil
}

// ---- Posting ----

// Target is where a post goes.
//
//   - channel_id and thread_ts: a reply in a thread everyone in the channel sees.
//   - channel_id, thread_ts and user_id: an ephemeral message in that thread,
//     which only that person sees.
//   - user_id alone: a direct message from the bot to that person.
//
// The thread, and the person, must be ones the organization's workspace has
// sent a verified message or click in within the last seven days.
type Target struct {
	ChannelID string `json:"channel_id"`
	ThreadTS  string `json:"thread_ts"`
	UserID    string `json:"user_id"`
}

func (t Target) placement() (placement, error) {
	switch {
	case t.ChannelID != "" && t.ThreadTS != "" && t.UserID == "":
		return inThread, nil
	case t.ChannelID != "" && t.ThreadTS != "" && t.UserID != "":
		return toPerson, nil
	case t.ChannelID == "" && t.ThreadTS == "" && t.UserID != "":
		return inDM, nil
	}
	return 0, fmt.Errorf("a target is a thread (channel_id and thread_ts), a person in a thread (with user_id) or a person (user_id alone): %w", domain.ErrInvalid)
}

func (t Target) valid() error {
	switch {
	case t.ChannelID != "" && !channelIDPattern.MatchString(t.ChannelID),
		t.ThreadTS != "" && !tsPattern.MatchString(t.ThreadTS),
		t.UserID != "" && !userIDPattern.MatchString(t.UserID):
		return fmt.Errorf("the target names a Slack id of a shape Slack does not use: %w", domain.ErrInvalid)
	}
	return nil
}

// PostRequest is a message for apps to post.
type PostRequest struct {
	OrganizationID string `json:"organization_id"`
	// IdempotencyKey makes the post happen once however often it is asked:
	// the same key with the same request answers the first post.
	IdempotencyKey string          `json:"idempotency_key"`
	Target         Target          `json:"target"`
	Template       string          `json:"template"`
	Params         json.RawMessage `json:"params"`
}

// PostResult is where a post went.
type PostResult struct {
	ChannelID string `json:"channel_id"`
	// TS is the message's timestamp: what an update names it by. An
	// ephemeral message's cannot be updated.
	TS        string `json:"ts"`
	Ephemeral bool   `json:"ephemeral,omitempty"`
	// Replayed says the key had been used, and this is the first post's.
	Replayed bool `json:"replayed,omitempty"`
}

// Post renders a template and posts it where Target says, once per
// idempotency key.
//
// It is refused, without a word to Slack, unless the organization has an
// active install, the template takes params of the shape given and may go
// where the target is, and the thread and person are ones a verified message
// or click has been in. An organization that posts faster than its budget is
// answered with a RateLimitedError. Slack itself rate limiting is the same,
// and either way the idempotency key is free to try again.
func (s *Service) Post(ctx context.Context, request PostRequest) (PostResult, error) {
	key, err := domain.CheckIdempotencyKey(request.IdempotencyKey)
	if err != nil {
		return PostResult{}, err
	}
	if err := request.Target.valid(); err != nil {
		return PostResult{}, err
	}
	where, err := request.Target.placement()
	if err != nil {
		return PostResult{}, err
	}
	rendered, allowed, err := s.render(request.Template, request.Params)
	if err != nil {
		return PostResult{}, err
	}
	if allowed&where == 0 {
		return PostResult{}, fmt.Errorf("the %s template cannot be posted there: %w", request.Template, domain.ErrInvalid)
	}
	install, err := s.activeInstall(ctx, request.OrganizationID)
	if err != nil {
		return PostResult{}, err
	}
	root, err := s.checkTarget(ctx, install, request.Target, where)
	if err != nil {
		return PostResult{}, err
	}

	kind := PostMessage
	if where == toPerson {
		kind = PostEphemeral
	}
	claim, mine, err := s.store.ClaimPost(ctx, Post{
		OrganizationID: install.OrganizationID, Key: key, RequestHash: requestHash(request, root), TeamID: install.TeamID,
		Template: request.Template, Kind: kind, ClaimedAt: s.now().UTC(),
	}, s.now().UTC().Add(-claimLease))
	if err != nil {
		return PostResult{}, err
	}
	if !mine {
		return s.replay(claim, request, root)
	}
	// From here the key is held, and every way out gives it back unless the
	// post went through.
	if err := s.spend(install.OrganizationID); err != nil {
		s.release(ctx, install.OrganizationID, key)
		return PostResult{}, err
	}
	token, err := s.botToken(ctx, install)
	if err != nil {
		s.release(ctx, install.OrganizationID, key)
		return PostResult{}, err
	}
	channel := request.Target.ChannelID
	if where == inDM {
		if channel, err = s.directMessage(ctx, install, token, request.Target.UserID); err != nil {
			s.release(ctx, install.OrganizationID, key)
			return PostResult{}, s.slackFailed(ctx, install, err)
		}
	}
	message := Message{Channel: channel, ThreadTS: root, Text: rendered.Text, Blocks: rendered.Blocks}
	if where == toPerson {
		message.User = request.Target.UserID
	}
	ts, err := s.api.Post(ctx, token, message)
	if err != nil {
		s.release(ctx, install.OrganizationID, key)
		return PostResult{}, s.slackFailed(ctx, install, err)
	}
	// Slack has the message. Recording it does not depend on the caller
	// still being there, since a retry would post it twice.
	done, cancel := context.WithTimeout(context.WithoutCancel(ctx), finishTimeout)
	defer cancel()
	if err := s.store.CompletePost(done, install.OrganizationID, key, channel, ts, s.now().UTC()); err != nil {
		s.logger.ErrorContext(done, "record a Slack post that was made", "organization_id", install.OrganizationID, "error", err)
	}
	return PostResult{ChannelID: channel, TS: ts, Ephemeral: where == toPerson}, nil
}

// checkTarget holds a post to conversations people opened with the bot, and
// answers the thread's root, which is where a reply goes.
func (s *Service) checkTarget(ctx context.Context, install Install, target Target, where placement) (string, error) {
	root := ""
	if where != inDM {
		found := false
		var err error
		if root, found, err = s.store.ThreadSeen(ctx, install.OrganizationID, install.TeamID, target.ChannelID, target.ThreadTS); err != nil {
			return "", err
		} else if !found {
			return "", ErrThreadUnseen
		}
	}
	if where != inThread {
		seen, err := s.store.UserSeen(ctx, install.OrganizationID, install.TeamID, target.UserID, s.now().UTC().Add(-InboundRetention))
		if err != nil {
			return "", err
		}
		if !seen {
			return "", ErrThreadUnseen
		}
	}
	return root, nil
}

// requestHash names a request's content, so a key reused for a different
// message is caught. The thread is its root, so naming it by another
// message's ts is the same request.
func requestHash(request PostRequest, root string) string {
	params := bytes.TrimSpace(request.Params)
	var compact bytes.Buffer
	if json.Compact(&compact, params) == nil {
		params = compact.Bytes()
	}
	sum := sha256.Sum256([]byte(strings.Join([]string{
		request.Template, request.Target.ChannelID, root, request.Target.UserID, string(params),
	}, "\x00")))
	return hex.EncodeToString(sum[:])
}

// replay answers a key that was used: the first post's answer for the same
// request, and a conflict for anything else, or for a post still being made.
func (s *Service) replay(held Post, request PostRequest, root string) (PostResult, error) {
	switch {
	case held.RequestHash != requestHash(request, root):
		return PostResult{}, fmt.Errorf("that idempotency key was used for a different message: %w", domain.ErrConflict)
	case held.CompletedAt == nil:
		return PostResult{}, fmt.Errorf("that message is being posted; try again shortly: %w", domain.ErrConflict)
	}
	return PostResult{ChannelID: held.ChannelID, TS: held.TS, Ephemeral: held.Kind == PostEphemeral, Replayed: true}, nil
}

// release gives a key back after a post that Slack did not take, so the
// caller can try again with it.
func (s *Service) release(ctx context.Context, organizationID, key string) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), finishTimeout)
	defer cancel()
	if err := s.store.ReleasePost(ctx, organizationID, key); err != nil {
		s.logger.ErrorContext(ctx, "release a Slack post's idempotency key", "organization_id", organizationID, "error", err)
	}
}

// directMessage opens the bot's conversation with a person, remembering it.
func (s *Service) directMessage(ctx context.Context, install Install, token secrets.Secret, userID string) (string, error) {
	if channel, ok := s.dms.get(install.TeamID, userID); ok {
		return channel, nil
	}
	channel, err := s.api.OpenDM(ctx, token, userID)
	if err != nil {
		return "", err
	}
	s.dms.put(install.TeamID, userID, channel)
	return channel, nil
}

// UpdateRequest is a message apps posted, to be replaced.
type UpdateRequest struct {
	OrganizationID string          `json:"organization_id"`
	ChannelID      string          `json:"channel_id"`
	TS             string          `json:"ts"`
	Template       string          `json:"template"`
	Params         json.RawMessage `json:"params"`
}

// Update replaces a message apps posted with another template's rendering,
// as a decided notice replaces its buttons. Only a message apps recorded as
// its own can be updated, and not an ephemeral one, which Slack cannot.
func (s *Service) Update(ctx context.Context, request UpdateRequest) (PostResult, error) {
	install, err := s.activeInstall(ctx, request.OrganizationID)
	if err != nil {
		return PostResult{}, err
	}
	if !channelIDPattern.MatchString(request.ChannelID) || !tsPattern.MatchString(request.TS) {
		return PostResult{}, fmt.Errorf("the message is named by a Slack channel id and timestamp: %w", domain.ErrInvalid)
	}
	post, err := s.store.PostAt(ctx, install.OrganizationID, request.ChannelID, request.TS)
	switch {
	case errors.Is(err, domain.ErrNotFound) || (err == nil && post.TeamID != install.TeamID):
		return PostResult{}, fmt.Errorf("apps did not post that message: %w", domain.ErrNotFound)
	case err != nil:
		return PostResult{}, err
	case post.Kind == PostEphemeral:
		return PostResult{}, fmt.Errorf("an ephemeral message cannot be updated: %w", domain.ErrInvalid)
	}
	rendered, allowed, err := s.render(request.Template, request.Params)
	if err != nil {
		return PostResult{}, err
	}
	where := inThread
	if strings.HasPrefix(request.ChannelID, "D") {
		where = inDM
	}
	if allowed&where == 0 {
		return PostResult{}, fmt.Errorf("the %s template cannot replace a message there: %w", request.Template, domain.ErrInvalid)
	}
	if err := s.spend(install.OrganizationID); err != nil {
		return PostResult{}, err
	}
	token, err := s.botToken(ctx, install)
	if err != nil {
		return PostResult{}, err
	}
	if err := s.api.Update(ctx, token, request.ChannelID, request.TS, rendered.Text, rendered.Blocks); err != nil {
		return PostResult{}, s.slackFailed(ctx, install, err)
	}
	return PostResult{ChannelID: request.ChannelID, TS: request.TS}, nil
}
