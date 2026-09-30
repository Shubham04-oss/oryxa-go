package interactions

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/agents"
)

// An answer to a request asked in Slack that the guards hold waits for its
// owner's review, as every held answer does. The owner is told in Slack: a
// direct message with the answer and three buttons, Send, Send masked (only
// for an answer that has a masked version) and Don't send, each carrying a
// review ticket (tickets.go). A click is the owner's review exactly as their
// review on the web is (ReviewAsOwner): the same review, recorded the same way
// with the surface it was made on, and whichever of the two comes first is the
// review, by the lock Store.Review takes on the request; the other finds the
// answer no longer in review (ErrConflict).
//
// A click carries no member token, and needs none: who clicked is proved by the
// ticket, made by this ledger for the request's owner and signed, and by the
// clicker, whom the router matched to a member from Slack's own word for who
// clicked, as a consent's click is (Consent). The one thing a token is needed
// for in a review, reading a teamspace again before an answer to a request made
// there is sent, never comes up for a request asked in Slack, which is never
// made in a teamspace; were one to be, its answer would not be sent from Slack.

// ReviewTickets are the tickets for the buttons of one review of a held answer.
type ReviewTickets struct {
	// Send and Withhold are always made; SendMasked only for an answer that has
	// a masked version, and is empty otherwise, since there is nothing to send
	// masked.
	Send, SendMasked, Withhold string
	// ExpiresAt is when they stop working: when the request's review ends.
	ExpiresAt time.Time
}

// ReviewTickets makes the tickets for the buttons of the review reviewID that
// asks the owner of a request asked in Slack to review the answer the guards
// held for them. The request is read here, and its owner and expiry are the
// ledger's: a ticket names no one the request does not. Only an answer to a
// request asked in Slack (ErrInvalid) that is still in review (ErrConflict), and
// whose request has not expired (ErrExpired), has buttons.
func (service *Service) ReviewTickets(ctx context.Context, requestID, reviewID string) (ReviewTickets, error) {
	if service.tickets == nil {
		return ReviewTickets{}, fmt.Errorf("%w: no ticket key is set", ErrConsentUnavailable)
	}
	request, err := service.repository.Get(ctx, strings.TrimSpace(requestID))
	if err != nil {
		return ReviewTickets{}, err
	}
	switch {
	case request.Origin != OriginSlack:
		return ReviewTickets{}, fmt.Errorf("%w: only the answer to a request asked in Slack is reviewed there", ErrInvalid)
	case request.Status != StatusReview || request.Held == nil:
		return ReviewTickets{}, ErrConflict
	case !request.ExpiresAt.After(service.now()):
		return ReviewTickets{}, ErrExpired
	}
	made := ReviewTickets{ExpiresAt: request.ExpiresAt.UTC()}
	type ticketFor struct {
		action string
		into   *string
	}
	buttons := []ticketFor{{ReviewSend, &made.Send}, {ReviewWithhold, &made.Withhold}}
	if request.Held.MaskedResult != nil {
		buttons = append(buttons, ticketFor{ReviewSendMasked, &made.SendMasked})
	}
	for _, button := range buttons {
		ticket, err := service.tickets.Mint(TicketClaims{
			RequestID: request.ID, OwnerUserID: request.Target.UserID, NoticeID: reviewID,
			Kind: TicketReview, Choice: button.action, ExpiresAt: request.ExpiresAt,
		})
		if err != nil {
			return ReviewTickets{}, err
		}
		*button.into = ticket
	}
	return made, nil
}

// ReviewFromSlack records the review a click in Slack made: ticket is the value
// of the button that was clicked, and clickerUserID the member Slack's user was
// matched to. It reviews only when the ticket is a review ticket this ledger
// made and has not run out (ErrTicketInvalid, ErrTicketExpired: a consent's
// ticket reviews nothing), the clicker is the member the ticket was made for and
// the request is addressed to (ErrForbidden), and the request was asked in Slack
// (ErrInvalid). What it then records is the review ReviewAsOwner records, by
// the same rules (review): the answer must still be in review, which a review
// made first on either surface ends (ErrConflict), before its request expires
// (ErrExpired), and only an answer with a masked version is sent masked
// (ErrConflict). The action is the ticket's, never the caller's, and the review
// says it was made in Slack.
func (service *Service) ReviewFromSlack(ctx context.Context, ticket, clickerUserID string) (agents.Interaction, error) {
	if service.tickets == nil {
		return agents.Interaction{}, fmt.Errorf("%w: no ticket key is set", ErrConsentUnavailable)
	}
	claims, err := service.tickets.Verify(ticket, service.now().UTC())
	if err != nil {
		return agents.Interaction{}, err
	}
	if claims.Kind != TicketReview {
		return agents.Interaction{}, fmt.Errorf("%w: it is not a review ticket", ErrTicketInvalid)
	}
	if strings.TrimSpace(clickerUserID) == "" || clickerUserID != claims.OwnerUserID {
		return agents.Interaction{}, fmt.Errorf("%w: this ticket is for another member", ErrForbidden)
	}
	request, err := service.repository.Get(ctx, claims.RequestID)
	if err != nil {
		return agents.Interaction{}, err
	}
	switch {
	case request.Target.UserID != claims.OwnerUserID:
		return agents.Interaction{}, fmt.Errorf("%w: this ticket is not for this request's owner", ErrForbidden)
	case request.Origin != OriginSlack:
		return agents.Interaction{}, fmt.Errorf("%w: only the answer to a request asked in Slack is reviewed there", ErrInvalid)
	}
	decider := agents.InteractionDecider{Kind: DecidedByMember, UserID: claims.OwnerUserID, Surface: SurfaceSlack}
	reviewed, err := service.review(ctx, request, claims.Choice, decider, "")
	if err != nil {
		return agents.Interaction{}, err
	}
	service.logger.InfoContext(ctx, "an owner reviewed a held answer from Slack",
		"request_id", request.ID, "ask_id", request.AskID, "action", claims.Choice, "status", reviewed.Status)
	return reviewed, nil
}
