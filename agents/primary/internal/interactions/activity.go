package interactions

import (
	"context"
	"github.com/Shubham04-oss/oryxa-go/internal/access"
	"slices"
	"time"

	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/agents"
)

// What an entry of a member's activity says happened.
const (
	// ActivityAsked is a Slack message that asked, or was asked of, the member.
	ActivityAsked = "asked"
	// ActivityDecided is the decision of a request: who made it, where, and
	// how far it lets the answer go. A decision made by a bypass, and one made
	// under a lease, are these, with the decider saying so.
	ActivityDecided = "decided"
	// ActivityAnswered is an answer that reached the member who asked, or
	// the failure that ended the request.
	ActivityAnswered = "answered"
	// ActivitySecretViewed is the member who asked opening the one-time secret
	// shared for a request.
	ActivitySecretViewed = "secret_viewed"
	// ActivityLeaseGiven is an owner giving an asker access in a thread; the
	// lease says until when, and whether it was revoked.
	ActivityLeaseGiven = "lease_given"
	// ActivityLeaseRevoked is an owner revoking one.
	ActivityLeaseRevoked = "lease_revoked"
	// ActivityAccessed is someone else, or one of the member's own agents,
	// reaching a file, a collection or a source, with the rule that let it
	// (internal/access's decision log).
	ActivityAccessed = "accessed"
)

// Decisions reads a member's part of the access decision log;
// *access.Log is it.
type Decisions interface {
	ForMember(ctx context.Context, organizationID, userID string, limit int) ([]access.Decision, error)
}

// The member's side of an activity entry.
const (
	// ActivityOwner is an entry about a request addressed to the member, or a
	// lease they gave.
	ActivityOwner = "owner"
	// ActivityAsker is one about a request the member asked, or a lease they
	// hold.
	ActivityAsker = "asker"
)

// ActivityEntry is one thing that happened to the member's Slack asks, as the
// ledger holds it. It is names and states, never a question or an answer: the
// request it points to is read, as the member, for those.
type ActivityEntry struct {
	Kind string    `json:"kind"`
	At   time.Time `json:"at"`
	// Role is ActivityOwner or ActivityAsker: which side of it the member is.
	Role string `json:"role"`
	// WithUserID is the other member.
	WithUserID string `json:"with_user_id,omitempty"`
	RequestID  string `json:"request_id,omitempty"`
	AskID      string `json:"ask_id,omitempty"`
	// Status is the request's status now.
	Status string `json:"status,omitempty"`
	// Decision, Consent and DecidedBy are set on a decided entry: approved or
	// rejected, how far the answer may go, and who decided, where and why.
	Decision  string                     `json:"decision,omitempty"`
	Consent   string                     `json:"consent,omitempty"`
	DecidedBy *agents.InteractionDecider `json:"decided_by,omitempty"`
	// WrittenByOwner is set on an answered entry whose answer the owner wrote
	// themself (result.written_by_owner): their reply, instead of their agent's
	// answer, whether the request was pending or its agent's answer was held.
	WrittenByOwner bool `json:"written_by_owner,omitempty"`
	// Access is set on an accessed entry: who reached what, and why.
	Access *access.Decision `json:"access,omitempty"`
	// Lease is set on the two lease entries.
	Lease *Lease `json:"lease,omitempty"`
}

