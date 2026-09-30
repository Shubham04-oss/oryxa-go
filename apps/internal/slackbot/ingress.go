package slackbot

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	appsv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/apps/v1"

	"github.com/Shubham04-oss/oryxa-go/apps/internal/domain"
	"github.com/Shubham04-oss/oryxa-go/apps/internal/integration"
)

// Slack ids and timestamps, as far as the surface trusts them: a delivery is
// signed, so these are Slack's, and the checks only keep something odd from
// reaching a query or a call.
var (
	userIDPattern    = regexp.MustCompile(`^[UW][A-Z0-9]{2,31}$`)
	channelIDPattern = regexp.MustCompile(`^[CGD][A-Z0-9]{2,31}$`)
	teamIDPattern    = regexp.MustCompile(`^[TE][A-Z0-9]{2,31}$`)
	tsPattern        = regexp.MustCompile(`^[0-9]{9,11}\.[0-9]{6}$`)
	// mentionPattern finds a user mention in a message's text: <@U123> or
	// <@U123|name>.
	mentionPattern = regexp.MustCompile(`<@([UW][A-Z0-9]{2,31})(?:\|[^>]*)?>`)
)

const (
	maxTextRunes     = 40_000
	maxActionIDBytes = 255
	// maxActionValueBytes is Slack's own limit on a button's value.
	maxActionValueBytes = 2000
	maxMentions         = 50
)

// Webhook is how Slack's deliveries are verified: the v0 scheme, which is
// the same one Slack signs every app's requests with, over the raw body and
// the timestamp header, within five minutes. It is a deployment endpoint's,
// with one secret for every workspace, held in configuration. A test holds it
// equal to the scheme the members' own Slack integration declares.
func (s *Service) Webhook() integration.Webhook {
	return integration.Webhook{
		Scope: integration.HookScopeDeployment,
		Verify: integration.Verify{
			Type:      integration.VerifyHMACSHA256,
			Signature: &integration.Locator{Header: "X-Slack-Signature", Prefix: "v0="},
			Encoding:  integration.EncodingHex,
			Signed:    "v0:{timestamp}:{body}",
			Timestamp: &integration.Locator{Header: "X-Slack-Request-Timestamp", Unit: integration.UnitSeconds},
			Tolerance: 300,
		},
		DeliveryID: integration.Locator{JSON: "/event_id"},
		Handshake:  integration.HandshakeSlackURLVerification,
	}
}

// Deliver takes a verified delivery. It reads and writes the database and
// nothing else, so hooks can answer Slack inside its three seconds. What it
// cannot use it drops: Slack retries on an error, and a delivery that will
// never be usable would only be sent again.
func (s *Service) Deliver(ctx context.Context, kind, key string, _ http.Header, body []byte, receivedAt time.Time) error {
	switch kind {
	case KindEvents:
		return s.deliverEvent(ctx, key, body, receivedAt.UTC())
	case KindInteractivity:
		return s.deliverInteraction(ctx, key, body, receivedAt.UTC())
	}
	return fmt.Errorf("slackbot: no such kind of delivery: %q", kind)
}

// eventEnvelope is the Events API wrapper: what the delivery is, and which
// workspace it is about.
type eventEnvelope struct {
	Type          string `json:"type"`
	TeamID        string `json:"team_id"`
	ContextTeamID string `json:"context_team_id"`
	EventID       string `json:"event_id"`
	// IsExtSharedChannel is set on a delivery from a channel shared with
	// another organization.
	IsExtSharedChannel bool `json:"is_ext_shared_channel"`
	Authorizations     []struct {
		TeamID string `json:"team_id"`
	} `json:"authorizations"`
	Event json.RawMessage `json:"event"`
}

type eventBody struct {
	Type        string `json:"type"`
	User        string `json:"user"`
	Text        string `json:"text"`
	TS          string `json:"ts"`
	Channel     string `json:"channel"`
	ChannelType string `json:"channel_type"`
	ThreadTS    string `json:"thread_ts"`
	// Tokens is what a tokens_revoked event names: the users whose tokens
	// were revoked, user tokens under oauth and bots' under bot.
	Tokens struct {
		OAuth []string `json:"oauth"`
		Bot   []string `json:"bot"`
	} `json:"tokens"`
}

// candidateTeams are the workspaces a delivery may be for, in the order to
// ask: the one it says it happened in, the one Slack says to process it under
// (they differ in a shared channel), and the ones its authorizations name.
func (e eventEnvelope) candidateTeams() []string {
	var teams []string
	add := func(id string) {
		if teamIDPattern.MatchString(id) && !slices.Contains(teams, id) {
			teams = append(teams, id)
		}
	}
	add(e.TeamID)
	add(e.ContextTeamID)
	for _, authorization := range e.Authorizations {
		add(authorization.TeamID)
	}
	return teams
}

