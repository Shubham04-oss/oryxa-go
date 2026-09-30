package surfaces

import (
	"context"

	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/agents"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/appsclient"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/interactions"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/slackrouter"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/threads"
)

// This file is everything the service asks of the rest of Oryxa, each from its
// own side and satisfied by what the service wires, so that it is built and
// tested against fakes. The ledger reads are the unfiltered rows: the service
// reads a request as Oryxa itself, and takes care to say nothing of what it
// holds that the member who asked may not read (an answer held for review is
// the one such thing, which only its owner is shown, in their own direct
// message).

// Requests reads the ledger's requests. interactions.Store implements it.
type Requests interface {
	// Get reads one request as the ledger holds it, or interactions.ErrNotFound.
	Get(ctx context.Context, requestID string) (agents.Interaction, error)
}

// Asks reads the ledger's asks: where an answer goes, who asked, and who won.
// interactions.Store and interactions.Service implement it.
type Asks interface {
	// Ask reads one ask, or interactions.ErrNotFound.
	Ask(ctx context.Context, askID string) (interactions.Ask, error)
}

// Tickets makes the tickets for the buttons of an owner's notice, and of their
// review of an answer the guards held. interactions.Service implements it.
type Tickets interface {
	// ConsentTickets makes them for the notice noticeID of a request. The request
	// is read in doing so: one that was decided is interactions.ErrConflict, one
	// past its expiry interactions.ErrExpired, and one that does not ask for
	// consent interactions.ErrInvalid.
	ConsentTickets(ctx context.Context, requestID, noticeID string) (interactions.ConsentTickets, error)
	// ReviewTickets makes them for the review reviewID of a request's held
	// answer: Send and Withhold, and SendMasked only for an answer with a masked
	// version. The request is read in doing so: one whose answer is no longer in
	// review is interactions.ErrConflict, one past its expiry
	// interactions.ErrExpired, and one not asked in Slack interactions.ErrInvalid.
	ReviewTickets(ctx context.Context, requestID, reviewID string) (interactions.ReviewTickets, error)
}

// Slack is what the service asks of apps' internal Slack API.
// *appsclient.Client implements it.
type Slack interface {
	// Send posts a templated message and says where it went. The same
	// IdempotencyKey sends nothing more, and says where the first went.
	Send(ctx context.Context, post slackrouter.Post) (appsclient.Posted, error)
	// Update replaces a message apps posted.
	Update(ctx context.Context, update appsclient.Update) error
	// ChannelDetails reads a conversation now: what it is, whether an
	// administrator marked it, and its name.
	ChannelDetails(ctx context.Context, organizationID, channelID string) (appsclient.ChannelDetails, error)
}

// SlackUsers says which Slack user a member was last matched to. The router's
// store implements it (slackrouter.Store.SlackUserFor); ErrNoIdentity for a
// member it never matched.
type SlackUsers interface {
	SlackUserFor(ctx context.Context, organizationID, memberUserID string) (slackrouter.Identity, error)
}

// Notes writes a line in a member's own agent chat without a model.
// *threads.Service implements it.
type Notes interface {
	Note(ctx context.Context, member threads.Member, note threads.Note) error
}

// Members says whether a member still belongs to an organization, as auth's
// tables have it. *adksession.Store implements it.
//
// A note makes a thread in the asker's agent chat that they did not ask for in
// Oryxa: they asked in Slack. As for a wake-up, a member who left must not have
// threads made for them, so the note is written only for one who still belongs.
type Members interface {
	IsMember(ctx context.Context, organizationID, userID string) (bool, error)
}

// links is the part of the store a pass needs. *Store implements it.
type links interface {
	Link(ctx context.Context, requestID, role string) (Link, bool, error)
	PutLink(ctx context.Context, link Link) error
	SetLinkState(ctx context.Context, requestID, role, state string) error
}
