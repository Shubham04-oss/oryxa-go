// Package slackbot is Oryxa's Slack surface: one Slack app, installed by a
// workspace for one organization, through which people ask Oryxa things by
// mentioning it and answer its questions by clicking its buttons.
//
// Slack carries three things and nothing else: the entry (a mention), the
// owner's notice (a message with three buttons) and the final answer. All the
// deciding happens elsewhere. This package is the gateway between Slack and
// the rest of Oryxa, and keeps to four jobs:
//
//   - Ingress. Slack's events and clicks arrive at two static URLs, are
//     verified by hooks with the v0 scheme, and come here as a hooks.Surface.
//     Each becomes an inbound record, a bus event that carries its id and
//     nothing else, and its dedupe mark, in one transaction, and Slack is
//     answered. Nothing on that path calls Slack, a model or another service.
//   - Install. An organization's admin sends the workspace's admin through
//     Slack's OAuth v2 install. The bot token it yields is sealed with the
//     apps vault key and kept under the workspace's team id.
//   - The internal API. Services with the purpose slack read the inbound
//     records, ask for people's profiles and channels' privacy, and have
//     apps post messages and update them. Only apps knows the bot token.
//   - Templates. Every message apps posts is rendered here, from typed
//     parameters, with link unfurling off: what a caller supplies is text to
//     be escaped, never Block Kit, so nothing but a template's own structure
//     ever reaches Slack, and a credential is never rendered.
//
// It is switched on by APPS_SLACK_SURFACE, and is no integration: nothing
// here is in the registry that members' own Slack sign-in belongs to, and the
// two Slack apps share nothing.
//
// Confirmed against Slack's documentation on 2026-09-29:
//   - https://docs.slack.dev/reference/events/app_mention,
//     https://docs.slack.dev/reference/events/app_uninstalled and
//     https://docs.slack.dev/reference/events/tokens_revoked: the envelope's
//     team_id and event_id; the tokens object naming, in oauth and bot,
//     user ids; that app_uninstalled and tokens_revoked may arrive in either
//     order.
//   - https://docs.slack.dev/reference/interaction-payloads/block_actions-payload:
//     user.id, container.{channel_id,message_ts}, team.id and
//     actions[].{action_id,value}.
//   - https://docs.slack.dev/reference/methods/oauth.v2.access: the code
//     exchange, its credentials preferably in HTTP Basic, bot_user_id,
//     team.{id,name}, and refresh_token with expires_in only when token
//     rotation is on.
//   - https://docs.slack.dev/reference/methods/conversations.info: is_private,
//     is_shared, is_ext_shared, is_org_shared, is_pending_ext_shared, is_im and
//     is_mpim; a conversation's type needs its own read scope, so a group
//     DM needs mpim:read, which the app does not have.
//   - https://docs.slack.dev/reference/methods/users.info: is_bot,
//     is_restricted, is_ultra_restricted, is_stranger and deleted; the email
//     needs users:read.email.
//   - https://docs.slack.dev/reference/methods/chat.postMessage,
//     chat.postEphemeral, chat.update and conversations.open: JSON with the
//     bearer token; an ephemeral message cannot be updated; an update that
//     sends no blocks removes them; 429 answers with Retry-After.
//   - https://docs.slack.dev/reference/app-manifest: every key of manifest.yaml.
//
// Not confirmed, and to be checked against a real workspace: whether
// app_uninstalled and tokens_revoked are subscribed under bot_events, whether
// app_mention reaches the bot in a group DM and carries channel_type or
// is_ext_shared_channel, whether Slack's app-approval flow keeps the install
// link's state, and apps.uninstall's arguments.
package slackbot

import (
	"context"
	"crypto/rand"
	"encoding/base32"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"time"

	appsv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/apps/v1"

	"github.com/Shubham04-oss/oryxa-go/apps/internal/authclient"
	"github.com/Shubham04-oss/oryxa-go/apps/internal/domain"
	"github.com/Shubham04-oss/oryxa-go/apps/internal/hooks"
	"github.com/Shubham04-oss/oryxa-go/internal/secrets"
)

const (
	// Toolkit is the surface's name to hooks: the toolkit of its deployment
	// endpoint. It is no registry integration's slug, and no member can
	// connect it.
	Toolkit = "slackbot"
	// KindEvents and KindInteractivity are the two kinds of delivery Slack
	// sends, told apart by the URL they arrive at.
	KindEvents        = "events"
	KindInteractivity = "interactivity"

	// EventsPath and InteractivityPath are the static URLs typed into the
	// Slack app's settings, under APPS_PUBLIC_BASE_URL; InstallCallbackPath
	// is its OAuth redirect.
	EventsPath          = "/v1/apps/slack/events"
	InteractivityPath   = "/v1/apps/slack/interactivity"
	InstallCallbackPath = "/v1/apps/slack/install/callback"

	// Purpose is the service-token purpose the internal API takes.
	Purpose = "slack"

	// SealResource is what a bot token is sealed for, with the workspace's
	// team id, so it cannot be moved to another workspace.
	SealResource = "slack_install"

	// InboundRetention is how long a record of what people typed and clicked
	// is kept.
	InboundRetention = 7 * 24 * time.Hour
)

