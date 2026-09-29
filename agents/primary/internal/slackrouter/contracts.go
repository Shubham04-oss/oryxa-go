package slackrouter

import (
	"context"
	"errors"
	"time"

	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/slackrouter/consentpolicy"
)

// This file is everything the router asks of the rest of Oryxa. Each
// interface is defined here, from the router's side, and satisfied by an
// adapter the service wires: apps' internal Slack API, the consent core in
// interactions, and auth's tables. None of them is imported, so the router
// is built and tested against fakes.
//
// An adapter reports a failure one of three ways, and the router treats each
// differently:
//
//   - ErrGone, ErrUnreadable: what was asked for is not there, or is a kind of
//     thing Slack will not describe. Each has its own meaning below; neither
//     is retried.
//   - Permanent(err): the call was understood and refused, and the same call
//     would be refused again: a forged ticket, a request already decided, a
//     thread apps will not post into, an install that has ended. It is not
//     retried.
//   - anything else: the call did not complete. It is retried with a delay,
//     a few times. An error that has a RetryAfter() time.Duration method
//     lengthens the delay, so a 429's Retry-After is honoured.

// Inbound kinds: which bus event a job came from.
const (
	// KindMessage is a message that mentioned the bot.
	KindMessage = "message"
	// KindAction is a click on one of the bot's buttons.
	KindAction = "action"
)

// SurfaceSlack is what Consent is told a click came from.
const SurfaceSlack = "slack"

// Slack template names, rendered only in apps. The router posts these two;
// the owner's notice, the settled notice, the answer, the status and the
// private-answer and bypass notes are the surfaces service's.
const (
	TemplateRefusal = "refusal"
	TemplateAskAck  = "ask_ack"
)

var (
	// ErrGone is what an adapter answers for a thing that no longer exists: an
	// inbound row apps purged, or a channel Slack does not know.
	ErrGone = errors.New("slackrouter: that no longer exists")
	// ErrUnreadable is what Slack answers for a channel it would not describe
	// to the bot because the bot has no scope for that kind of conversation.
	// For this app that is a group message, and it is refused as one.
	ErrUnreadable = errors.New("slackrouter: Slack will not describe that conversation to the bot")
	// ErrNotMember is what Members answers for an address no member of the
	// organization uses.
	ErrNotMember = errors.New("slackrouter: no member of the organization has that address")
	// ErrNoIdentity is what SlackUserFor answers for a member the router has
	// never matched to a Slack user.
	ErrNoIdentity = errors.New("slackrouter: that member is not matched to a Slack user")
)

// Inbound is one verified Slack delivery, as apps kept it. apps has already
// checked its signature and mapped its workspace to an organization; the
// router reads it by the id the bus event carried and believes nothing else
// about where it came from.
type Inbound struct {
	ID             string
	OrganizationID string
	TeamID         string
	// Kind is KindMessage or KindAction, and says which of the two below is
	// set.
	Kind       string
	ReceivedAt time.Time
	Mention    *Mention
	Click      *Click
}

// Mention is a message that mentioned the bot.
type Mention struct {
	// EventType is the Slack event, "app_mention" for everything v1 handles.
	// Left empty, it is taken to be one. Anything else is acknowledged and
	// dropped.
	EventType string
	ChannelID string
	// ChannelType is Slack's, when the delivery carries one: channel, group,
	// im or mpim.
	ChannelType string
	// UserID is the Slack user who wrote it. A message with no user, a bot's,
	// is dropped.
	UserID string
	// Text is the message as Slack sent it, mentions written <@U123>. The
	// router reads the people mentioned from it, and nowhere else.
	Text string
	TS   string
	// ThreadTS is the thread the message is in. apps sets it to the message's
	// own ts for one that starts a thread; empty is taken to mean the same.
	ThreadTS string
	// BotUserID is the bot's own Slack user id in this workspace, which the
	// message's text mentions to address it and which is therefore not a
	// person being asked. A delivery without it cannot be told apart from an
	// ask of the bot, and is dropped.
	BotUserID string
	// SharedExternally is the delivery's own is_ext_shared_channel.
	SharedExternally bool
}

// Click is a click on a button in a message the bot posted.
type Click struct {
	// UserID is the Slack user who clicked.
	UserID    string
	ChannelID string
	MessageTS string
	ActionID  string
	// Value is the button's opaque ticket. It goes to Consent untouched.
	Value string
}