// Activity is what happened to the member's Slack asks, newest first: asks
// made and received, how each was decided and where, bypasses and leases among
// the decisions, answers, leases given and revoked, and secrets opened. It is
// read from the ledger's own state, so it lists what the ledger still has, and
// reads as its two parties may read it (viewFor): a member never sees an answer
// that was held, or its owner's review of it.
func (service *Service) Activity(ctx context.Context, actor agents.InteractionActor, limit int) ([]ActivityEntry, error) {
	if err := validateActor(actor); err != nil {
		return nil, err
	}
	query := Query{Box: BoxAll, Origin: OriginSlack, Limit: clamp(limit)}
	requests, err := service.repository.List(ctx, actor.OrganizationID, actor.UserID, query)
	if err != nil {
		return nil, err
	}
	entries := []ActivityEntry{}
	for _, request := range filterRequests(requests, func(request agents.Interaction) bool { return listed(actor, query, request) }) {
		entries = append(entries, requestEntries(actor, service.viewFor(actor, request))...)
	}
	if service.consents != nil {
		leases, err := service.consents.Leases(ctx, actor.OrganizationID, actor.UserID, query.Limit)
		if err != nil {
			return nil, err
		}
		for _, lease := range leases {
			entries = append(entries, leaseEntries(actor, lease)...)
		}
	}
	if service.decisions != nil {
		decisions, err := service.decisions.ForMember(ctx, actor.OrganizationID, actor.UserID, query.Limit)
		if err != nil {
			return nil, err
		}
		for _, decision := range decisions {
			entries = append(entries, accessEntry(actor, decision))
		}
	}
	slices.SortStableFunc(entries, func(a, b ActivityEntry) int { return b.At.Compare(a.At) })
	if len(entries) > query.Limit {
		entries = entries[:query.Limit]
	}
	return entries, nil
}

// requestEntries is what one request of the member's says happened, oldest
// first.
func requestEntries(actor agents.InteractionActor, request agents.Interaction) []ActivityEntry {
	role, with := ActivityAsker, request.Target.UserID
	if request.Target.UserID == actor.UserID {
		role, with = ActivityOwner, request.Requester.UserID
	}
	entry := func(kind string, at time.Time) ActivityEntry {
		return ActivityEntry{Kind: kind, At: at, Role: role, WithUserID: with, RequestID: request.ID, AskID: request.AskID, Status: request.Status}
	}
	entries := []ActivityEntry{entry(ActivityAsked, request.CreatedAt)}
	if decision := request.Decision; decision != nil {
		decided := entry(ActivityDecided, decision.DecidedAt)
		decided.Decision, decided.Consent, decided.DecidedBy = decision.Decision, decision.Consent, decision.DecidedBy
		entries = append(entries, decided)
	}
	if result := request.Result; result != nil {
		answered := entry(ActivityAnswered, result.CompletedAt)
		answered.WrittenByOwner = result.WrittenByOwner
		entries = append(entries, answered)
	}
	if request.SecretViewedAt != nil {
		entries = append(entries, entry(ActivitySecretViewed, *request.SecretViewedAt))
	}
	return entries
}

// leaseEntries is what a lease of the member's says happened: it was given,
// and it was revoked when it was.
func leaseEntries(actor agents.InteractionActor, lease Lease) []ActivityEntry {
	role, with := ActivityAsker, lease.OwnerUserID
	if lease.OwnerUserID == actor.UserID {
		role, with = ActivityOwner, lease.AskerUserID
	}
	shown := lease
	entries := []ActivityEntry{{Kind: ActivityLeaseGiven, At: lease.CreatedAt, Role: role, WithUserID: with, RequestID: lease.RequestID, Lease: &shown}}
	if lease.RevokedAt != nil {
		entries = append(entries, ActivityEntry{Kind: ActivityLeaseRevoked, At: *lease.RevokedAt, Role: role, WithUserID: with, RequestID: lease.RequestID, Lease: &shown})
	}
	return entries
}

// accessEntry is one allow from the decision log, from the member's side: the
// owner of what was reached, or the member whose agent reached it.
func accessEntry(actor agents.InteractionActor, decision access.Decision) ActivityEntry {
	role, with := ActivityOwner, decision.ActorUserID
	if decision.OwnerID != actor.UserID {
		role, with = ActivityAsker, decision.OwnerID
	}
	if with == actor.UserID {
		with = ""
	}
	shown := decision
	return ActivityEntry{Kind: ActivityAccessed, At: decision.At, Role: role, WithUserID: with, Access: &shown}
}
