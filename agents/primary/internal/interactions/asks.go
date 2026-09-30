package interactions

import (
	"context"
	"errors"
	"time"

	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/agents"
)

// The Slack consent loop. A member mentions the Oryxa app in a Slack message
// and names the people whose agents they want to ask: that message is an ask,
// and it makes one request for each owner it names, addressed to the owner's
// primary agent and filed on the asker's behalf. An owner decides their own
// request, in Slack or on the web, with one of three choices (consent.go):
// an AI summary alone, access for an hour, or no. Access also gives the asker
// a lease (leases.go), which approves their follow-ups in that thread without
// asking again. The first request whose answer is delivered settles the ask,
// and the requests that did not answer are closed (Store.Respond).
//
// The ledger is the one source of truth. It carries no Slack client and no
// Slack identity: the router that turns a mention into a call (CreateAsk)
// has already checked who the asker and the owners are, and every message a
// person reads in Slack is posted from a re-read of the ledger.

// Where a request was made (agents.Interaction.Origin).
const (
	// OriginAgent is a request an agent filed for its member in the ordinary
	// way, which is every request made before Slack asks.
	OriginAgent = "agent"
	// OriginSlack is a request made from a Slack message.
	OriginSlack = "slack"
)

// The permissions a Slack request asks for, and the owner's approval grants
// one or both of: an approval of either is a subset of what was asked, as
// validateDecision requires.
const (
	// PermissionConsentSummary lets the answer be an AI summary alone.
	PermissionConsentSummary = "consent:summary"
	// PermissionConsentAccess lets the answer quote what the agent read.
	PermissionConsentAccess = "consent:access"
)

// What an owner allowed (agents.InteractionDecision.Consent).
const (
	ConsentSummary = "summary"
	ConsentAccess  = "access"
	// ConsentReply is an owner who answered the request themself, in their own
	// words, instead of letting their agent: the request is approved and
	// answered in one change, and nothing is granted to their agent, which
	// never runs for it (reply.go). No permission grants it.
	ConsentReply = "reply"
)

// ConsentFor is how far a decision let the answer go, which follows from the
// permissions it granted: ConsentAccess when consent:access was, else
// ConsentSummary when consent:summary was, and none, for a rejection and for
// any other approval.
func ConsentFor(decision string, granted []string) string {
	if decision != DecisionApproved {
		return ""
	}
	consent := ""
	for _, permission := range granted {
		switch permission {
		case PermissionConsentAccess:
			return ConsentAccess
		case PermissionConsentSummary:
			consent = ConsentSummary
		}
	}
	return consent
}

// Surfaces a decision is made on (agents.InteractionDecider.Surface).
const (
	SurfaceWeb    = "web"
	SurfaceSlack  = "slack"
	SurfaceLease  = "lease"
	SurfaceBypass = "bypass"
)

// The kinds of ask.
const (
	// AskQuestion asks for an answer.
	AskQuestion = "question"
	// AskSecret asks for a credential, which the owner shares as a one-time
	// view in Oryxa (secrets.go) and never in Slack.
	AskSecret = "secret"
)

// MaxOpenAsks is how many asks one member may have open at once. The router
// refuses the next (CountOpenAsks): the ledger only counts.
const MaxOpenAsks = 5

// The reason a request closed because another owner answered its ask carries
// (agents.InteractionDecider.Reason).
const ReasonAnsweredElsewhere = "answered_elsewhere"

var (
	// ErrConsentUnavailable reports a Slack ask, consent, ticket or lease
	// asked of a ledger that is not set up for them.
	ErrConsentUnavailable = errors.New("slack consent is not available on this server")
	// ErrAlreadyDecided reports a consent that lost: the request was decided
	// first, by another click or another surface. The ConsentResult that comes
	// with it holds the request as it now stands, and so the winner.
	ErrAlreadyDecided = errors.New("agent request was already decided")
	// ErrAnsweredElsewhere reports an answer that lost: another owner's answer
	// settled the ask first, and Oryxa closed this request instead.
	ErrAnsweredElsewhere = errors.New("another owner already answered this ask, so Oryxa closed this request")
	// ErrRevoked reports an answer that lost to its owner: the lease the
	// request was approved under was revoked before the answer was sent, and
	// Oryxa closed the request instead of delivering it.
	ErrRevoked = errors.New("the owner revoked the access this request was approved under, so Oryxa closed it")
)

// Ask is what one Slack message asked of one or more owners. It holds where
// the answer goes and who asked, never what was asked: that is in the
// requests.
type Ask struct {
	ID             string
	OrganizationID string
	// Origin is OriginSlack, the only origin asks have.
	Origin string
	// TeamID, ChannelID and ThreadTS are where the asker wrote and where the
	// answer goes: the workspace, the channel, and the thread the answer is
	// posted in, which is the message's own ts when it started none. TriggerTS
	// is the asking message's ts, which is what a retried delivery of it is
	// recognised by.
	TeamID, ChannelID, ThreadTS, TriggerTS string
	AskerUserID                            string
	// Kind is AskQuestion or AskSecret.
	Kind string
	// WinnerRequestID is the first request completed with a delivered answer,
	// empty while there is none. An ask has one winner, set once.
	WinnerRequestID string
	// Bypass says a request of the ask was approved by a bypass.
	Bypass    bool
	CreatedAt time.Time
	// SettledAt is when the winner answered.
	SettledAt *time.Time
}