// BotScopes are the bot scopes the app asks for. manifest.yaml lists the same
// ones, each with the reason it is there, and a test holds the two together.
var BotScopes = []string{
	"app_mentions:read", "chat:write", "im:write", "users:read", "users:read.email", "channels:read", "groups:read",
}

// The kinds of inbound record.
const (
	// InboundMessage is an @-mention of the bot.
	InboundMessage = "message"
	// InboundAction is a click on a button of one of the bot's messages.
	InboundAction = "action"
)

var (
	// ErrTeamBound means the workspace's install is active for another
	// organization.
	ErrTeamBound = fmt.Errorf("this Slack workspace is already installed for another organization: %w", domain.ErrConflict)
	// ErrOrganizationBound means the organization already has another
	// workspace installed, and has one: v1 serves one workspace per
	// organization.
	ErrOrganizationBound = fmt.Errorf("this organization already has another Slack workspace installed: %w", domain.ErrConflict)
)

// Install is a workspace's install of the app, and which organization it is
// for.
type Install struct {
	// TeamID is the Slack workspace, and the install's key.
	TeamID         string
	OrganizationID string
	// Status is appsv1.SlackInstallActive, or SlackInstallEnded once it
	// stopped working; EndReason says why.
	Status    string
	EndReason string
	TeamName  string
	// BotUserID is the bot's own user, which is what a mention names.
	BotUserID string
	AppID     string
	Scopes    []string
	// Sealed is the bot token, sealed and bound to the team; nil once the
	// install has ended.
	Sealed *secrets.SealedData
	// InstalledBy is the member who ran the install.
	InstalledBy string
	InstalledAt time.Time
	UpdatedAt   time.Time
	EndedAt     *time.Time
}

// Active says the bot can post through the install.
func (install Install) Active() bool { return install.Status == appsv1.SlackInstallActive }

// InstallFlow is an install in progress: an admin has been given the Add to
// Slack link and has not come back. Only the state's hash is kept.
type InstallFlow struct {
	StateHash      string
	OrganizationID string
	MemberID       string
	// ReturnTo is a page the browser goes back to, or "".
	ReturnTo   string
	CreatedAt  time.Time
	ExpiresAt  time.Time
	ConsumedAt *time.Time
}

// Inbound is what a verified delivery became: a message that mentioned the
// bot, or a click, kept for services that act on it to read.
type Inbound struct {
	ID             string
	OrganizationID string
	TeamID         string
	// Kind is InboundMessage or InboundAction.
	Kind string
	// EventID is Slack's id for a message's event; empty for a click.
	EventID string
	// ChannelID is where it happened, and UserID who did it.
	ChannelID string
	UserID    string
	// MessageTS is the message's own timestamp (for a click, the message
	// clicked), and ThreadTS the thread's root: the message's own ts when it
	// started one.
	MessageTS string
	ThreadTS  string
	// ChannelType is Slack's channel_type, when its event carries one.
	ChannelType string
	// ExtShared says Slack flagged the delivery as from a channel shared
	// with another organization.
	ExtShared bool
	// Mentions are the users the message @-mentions, the bot itself left out.
	Mentions []string
	// Text is what was typed, and ActionID and ActionValue what was clicked:
	// a button's action and its opaque ticket.
	Text        string
	ActionID    string
	ActionValue string
	ReceivedAt  time.Time
}

// Post is a message apps posted, or is about to. The key makes the post
// idempotent, and the request's hash catches a key reused for something
// else.
type Post struct {
	OrganizationID string
	Key            string
	RequestHash    string
	TeamID         string
	Template       string
	// Kind is PostMessage or PostEphemeral.
	Kind      string
	ChannelID string
	TS        string
	ClaimedAt time.Time
	// CompletedAt is set once Slack accepted it.
	CompletedAt *time.Time
}

// The kinds of post.
const (
	PostMessage   = "message"
	PostEphemeral = "ephemeral"
)

// Profile is what Slack said about a person, kept for a day.
type Profile struct {
	TeamID string
	UserID string
	Email  string
	// EmailConfirmed says Slack has the address confirmed.
	EmailConfirmed bool
	Name           string
	Title          string
	IsGuest        bool
	IsBot          bool
	IsDeleted      bool
	// IsStranger is a person from another workspace, such as in a Slack
	// Connect channel.
	IsStranger bool
	FetchedAt  time.Time
}