func (s *Service) deliverEvent(ctx context.Context, key string, body []byte, at time.Time) error {
	var envelope eventEnvelope
	if err := json.Unmarshal(body, &envelope); err != nil || envelope.Type != "event_callback" {
		s.logger.DebugContext(ctx, "a Slack delivery that is no event was dropped", "type", truncate(envelope.Type, 32))
		return nil
	}
	var event eventBody
	if err := json.Unmarshal(envelope.Event, &event); err != nil {
		s.logger.DebugContext(ctx, "a Slack event that could not be read was dropped")
		return nil
	}
	switch event.Type {
	case "app_mention", "app_uninstalled", "tokens_revoked":
	default:
		// Everything else is acknowledged and forgotten: the surface asked
		// Slack for these three, and takes nothing it did not ask for.
		s.logger.DebugContext(ctx, "a Slack event of a kind the surface does not take was dropped", "type", truncate(event.Type, 64))
		return nil
	}
	install, found, err := s.installFor(ctx, envelope.candidateTeams())
	if err != nil {
		return err
	}
	if !found {
		s.logger.InfoContext(ctx, "a Slack event for a workspace with no active install was dropped", "type", event.Type)
		return nil
	}
	switch event.Type {
	case "app_uninstalled":
		return s.installEnded(ctx, key, install, appsv1.SlackAppUninstalled, at)
	case "tokens_revoked":
		// A revoked user token is no concern of the bot's, which holds none.
		if !slices.Contains(event.Tokens.Bot, install.BotUserID) {
			s.logger.DebugContext(ctx, "a Slack token revocation that is not the bot's was dropped", "team_id", install.TeamID)
			return nil
		}
		return s.installEnded(ctx, key, install, appsv1.SlackTokensRevoked, at)
	}
	return s.mention(ctx, key, install, envelope, event, at)
}

// installFor finds the active install a delivery is for among the workspaces
// it may be about. No install, or one that ended, is not an error: an event
// for a workspace Oryxa does not serve is dropped.
func (s *Service) installFor(ctx context.Context, teams []string) (Install, bool, error) {
	for _, team := range teams {
		install, err := s.store.InstallByTeam(ctx, team)
		switch {
		case errors.Is(err, domain.ErrNotFound):
			continue
		case err != nil:
			return Install{}, false, err
		case install.Active():
			return install, true, nil
		}
	}
	return Install{}, false, nil
}

// mention records an @-mention of the bot.
func (s *Service) mention(ctx context.Context, key string, install Install, envelope eventEnvelope, event eventBody, at time.Time) error {
	switch {
	case !channelIDPattern.MatchString(event.Channel), !userIDPattern.MatchString(event.User), !tsPattern.MatchString(event.TS),
		event.ThreadTS != "" && !tsPattern.MatchString(event.ThreadTS):
		s.logger.WarnContext(ctx, "a Slack mention with ids of a shape the surface does not know was dropped", "team_id", install.TeamID)
		return nil
	case event.User == install.BotUserID:
		// The bot mentioning itself must not make it answer itself.
		return nil
	}
	id, err := newID(s.random, "asi_")
	if err != nil {
		return err
	}
	inbound := Inbound{
		ID: id, OrganizationID: install.OrganizationID, TeamID: install.TeamID, Kind: InboundMessage,
		EventID: truncate(envelope.EventID, 64), ChannelID: event.Channel, UserID: event.User,
		MessageTS: event.TS, ThreadTS: firstNonEmpty(event.ThreadTS, event.TS),
		ChannelType: truncate(event.ChannelType, 16), ExtShared: envelope.IsExtSharedChannel,
		Mentions: mentionedUsers(event.Text, install.BotUserID), Text: cleanText(event.Text, maxTextRunes), ReceivedAt: at,
	}
	notice, err := s.event(appsv1.SlackMessageReceivedDetailType, appsv1.SlackMessageReceived{
		InboundID: id, OrganizationID: install.OrganizationID, TeamID: install.TeamID, ReceivedAt: at,
	}, at)
	if err != nil {
		return err
	}
	recorded, err := s.store.RecordInbound(ctx, key, at, inbound, notice)
	if err != nil {
		return err
	}
	if recorded {
		s.logger.InfoContext(ctx, "slack mention recorded", "inbound_id", id, "organization_id", install.OrganizationID, "team_id", install.TeamID)
	}
	return nil
}

// installEnded ends an install because Slack said it is over.
func (s *Service) installEnded(ctx context.Context, key string, install Install, reason string, at time.Time) error {
	recorded, err := s.store.RecordInstallEnd(ctx, key, at, install.TeamID, reason, func(ended Install) (domain.Event, error) {
		return s.event(appsv1.SlackInstallChangedDetailType, appsv1.SlackInstallChanged{
			OrganizationID: ended.OrganizationID, TeamID: ended.TeamID, State: appsv1.SlackInstallEnded, Reason: reason, ChangedAt: at,
		}, at)
	})
	if err != nil {
		return err
	}
	s.tokens.forget(install.TeamID)
	if recorded {
		s.logger.InfoContext(ctx, "slack install ended", "organization_id", install.OrganizationID, "team_id", install.TeamID, "reason", reason)
	}
	return nil
}

