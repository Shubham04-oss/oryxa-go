package interactions

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/agents"
)

// The three choices an owner has of a request that came from Slack. Each is a
// decision, recorded as any other is: what it grants is a subset of the
// permissions the request asked for, and what the answer may then be follows
// from it (ConsentFor).
//
//	summary  approved, granting consent:summary: the answer is an AI summary
//	access   approved, granting consent:summary and consent:access: the answer
//	         may quote what the agent read, and for LeaseTTL the same asker's
//	         follow-ups in that thread are approved with no click (a lease)
//	deny     rejected, granting nothing
//
// A choice is made in Slack, on the button of a notice (Consent, with the
// button's ticket), or on the web (ConsentAsOwner): whichever comes first
// decides, by the lock Store.Decide takes on the request, and the other finds
// it decided (ErrAlreadyDecided). A choice only ever decides: it never
// reviews an answer that was held. Instead of choosing, the owner may answer
// themself, in their own words (reply.go), which is no choice of these three:
// it takes the same lock, and whichever of it and a choice comes first wins.
const (
	ChoiceSummary = "summary"
	ChoiceAccess  = "access"
	ChoiceDeny    = "deny"
)

// LeaseTTL is how long access lets the asker's follow-ups in a thread go
// unasked: an hour, or less when the organization's workspace policy expires
// consent sooner.
const LeaseTTL = time.Hour

// What the asker reads of an owner's choice, in fixed words: an owner deciding
// with a button writes nothing of their own.
const (
	summaryApproved = "Approved: answer shared."
	consentDenied   = "Denied."
)

func validChoice(choice string) bool {
	return choice == ChoiceSummary || choice == ChoiceAccess || choice == ChoiceDeny
}

// asksConsent is whether a request asks the owner for the consent choices:
// the requests the Slack app makes, and no other.
func asksConsent(request agents.Interaction) bool {
	return slices.Contains(request.RequestedPermissions, PermissionConsentSummary) &&
		slices.Contains(request.RequestedPermissions, PermissionConsentAccess)
}

// ConsentResult is what an owner's choice came to.
type ConsentResult struct {
	// Request is the request as it now stands. When the choice lost
	// (ErrAlreadyDecided) it is the request as the winner left it, whose
	// decision says who won, where, and what they chose.
	Request agents.Interaction
	// Choice is the choice that was made.
	Choice string
	// Lease is the lease access gave; nil for any other choice, and for a
	// choice that lost.
	Lease *Lease
	// NoticeID is the notice the button was on, from its ticket; empty for a
	// choice made on the web.
	NoticeID string
}

// ConsentAsOwner records the choice of the member a request is addressed to,
// made in their own signed-in session on the web: the actor must be marked
// agents.SessionMember, which only the HTTP API's member path does. It
// answers ErrAlreadyDecided, with the request as it stands, when the request
// was decided first.
func (service *Service) ConsentAsOwner(ctx context.Context, actor agents.InteractionActor, id, choice string) (ConsentResult, error) {
	if actor.Session != agents.SessionMember {
		return ConsentResult{}, fmt.Errorf("%w: only the member, in their own signed-in session, decides as the owner", ErrForbidden)
	}
	if err := validateActor(actor); err != nil {
		return ConsentResult{}, err
	}
	choice = strings.ToLower(strings.TrimSpace(choice))
	if !validChoice(choice) {
		return ConsentResult{}, fmt.Errorf("%w: choice must be summary, access or deny", ErrInvalid)
	}
	request, err := service.repository.Get(ctx, strings.TrimSpace(id))
	if err != nil {
		return ConsentResult{}, err
	}
	if request.OrganizationID != actor.OrganizationID || !canRead(actor, request) {
		return ConsentResult{}, ErrNotFound
	}
	if request.Target.UserID != actor.UserID {
		return ConsentResult{}, ErrForbidden
	}
	decider := agents.InteractionDecider{Kind: DecidedByMember, UserID: actor.UserID, Surface: SurfaceWeb}
	result, err := service.consent(ctx, request, choice, decider, "")
	result.Request = service.viewFor(actor, result.Request)
	return result, err
}