// MarkedChannel is a channel an organization's admin marked high priority.
type MarkedChannel struct {
	ChannelID string
	Name      string
	MarkedBy  string
	MarkedAt  time.Time
}

// Cutoffs say how old each kind of record may be: what is older is purged.
type Cutoffs struct {
	Inbound, Flows, Posts, Profiles time.Time
}

// Purged is what maintenance forgot.
type Purged struct {
	Inbound, Flows, Posts, Profiles int64
}

// Store is what the surface keeps: postgres.Store is one. A missing row is
// domain.ErrNotFound.
type Store interface {
	// InstallByTeam reads a workspace's install, active or ended.
	InstallByTeam(ctx context.Context, teamID string) (Install, error)
	// InstallForOrganization reads an organization's active install, or when
	// it has none the one that ended last.
	InstallForOrganization(ctx context.Context, organizationID string) (Install, error)
	// SaveInstall makes install the workspace's active install, replacing
	// whatever was there, and writes the event event names in the same
	// transaction. reinstalled tells it whether an active install was
	// replaced. It is ErrTeamBound when the workspace's active install is
	// another organization's, and ErrOrganizationBound when the organization
	// has another workspace active; nothing is written then.
	SaveInstall(ctx context.Context, install Install, event func(reinstalled bool) (domain.Event, error)) error
	// EndInstall ends a workspace's active install, discarding its token, and
	// writes event in the same transaction. It answers false, writing
	// nothing, when there was no active install.
	EndInstall(ctx context.Context, teamID, reason string, now time.Time, event func(Install) (domain.Event, error)) (Install, bool, error)

	// InsertInstallFlow keeps an install in progress; ConsumeInstallFlow
	// spends it, once, while it is unexpired.
	InsertInstallFlow(ctx context.Context, flow InstallFlow) error
	ConsumeInstallFlow(ctx context.Context, stateHash string, now time.Time) (InstallFlow, error)

	// RecordInbound writes a verified delivery in one transaction: its dedupe
	// mark under mark, the inbound record and the event that names it. It
	// answers false, writing nothing, when the mark was already there.
	RecordInbound(ctx context.Context, mark string, at time.Time, inbound Inbound, event domain.Event) (bool, error)
	// RecordInstallEnd is the same for a delivery that ends an install: the
	// mark, the install ended and its event. It answers false when the mark
	// was already there, and also when there was no active install to end,
	// the mark being written either way.
	RecordInstallEnd(ctx context.Context, mark string, at time.Time, teamID, reason string, event func(Install) (domain.Event, error)) (bool, error)
	Inbound(ctx context.Context, id string) (Inbound, error)
	// ThreadSeen finds a thread the organization's workspace has sent a
	// verified message or click in, by the thread's root or by any message
	// timestamp in it, and answers its root.
	ThreadSeen(ctx context.Context, organizationID, teamID, channelID, ts string) (string, bool, error)
	// UserSeen says a person is the author or a named person of a verified
	// inbound record received since.
	UserSeen(ctx context.Context, organizationID, teamID, userID string, since time.Time) (bool, error)

	// ClaimPost records that a post is being made. It answers the post that
	// holds the key and false when someone else has it, unless that claim is
	// older than staleBefore and never completed, which is taken over.
	ClaimPost(ctx context.Context, post Post, staleBefore time.Time) (Post, bool, error)
	CompletePost(ctx context.Context, organizationID, key, channelID, ts string, at time.Time) error
	// ReleasePost forgets a claim whose post Slack refused, so a retry can
	// make it.
	ReleasePost(ctx context.Context, organizationID, key string) error
	// PostAt finds a completed post by where it went.
	PostAt(ctx context.Context, organizationID, channelID, ts string) (Post, error)

	Profiles(ctx context.Context, teamID string, userIDs []string, fetchedAfter time.Time) ([]Profile, error)
	SaveProfile(ctx context.Context, profile Profile) error
	// HighPriorityChannels lists the channels marked; ReplaceHighPriority
	// makes exactly channels the marked ones, recording who and when.
	HighPriorityChannels(ctx context.Context, organizationID, teamID string) ([]MarkedChannel, error)
	IsHighPriority(ctx context.Context, organizationID, teamID, channelID string) (bool, error)
	ReplaceHighPriority(ctx context.Context, organizationID, teamID, actor string, channels []MarkedChannel, now time.Time) error

	// Purge forgets what is older than the cutoffs.
	Purge(ctx context.Context, cutoffs Cutoffs) (Purged, error)
}

// Sealer seals and opens the bot token. internal/secrets.Envelope, bound to
// the service oryxa-apps, is one.
type Sealer interface {
	Seal(ctx context.Context, binding secrets.Binding, plaintext []byte) (secrets.SealedData, error)
	Open(ctx context.Context, binding secrets.Binding, sealed secrets.SealedData) ([]byte, error)
}