// interaction is the JSON of a click, which arrives in the payload field of a
// form.
type interaction struct {
	Type string `json:"type"`
	User struct {
		ID     string `json:"id"`
		TeamID string `json:"team_id"`
	} `json:"user"`
	Team struct {
		ID string `json:"id"`
	} `json:"team"`
	Container struct {
		ChannelID string `json:"channel_id"`
		MessageTS string `json:"message_ts"`
	} `json:"container"`
	Channel struct {
		ID string `json:"id"`
	} `json:"channel"`
	Message struct {
		TS string `json:"ts"`
	} `json:"message"`
	Actions []struct {
		ActionID string `json:"action_id"`
		Value    string `json:"value"`
		Type     string `json:"type"`
	} `json:"actions"`
}

func (s *Service) deliverInteraction(ctx context.Context, key string, body []byte, at time.Time) error {
	form, err := url.ParseQuery(string(body))
	if err != nil || form.Get("payload") == "" {
		s.logger.DebugContext(ctx, "a Slack interaction with no payload was dropped")
		return nil
	}
	var click interaction
	if err := json.Unmarshal([]byte(form.Get("payload")), &click); err != nil {
		s.logger.DebugContext(ctx, "a Slack interaction that could not be read was dropped")
		return nil
	}
	if click.Type != "block_actions" || len(click.Actions) == 0 || click.Actions[0].Type != "button" {
		// Modals, shortcuts and menus are not the surface's: it has buttons.
		s.logger.DebugContext(ctx, "a Slack interaction of a kind the surface does not take was dropped", "type", truncate(click.Type, 32))
		return nil
	}
	install, found, err := s.installFor(ctx, teamsOfClick(click))
	if err != nil {
		return err
	}
	if !found {
		s.logger.InfoContext(ctx, "a Slack click for a workspace with no active install was dropped")
		return nil
	}
	action := click.Actions[0]
	channel := firstNonEmpty(click.Container.ChannelID, click.Channel.ID)
	ts := firstNonEmpty(click.Container.MessageTS, click.Message.TS)
	switch {
	case !channelIDPattern.MatchString(channel), !userIDPattern.MatchString(click.User.ID), !tsPattern.MatchString(ts),
		action.ActionID == "" || len(action.ActionID) > maxActionIDBytes, len(action.Value) > maxActionValueBytes:
		s.logger.WarnContext(ctx, "a Slack click of a shape the surface does not know was dropped", "team_id", install.TeamID)
		return nil
	}
	id, err := newID(s.random, "asi_")
	if err != nil {
		return err
	}
	// A click has no thread of its own: the message it was on is its thread.
	inbound := Inbound{
		ID: id, OrganizationID: install.OrganizationID, TeamID: install.TeamID, Kind: InboundAction,
		ChannelID: channel, UserID: click.User.ID, MessageTS: ts, ThreadTS: ts,
		ActionID: cleanText(action.ActionID, maxActionIDBytes), ActionValue: cleanText(action.Value, maxActionValueBytes), ReceivedAt: at,
	}
	notice, err := s.event(appsv1.SlackActionReceivedDetailType, appsv1.SlackActionReceived{
		InboundID: id, OrganizationID: install.OrganizationID, TeamID: install.TeamID, ReceivedAt: at,
	}, at)
	if err != nil {
		return err
	}
	recorded, err := s.store.RecordInbound(ctx, key, at, inbound, notice)
	if err != nil {
		return err
	}
	if recorded {
		// Not the button's value: it is a ticket.
		s.logger.InfoContext(ctx, "slack click recorded", "inbound_id", id, "organization_id", install.OrganizationID, "team_id", install.TeamID)
	}
	return nil
}

func teamsOfClick(click interaction) []string {
	var teams []string
	for _, id := range []string{click.Team.ID, click.User.TeamID} {
		if teamIDPattern.MatchString(id) && !slices.Contains(teams, id) {
			teams = append(teams, id)
		}
	}
	return teams
}

// mentionedUsers lists the people a message names, in order and once each,
// leaving the bot out: it is named by every mention that reaches it.
func mentionedUsers(text, botUserID string) []string {
	users := []string{}
	for _, match := range mentionPattern.FindAllStringSubmatch(text, -1) {
		if id := match[1]; id != botUserID && !slices.Contains(users, id) {
			users = append(users, id)
			if len(users) == maxMentions {
				break
			}
		}
	}
	return users
}

// cleanText makes what a person typed fit the database: valid UTF-8, no NUL,
// and no more than limit characters.
func cleanText(text string, limit int) string {
	text = strings.ToValidUTF8(text, "")
	text = strings.ReplaceAll(text, "\x00", "")
	if utf8.RuneCountInString(text) > limit {
		text = string([]rune(text)[:limit])
	}
	return text
}