// Consent records the choice a click in Slack made. ticket is the value of
// the button that was clicked, clickerUserID the member Slack's user was
// matched to, and surface where the click was (SurfaceSlack; SurfaceWeb for a
// ticket used on the web). It decides only when the ticket is a consent ticket
// this ledger made and has not run out (ErrTicketInvalid, ErrTicketExpired: a
// review's ticket decides nothing), the clicker is the member the ticket was
// made for and the request is addressed to (ErrForbidden), and the request is
// still pending: a request decided first answers ErrAlreadyDecided, with the
// request as it stands. The choice is the ticket's, never the caller's.
func (service *Service) Consent(ctx context.Context, ticket, clickerUserID, surface string) (ConsentResult, error) {
	if service.tickets == nil {
		return ConsentResult{}, fmt.Errorf("%w: no ticket key is set", ErrConsentUnavailable)
	}
	claims, err := service.tickets.Verify(ticket, service.now().UTC())
	if err != nil {
		return ConsentResult{}, err
	}
	if claims.Kind != TicketConsent {
		return ConsentResult{}, fmt.Errorf("%w: it is not a consent ticket", ErrTicketInvalid)
	}
	if surface != SurfaceSlack && surface != SurfaceWeb {
		return ConsentResult{}, fmt.Errorf("%w: a ticket is used in Slack or on the web", ErrInvalid)
	}
	if strings.TrimSpace(clickerUserID) == "" || clickerUserID != claims.OwnerUserID {
		return ConsentResult{}, fmt.Errorf("%w: this ticket is for another member", ErrForbidden)
	}
	request, err := service.repository.Get(ctx, claims.RequestID)
	if err != nil {
		return ConsentResult{}, err
	}
	if request.Target.UserID != claims.OwnerUserID {
		return ConsentResult{}, fmt.Errorf("%w: this ticket is not for this request's owner", ErrForbidden)
	}
	decider := agents.InteractionDecider{Kind: DecidedByMember, UserID: claims.OwnerUserID, Surface: surface}
	return service.consent(ctx, request, claims.Choice, decider, claims.NoticeID)
}

// consent decides a request the owner's choice is for. The request has been
// read from the ledger, and its owner proved to be the one choosing.
func (service *Service) consent(ctx context.Context, request agents.Interaction, choice string, decider agents.InteractionDecider, noticeID string) (ConsentResult, error) {
	result := ConsentResult{Request: request, Choice: choice, NoticeID: noticeID}
	if service.consents == nil {
		return result, ErrConsentUnavailable
	}
	if !asksConsent(request) {
		return result, fmt.Errorf("%w: this request does not ask for consent; decide it with a decision", ErrInvalid)
	}
	now := service.now().UTC()
	switch {
	case request.Status != StatusPending:
		return result, ErrAlreadyDecided
	case !request.ExpiresAt.After(now):
		return result, ErrExpired
	}
	record := ConsentRecord{Decider: decider}
	var ttl time.Duration
	if choice == ChoiceAccess {
		var err error
		if ttl, err = service.leaseTTL(ctx, request.OrganizationID); err != nil {
			return result, err
		}
		id, err := newID("ils_")
		if err != nil {
			return result, err
		}
		record.LeaseID, record.LeaseTTL = id, ttl
	}
	record.Input = consentInput(request, choice, ttl)
	if err := validateDecision(request, record.Input); err != nil {
		return result, err
	}
	decided, lease, err := service.consents.Consent(ctx, request.ID, record, now)
	if errors.Is(err, ErrConflict) {
		// Decided while this call was reading it: the request as it stands is
		// the winner's.
		current, getErr := service.repository.Get(ctx, request.ID)
		if getErr != nil {
			return result, getErr
		}
		result.Request = current
		return result, ErrAlreadyDecided
	}
	if err != nil {
		return result, err
	}
	service.logger.InfoContext(ctx, "an owner decided a request from Slack",
		"request_id", request.ID, "ask_id", request.AskID, "choice", choice, "surface", decider.Surface, "lease", lease != nil)
	result.Request, result.Lease = decided, lease
	return result, nil
}