// Directory is auth, which says who a caller is in an organization.
type Directory interface {
	Membership(ctx context.Context, organizationID string, credentials authclient.Credentials) (domain.OrgMembership, error)
}

// App is Oryxa's one Slack app, as its settings page gives it.
type App struct {
	ClientID      string
	ClientSecret  secrets.Secret
	SigningSecret secrets.Secret
}

// Config is what the surface is told about the deployment.
type Config struct {
	// PublicBaseURL is where Slack and the browser reach this process.
	PublicBaseURL string
	App           App
	// APIBaseURL is Slack's Web API; "" is the real Slack. Anything else
	// must be loopback, for a fake.
	APIBaseURL string
	// ReturnAllowed says which pages an install may send the admin's browser
	// back to. Without it none is allowed.
	ReturnAllowed func(string) bool
	// WebOrigins are the origins of Oryxa's web app: a message may carry a
	// link to a page on one of them, and to nothing else.
	WebOrigins []string
}

// Dependencies are what the surface borrows from apps.
type Dependencies struct {
	Store     Store
	Sealer    Sealer
	Directory Directory
	// HTTP calls Slack. Without one the Web API client builds its own, for
	// the real Slack over the public internet, or for a loopback fake.
	HTTP   Doer
	Logger *slog.Logger
	Clock  func() time.Time
	// Random makes ids and states; crypto/rand without one.
	Random io.Reader
}

// Service is the Slack surface.
type Service struct {
	store     Store
	sealer    Sealer
	directory Directory
	api       *Client
	logger    *slog.Logger
	now       func() time.Time
	random    io.Reader

	config  Config
	limiter *limiter
	tokens  tokenCache
	dms     dmCache
}

// New assembles the surface. It checks its configuration and touches
// nothing.
func New(dependencies Dependencies, config Config) (*Service, error) {
	switch {
	case dependencies.Store == nil:
		return nil, errors.New("slackbot needs a store")
	case dependencies.Sealer == nil:
		return nil, errors.New("slackbot needs a sealer for bot tokens")
	case dependencies.Directory == nil:
		return nil, errors.New("slackbot needs auth to say who an admin is")
	case strings.TrimSpace(config.App.ClientID) == "" || config.App.ClientSecret.Reveal() == "" || config.App.SigningSecret.Reveal() == "":
		return nil, errors.New("slackbot needs the Slack app's client id, client secret and signing secret")
	}
	base := strings.TrimRight(strings.TrimSpace(config.PublicBaseURL), "/")
	if base == "" {
		return nil, errors.New("slackbot needs the public base URL")
	}
	config.PublicBaseURL = base
	api, err := NewClient(config.APIBaseURL, dependencies.HTTP)
	if err != nil {
		return nil, err
	}
	service := &Service{
		store: dependencies.Store, sealer: dependencies.Sealer, directory: dependencies.Directory, api: api,
		logger: dependencies.Logger, now: dependencies.Clock, random: dependencies.Random, config: config,
	}
	if service.logger == nil {
		service.logger = slog.New(slog.DiscardHandler)
	}
	if service.now == nil {
		service.now = time.Now
	}
	if service.random == nil {
		service.random = rand.Reader
	}
	service.limiter = newLimiter(postsPerSecond, postBurst, func() time.Time { return service.now() })
	return service, nil
}

// The surface is what hooks serves at its aliases: a Surface, found by its
// toolkit.
var (
	_ hooks.Surface  = (*Service)(nil)
	_ hooks.Surfaces = (*Service)(nil)
)

// Surface is how hooks finds the surface: it is the one toolkit's.
func (s *Service) Surface(toolkit string) (hooks.Surface, bool) {
	if toolkit == Toolkit {
		return s, true
	}
	return nil, false
}

var idEncoding = base32.StdEncoding.WithPadding(base32.NoPadding)

// newID is prefix and 120 random bits, as apps' own ids are.
func newID(random io.Reader, prefix string) (string, error) {
	value := make([]byte, 15)
	if _, err := io.ReadFull(random, value); err != nil {
		return "", fmt.Errorf("generate an id: %w", err)
	}
	return prefix + strings.ToLower(idEncoding.EncodeToString(value)), nil
}

// event packages what happened for the outbox, in the envelope apps uses.
func (s *Service) event(detailType string, data any, at time.Time) (domain.Event, error) {
	id, err := newID(s.random, "evt_")
	if err != nil {
		return domain.Event{}, err
	}
	encoded, err := json.Marshal(data)
	if err != nil {
		return domain.Event{}, fmt.Errorf("encode %s: %w", detailType, err)
	}
	return domain.Event{ID: id, DetailType: detailType, OccurredAt: at, Data: encoded}, nil
}