// Profile is one Slack user as apps reads them from users.info, cached for a
// day. Only what is needed to decide who they are is here.
type Profile struct {
	UserID string
	TeamID string
	// Email is as Slack has it, in any case. EmailConfirmed is Slack's own
	// word (is_email_confirmed) that the person controls it; an address not
	// confirmed proves nothing about who they are, and a profile that does not
	// say is one that did not confirm it.
	Email          string
	EmailConfirmed bool
	// Name is what Slack calls them: their display name, or else their name.
	Name string
	// Guest is a single- or multi-channel guest. Bot, Deleted and Stranger
	// (a Slack Connect user from another workspace) are what they say.
	Guest    bool
	Bot      bool
	Deleted  bool
	Stranger bool
}

// ChannelInfo is a Slack conversation as apps reads it now, never from a
// cache.
type ChannelInfo struct {
	ID string
	// Private is a private channel, which a direct message and a group message
	// are as well. Shared is one shared with another organization.
	Private bool
	Shared  bool
	// MPIM is a group message and IM a direct message: conversations, not
	// channels.
	MPIM bool
	IM   bool
	// HighPriority is an administrator's mark, kept in Oryxa, and only ever
	// set on a private, unshared channel.
	HighPriority bool
}

// Target says where a Slack message goes, in the three ways apps takes:
//
//   - ChannelID and ThreadTS: a reply in a thread everyone in the channel sees;
//   - with UserID as well: a message in that thread only that person sees;
//   - UserID alone: a direct message from the bot to that person.
type Target struct {
	ChannelID string `json:"channel_id,omitempty"`
	ThreadTS  string `json:"thread_ts,omitempty"`
	UserID    string `json:"user_id,omitempty"`
}

// Post is one templated message for apps to render and send. What the message
// says is decided in apps from the template and Params, and never here:
// Params is the typed struct this package defines for the template, and the
// adapter encodes it as JSON as it is. apps decodes it strictly, so a field
// the template does not take is refused, and the structs below are exactly
// the fields the two templates take.
type Post struct {
	OrganizationID string
	// IdempotencyKey makes a retry of the same message nothing: apps sends
	// one message for one key.
	IdempotencyKey string
	Target         Target
	Template       string
	Params         any
}

// RefusalParams are the parameters of the refusal template, shown only to the
// asker. apps writes the words, from the reason.
type RefusalParams struct {
	// Reason is one of consentpolicy's refusal codes.
	Reason consentpolicy.Reason `json:"reason"`
	// OrganizationName is the organization's name, for not_member. Left out,
	// the message says "this organization".
	OrganizationName string `json:"organization_name,omitempty"`
	// UserID is the Slack user the refusal is about, for owner_not_member.
	// apps shows it as a mention, which readers see as their name.
	UserID string `json:"user_id,omitempty"`
}

// AskAckParams are the parameters of the ask_ack template, shown only to the
// asker: who was asked, and where to see it.
type AskAckParams struct {
	// Owners are the people asked, in the order they were named, as Slack users.
	Owners []AskedPerson `json:"owners"`
	// OpenURL is the ask in Oryxa's web app, or nothing.
	OpenURL string `json:"open_url,omitempty"`
}

// AskedPerson is one person asked, by Slack user id.
type AskedPerson struct {
	UserID string `json:"user_id"`
}

// Slack is what the router needs of apps' internal Slack API. Every call is
// made with a service token the adapter holds, for the purpose "slack". An
// install that has ended answers each of them the same way, and the adapter
// makes it Permanent: nothing retried will work until the workspace installs
// the app again.
type Slack interface {
	// Inbound reads one verified inbound row by id
	// (GET /v1/internal/apps/slack/inbound/{id}). ErrGone once it is purged.
	Inbound(ctx context.Context, id string) (Inbound, error)
	// Profiles reads the profiles of some users of a workspace
	// (POST /v1/internal/apps/slack/profiles). A user Slack does not know is
	// left out of the answer.
	Profiles(ctx context.Context, organizationID, teamID string, userIDs []string) ([]Profile, error)
	// Channel reads a conversation now (GET /v1/internal/apps/slack/channels/{id}).
	// ErrGone when Slack does not know it, and ErrUnreadable when Slack will
	// not describe it to the bot.
	Channel(ctx context.Context, organizationID, channelID string) (ChannelInfo, error)
	// Post sends a message (POST /v1/internal/apps/slack/post). Sending the
	// same IdempotencyKey again sends nothing more. A post apps will never
	// send, such as into a thread it has not seen, is Permanent; a rate limit
	// is an error with RetryAfter.
	Post(ctx context.Context, post Post) error
}

// Member is a member of an organization as auth keeps them.
type Member struct {
	UserID      string
	Email       string
	DisplayName string
	Role        string
}

// Members reads auth's tables: who a sign-in address belongs to.
type Members interface {
	// ByEmail returns the member of the organization whose sign-in address is
	// email, compared without regard to case, or ErrNotMember. Someone who
	// belongs to other organizations only is not a member of this one.
	ByEmail(ctx context.Context, organizationID, email string) (Member, error)
	// OrganizationName is the organization's name, for a refusal that names it.
	OrganizationName(ctx context.Context, organizationID string) (string, error)
}