// Open says the ask is still waiting for an answer.
func (ask Ask) Open() bool { return ask.WinnerRequestID == "" }

// AnsweredElsewhere says a request of the ask was not answered because another
// owner's answer settled the ask first: the ask has a winner, and it is not this
// request. Such a request was closed with "Answered by ...", a pending one
// rejected and an approved one failed, and its notice says so rather than that
// it failed or was denied.
func AnsweredElsewhere(ask Ask, request agents.Interaction) bool {
	return ask.ID != "" && ask.ID == request.AskID && ask.WinnerRequestID != "" && ask.WinnerRequestID != request.ID
}

// Lease is what an owner gave an asker in one Slack thread: for as long as it
// lives, the asker's follow-ups there are approved with no click. It is given
// by approving a request with access, and ends when it expires or the owner
// revokes it.
type Lease struct {
	ID             string     `json:"id"`
	OrganizationID string     `json:"organization_id"`
	AskerUserID    string     `json:"asker_user_id"`
	OwnerUserID    string     `json:"owner_user_id"`
	ChannelID      string     `json:"channel_id"`
	ThreadTS       string     `json:"thread_ts"`
	RequestID      string     `json:"request_id,omitempty"`
	CreatedAt      time.Time  `json:"created_at"`
	ExpiresAt      time.Time  `json:"expires_at"`
	RevokedAt      *time.Time `json:"revoked_at,omitempty"`
}

// Live says the lease approves follow-ups at at: it has neither run out nor
// been revoked.
func (lease Lease) Live(at time.Time) bool {
	return lease.RevokedAt == nil && lease.ExpiresAt.After(at)
}

// AskRequest is one request of an ask, as the ledger is asked to store it.
type AskRequest struct {
	// Request is the request in full: pending, revision 1, its AskID left for
	// the store to set.
	Request agents.Interaction
	// Key is the request's idempotency key, which makes the ask replay-safe.
	Key string
	// Bypass, when set, has the store approve the request in the transaction
	// that creates it, with no owner click.
	Bypass *BypassDecision
	// OnLease is how to approve the request when a live lease of its owner
	// covers the asker in this thread; the store finds the lease and records
	// the decision as the lease's. A lease wins over a bypass.
	OnLease *agents.InteractionDecisionInput
}

// BypassDecision is a decision no owner click makes: the ledger records it as
// a bypass, with why.
type BypassDecision struct {
	// Reason is a short code, not a person's words: it is kept, sent on the
	// bus and shown to the owner.
	Reason string
	// Input is what the approval says and grants.
	Input agents.InteractionDecisionInput
}

// AskedRequest is a request of an ask as CreateAsk found it.
type AskedRequest struct {
	Request agents.Interaction
	// Created says this call made the request, and not an earlier one that this
	// call is a retry of.
	Created bool
}

// ConsentRecord is an owner's decision of a request as the ledger records it,
// with the lease access gives.
type ConsentRecord struct {
	Input   agents.InteractionDecisionInput
	Decider agents.InteractionDecider
	// LeaseID and LeaseTTL, when the TTL is positive, have the store give the
	// asker a lease in the ask's thread for that long, in the transaction that
	// records the decision. An existing live lease is renewed, and keeps its id.
	LeaseID  string
	LeaseTTL time.Duration
}

// Consents is the part of the ledger the Slack consent loop needs beyond
// Repository. Store implements it; New takes it from the Repository when that
// is one.
type Consents interface {
	// CreateAsk records an ask and a request for each owner in one
	// transaction, under their idempotency keys: an ask made again, as a
	// retried Slack delivery makes it, stores nothing and returns what is
	// there. It records each new request's requested event, and, for one
	// approved as it is created, its decided event after it. A request an
	// owner's live lease covers is approved under that lease.
	CreateAsk(ctx context.Context, ask Ask, requests []AskRequest, at time.Time) (Ask, []AskedRequest, error)
	// Ask reads one ask, or ErrNotFound.
	Ask(ctx context.Context, id string) (Ask, error)
	// CountOpenAsks is how many asks the member asked have no winner yet and
	// at least one request still waiting, unexpired, at at.
	CountOpenAsks(ctx context.Context, organizationID, askerUserID string, at time.Time) (int, error)
	// Consent records a pending request's decision and, for access, gives the
	// lease, in one transaction. A request already decided is ErrConflict.
	Consent(ctx context.Context, id string, record ConsentRecord, at time.Time) (agents.Interaction, *Lease, error)
	// Bypass records a pending request's approval as a bypass, and marks its
	// ask so. A request already decided is ErrConflict.
	Bypass(ctx context.Context, id string, decision BypassDecision, at time.Time) (agents.Interaction, error)
	// RevokeLease ends a live lease of the owner's, recording the revoked
	// event. It reports whether this call revoked it: one already revoked, or
	// run out, is returned as it is, with no event.
	RevokeLease(ctx context.Context, organizationID, id, ownerUserID string, at time.Time) (Lease, bool, error)
	// Leases lists, newest first, the leases the member gave and the ones they
	// hold.
	Leases(ctx context.Context, organizationID, userID string, limit int) ([]Lease, error)
	// ActiveLeases selects only live windows the member gave or holds, and
	// returns at most limit+1 rows for a page's continuation check.
	ActiveLeases(ctx context.Context, organizationID, userID string, limit int, before LeaseCursor, at time.Time) ([]Lease, error)
}