// consentInput is the decision a choice makes: what it says and what it
// grants, which is always a subset of what the request asked for.
func consentInput(request agents.Interaction, choice string, ttl time.Duration) agents.InteractionDecisionInput {
	tools := nonNilStrings(slices.Clone(request.RequestedTools))
	switch choice {
	case ChoiceSummary:
		return agents.InteractionDecisionInput{
			Decision: DecisionApproved, Message: summaryApproved, AllowedTools: tools,
			GrantedPermissions: []string{PermissionConsentSummary},
		}
	case ChoiceAccess:
		return agents.InteractionDecisionInput{
			Decision: DecisionApproved, Message: "Approved: access for " + spellDuration(ttl) + ".", AllowedTools: tools,
			GrantedPermissions: []string{PermissionConsentSummary, PermissionConsentAccess},
		}
	default:
		return agents.InteractionDecisionInput{Decision: DecisionRejected, Message: consentDenied}
	}
}

// leaseTTL is how long access leaves an asker's follow-ups approved: LeaseTTL,
// or the organization's consent expiry when that is shorter.
func (service *Service) leaseTTL(ctx context.Context, organizationID string) (time.Duration, error) {
	settings, err := service.organizationPolicy(ctx, organizationID)
	if err != nil {
		return 0, err
	}
	ttl := LeaseTTL
	if settings.ExpireAfter > 0 && settings.ExpireAfter < ttl {
		ttl = settings.ExpireAfter
	}
	return ttl, nil
}

// spellDuration is a lease's length in words: "1 hour", "2 hours",
// "30 minutes", "1 minute".
func spellDuration(ttl time.Duration) string {
	if ttl >= time.Hour && ttl%time.Hour == 0 {
		return plural(int(ttl/time.Hour), "hour")
	}
	return plural(max(int(ttl/time.Minute), 1), "minute")
}

func plural(n int, unit string) string {
	if n == 1 {
		return "1 " + unit
	}
	return fmt.Sprintf("%d %ss", n, unit)
}

// ConsentTickets are the tickets for the buttons of one notice: the three
// choices, and Reply yourself.
type ConsentTickets struct {
	Summary, Access, Deny string
	// Reply is the ticket of the Reply yourself button: a reply ticket
	// (TicketReply) that lands only while the request is pending, which is
	// what the notice shows.
	Reply string
	// ExpiresAt is when they stop working: when the request expires.
	ExpiresAt time.Time
}

// ConsentTickets makes the tickets for the buttons of the notice noticeID that
// tells the owner of a request that it is waiting for them. The request is read
// here, and its owner and expiry are the ledger's: a ticket names no one the
// request does not. Only a request that asks for consent, and is still pending,
// has buttons. An owner-first request (OwnerFirst) has no access ticket: what
// it asks is its owner's to say, and their notice offers no access to give.
func (service *Service) ConsentTickets(ctx context.Context, requestID, noticeID string) (ConsentTickets, error) {
	if service.tickets == nil {
		return ConsentTickets{}, fmt.Errorf("%w: no ticket key is set", ErrConsentUnavailable)
	}
	request, err := service.repository.Get(ctx, strings.TrimSpace(requestID))
	if err != nil {
		return ConsentTickets{}, err
	}
	switch {
	case !asksConsent(request):
		return ConsentTickets{}, fmt.Errorf("%w: this request does not ask for consent", ErrInvalid)
	case request.Status != StatusPending:
		return ConsentTickets{}, ErrConflict
	case !request.ExpiresAt.After(service.now()):
		return ConsentTickets{}, ErrExpired
	}
	var made ConsentTickets
	made.ExpiresAt = request.ExpiresAt.UTC()
	for _, button := range []struct {
		kind, choice string
		into         *string
	}{
		{TicketConsent, ChoiceSummary, &made.Summary}, {TicketConsent, ChoiceAccess, &made.Access}, {TicketConsent, ChoiceDeny, &made.Deny},
		{TicketReply, StatusPending, &made.Reply},
	} {
		if button.choice == ChoiceAccess && OwnerFirst(request) {
			continue
		}
		ticket, err := service.tickets.Mint(TicketClaims{
			RequestID: request.ID, OwnerUserID: request.Target.UserID, NoticeID: noticeID,
			Kind: button.kind, Choice: button.choice, ExpiresAt: request.ExpiresAt,
		})
		if err != nil {
			return ConsentTickets{}, err
		}
		*button.into = ticket
	}
	return made, nil
}