// Person is a member of the organization, and the Slack user they are.
type Person struct {
	UserID string
	// Email is the member's sign-in address, lower-cased. It is what the
	// organization chart is written in.
	Email string
	// Name is what to call them: their Slack name, cleaned.
	Name        string
	SlackUserID string
}

// Origin is where an ask was made.
type Origin struct {
	TeamID    string
	ChannelID string
	// ThreadTS is the thread the answer belongs in: the thread the mention
	// was in, or the mention's own message when it started one.
	ThreadTS string
	// TriggerTS is the mention's own message. Together with the team and
	// channel it names the ask, so making it twice makes it once.
	TriggerTS        string
	AskerSlackUserID string
}

// AskOwner is one person to ask.
type AskOwner struct {
	Person
	// Bypass, when set, lets this owner's request go ahead without their
	// consent, for the reason it says. The reason is the code recorded with the
	// decision (decided_by {bypass, reason}); AnswerTo says where the answer
	// goes, which whoever posts it derives from the reason as well: the thread
	// for priority_channel, the asker's direct message for org_chart. Telling
	// the owner and auditing it are the consent core's and the surfaces
	// service's; the router only says it.
	Bypass *consentpolicy.Bypass
}

// AskInput is one ask: one question to one or more owners.
type AskInput struct {
	OrganizationID string
	Asker          Person
	Origin         Origin
	// Question is what the asker wrote, without the mention of the bot and
	// with the other mentions written as names.
	Question string
	// Owners are the people asked, in parallel: the first answer that is
	// delivered wins and the rest are closed.
	Owners []AskOwner
}

// Ask is what CreateAsk made.
type Ask struct {
	ID string
}

// Asks is the consent core's side of what the router does: file an ask,
// count an asker's open ones, and hand a click to the decision.
type Asks interface {
	// CreateAsk files one request to each owner, from the asker's primary
	// agent, all under one ask. It is idempotent: the same Origin and owners
	// again return the ask already made and make nothing more.
	CreateAsk(ctx context.Context, input AskInput) (Ask, error)
	// CountOpenAsks is how many asks the asker has that are not settled.
	CountOpenAsks(ctx context.Context, organizationID, askerUserID string) (int, error)
	// Consent hands a click to the decision, unchanged: the ticket the button
	// carried, the member who clicked, and the surface. It decides nothing
	// here; whether the ticket is genuine, whether the clicker is the person
	// it names and whether the request is still waiting are its to check. A
	// click it refuses, for any of those, is Permanent.
	Consent(ctx context.Context, ticket, clickerUserID, surface string) error
}

// Authority is the one question the organization chart answers for bypass.
type Authority interface {
	// Outranks reports whether asker is above owner in the organization's
	// chart. It answers false, and no error, for anything the chart cannot
	// say: an asker who has given agents no standing grant, or a chart that
	// names neither of them. An error is a chart that could not be read.
	Outranks(ctx context.Context, organizationID string, asker, owner Person) (bool, error)
}

// Identity is the match between a Slack user and a member, as last resolved.
type Identity struct {
	OrganizationID string
	TeamID         string
	SlackUserID    string
	MemberUserID   string
	// Email is the address the match was made on, lower-cased.
	Email       string
	DisplayName string
	ResolvedAt  time.Time
}

// Identities keeps the matches. Store implements it.
type Identities interface {
	SaveIdentity(ctx context.Context, identity Identity) error
	DeleteIdentity(ctx context.Context, teamID, slackUserID string) error
}

// Asked records that a mention has been turned into an ask, so that a retry
// of the same job, which would count the ask it just made against its
// asker's limit, does not make it twice or refuse it. Store implements it.
type Asked interface {
	MarkAsked(ctx context.Context, inboundID, askID string) error
}

type permanentError struct{ err error }

func (e *permanentError) Error() string { return e.err.Error() }
func (e *permanentError) Unwrap() error { return e.err }

// Permanent marks err as one a retry would only repeat.
func Permanent(err error) error {
	if err == nil || IsPermanent(err) {
		return err
	}
	return &permanentError{err: err}
}

// IsPermanent reports whether err carries a Permanent mark.
func IsPermanent(err error) bool {
	var permanent *permanentError
	return errors.As(err, &permanent)
}

// retryAfterOf reads the delay an error asks for, if it asks for one.
func retryAfterOf(err error) time.Duration {
	var hinted interface{ RetryAfter() time.Duration }
	if errors.As(err, &hinted) {
		return hinted.RetryAfter()
	}
	return 0
}
