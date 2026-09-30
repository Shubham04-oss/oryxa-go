package interactions

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/agents"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/answerguard"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/consentguard"
	"github.com/Shubham04-oss/oryxa-go/internal/cleantext"
)

// "Reply yourself": the member a request is addressed to answers it in their
// own words, rather than letting their agent. Some asks are theirs to answer,
// an approval, a plan, anything their agent has nothing written down for, and
// the reply is the answer the member who asked reads, exactly as an agent's
// answer would reach them: in the Slack thread they asked in, or in the chat
// they asked from.
//
// A reply lands on a request that waits for its owner, which is one of two:
//
//   - pending, waiting for their decision: it is approved and answered in one
//     change (Store.Reply), and their agent, which the approval grants nothing,
//     never runs for it;
//   - in review, an answer of their agent's held for them: the reply replaces
//     it, and the held answer, in either of its versions, is never sent.
//
// It is made in the owner's own signed-in session (ReplyAsOwner), or in Slack,
// in the modal the Reply yourself button of their notice or review opens,
// with the button's reply ticket (ReplyFromSlack). Whichever of a reply, a
// choice and a review comes first wins, by the lock the store takes on the
// request, and the one that comes second finds it decided: ErrAlreadyDecided,
// with the request as the winner left it.
//
// A reply is the owner's own words, so the guards read it for what it may
// carry that is never let out, and for nothing else: the answer guard masks
// the kinds it always masks (secrets, card, bank and government numbers), and
// every credential is taken out (consentguard.Strip). Neither grounding nor
// relevance is asked of it, there being no run for it to stand on, and it is
// never held: it would only be held for the owner who wrote it.

// MaxReplyRunes is the most a reply holds, once trimmed, in characters.
const MaxReplyRunes = maxMessageRunes

// replyDecided is what a pending request's approval says when its owner
// answered it themself, in fixed words: their words are the answer.
const replyDecided = "Answered in their own words."

// ReplyRecord is an owner's reply as the ledger records it.
type ReplyRecord struct {
	// Answer is the reply as the guards left it, which is delivered: a result
	// completed and written by its owner, and nothing held.
	Answer Answer
	// Decider is the owner, and where they replied.
	Decider agents.InteractionDecider
	// Stage is the status the request must still be in for the reply to land:
	// StatusPending or StatusReview, as the ticket of a Slack button says, or
	// empty for either, as a reply on the web is.
	Stage string
}

// valid is whether a reply is one the ledger records: delivered as written by
// its owner, by a member, for a stage a reply lands on.
func (reply ReplyRecord) valid() error {
	result := reply.Answer.Result
	switch {
	case result == nil || reply.Answer.Held != nil || result.Status != StatusCompleted || !result.WrittenByOwner:
		return fmt.Errorf("%w: a reply is its owner's answer, delivered as they wrote it and never held", ErrInvalid)
	case reply.Decider.Kind != DecidedByMember || strings.TrimSpace(reply.Decider.UserID) == "" ||
		reply.Decider.Surface != SurfaceWeb && reply.Decider.Surface != SurfaceSlack:
		return fmt.Errorf("%w: a reply is made by its owner, on the web or in Slack", ErrInvalid)
	case reply.Stage != "" && !validReplyStage(reply.Stage):
		return fmt.Errorf("%w: a reply lands on a request pending or in review", ErrInvalid)
	}
	return nil
}

// validReplyStage is whether status is one a reply lands on, as a reply
// ticket's choice names it.
func validReplyStage(status string) bool {
	return status == StatusPending || status == StatusReview
}

// replyLands is whether a reply made for stage lands on request as the ledger
// holds it: pending or in review, and in stage when one is named.
func replyLands(request agents.Interaction, stage string) bool {
	switch request.Status {
	case StatusPending:
	case StatusReview:
		if request.Held == nil {
			return false
		}
	default:
		return false
	}
	return stage == "" || stage == request.Status
}

// replyDecision is the approval a reply makes of a pending request: nothing
// allowed and nothing granted, since no agent works on it.
func replyDecision() agents.InteractionDecisionInput {
	return agents.InteractionDecisionInput{
		Decision: DecisionApproved, Message: replyDecided, AllowedTools: []string{}, GrantedPermissions: []string{},
	}
}

// RepliedByOwner is whether a request's owner answered it themself (a reply):
// its approval was a reply, or its review was.
func RepliedByOwner(request agents.Interaction) bool {
	return request.Decision != nil && request.Decision.Consent == ConsentReply ||
		request.Review != nil && request.Review.Action == ReviewReply
}

// cleanReply is a reply as it is recorded: trimmed. checkReply refuses one
// that is empty, too long, or holds what PostgreSQL cannot keep.
func cleanReply(text string) string { return strings.TrimSpace(text) }

func checkReply(text string) error {
	if text == "" || utf8.RuneCountInString(text) > MaxReplyRunes {
		return fmt.Errorf("%w: text is required and holds at most %d characters", ErrInvalid, MaxReplyRunes)
	}
	if !cleantext.Valid(text) {
		return fmt.Errorf("%w: text holds a character that cannot be kept", ErrInvalid)
	}
	return nil
}

// ReplyAsOwner records the reply of the member a request is addressed to, made
// in their own signed-in session: the actor must be marked
// agents.SessionMember, which only the HTTP API's member path does, and
// agents' tools see agents.InteractionService, which has no such method. token
// is their own, from that session: a reply to a request made in a teamspace
// reads the teamspace again with it first, as approving it and sending its
// answer do.
//
// It answers the request as they now read it. A request decided, reviewed or
// replied to first, on any surface, is ErrAlreadyDecided, with the request as
// it stands, whose decision and review say who won.
func (service *Service) ReplyAsOwner(ctx context.Context, actor agents.InteractionActor, token, id, text string) (agents.Interaction, error) {
	if actor.Session != agents.SessionMember {
		return agents.Interaction{}, fmt.Errorf("%w: only the member, in their own signed-in session, replies as the owner", ErrForbidden)
	}
	if err := validateActor(actor); err != nil {
		return agents.Interaction{}, err
	}
	text = cleanReply(text)
	if err := checkReply(text); err != nil {
		return agents.Interaction{}, err
	}
	request, err := service.repository.Get(ctx, strings.TrimSpace(id))
	if err != nil {
		return agents.Interaction{}, err
	}
	if request.OrganizationID != actor.OrganizationID || !canRead(actor, request) {
		return agents.Interaction{}, ErrNotFound
	}
	if request.Target.UserID != actor.UserID {
		return agents.Interaction{}, ErrForbidden
	}
	decider := agents.InteractionDecider{Kind: DecidedByMember, UserID: actor.UserID, Surface: SurfaceWeb}
	replied, err := service.reply(ctx, request, text, decider, token, "")
	if err != nil && !errors.Is(err, ErrAlreadyDecided) {
		return agents.Interaction{}, err
	}
	return service.viewFor(actor, replied), err
}

// ReplyFromSlack records the reply an owner sent from the Slack modal the Reply
// yourself button of their notice or review opened: ticket is the button's,
// which the modal carried, clickerUserID the member Slack's user who sent it was
// matched to, and text what they wrote. It replies only when the ticket is a
// reply ticket this ledger made and has not run out (ErrTicketInvalid,
// ErrTicketExpired: a consent's or a review's ticket replies nothing), the
// sender is the member the ticket was made for and the request is addressed to
// (ErrForbidden), and the request was asked in Slack (ErrInvalid). It lands
// only while the request is still in the status the ticket names: pending for
// the notice's button, in review for the review's; otherwise it is
// ErrAlreadyDecided, with the request as it stands. The request is returned as
// the ledger holds it.
func (service *Service) ReplyFromSlack(ctx context.Context, ticket, clickerUserID, text string) (agents.Interaction, error) {
	if service.tickets == nil {
		return agents.Interaction{}, fmt.Errorf("%w: no ticket key is set", ErrConsentUnavailable)
	}
	claims, err := service.tickets.Verify(ticket, service.now().UTC())
	if err != nil {
		return agents.Interaction{}, err
	}
	if claims.Kind != TicketReply {
		return agents.Interaction{}, fmt.Errorf("%w: it is not a reply ticket", ErrTicketInvalid)
	}
	if strings.TrimSpace(clickerUserID) == "" || clickerUserID != claims.OwnerUserID {
		return agents.Interaction{}, fmt.Errorf("%w: this ticket is for another member", ErrForbidden)
	}
	text = cleanReply(text)
	if err := checkReply(text); err != nil {
		return agents.Interaction{}, err
	}
	request, err := service.repository.Get(ctx, claims.RequestID)
	if err != nil {
		return agents.Interaction{}, err
	}
	switch {
	case request.Target.UserID != claims.OwnerUserID:
		return agents.Interaction{}, fmt.Errorf("%w: this ticket is not for this request's owner", ErrForbidden)
	case request.Origin != OriginSlack:
		return agents.Interaction{}, fmt.Errorf("%w: only a request asked in Slack is replied to there", ErrInvalid)
	}
	decider := agents.InteractionDecider{Kind: DecidedByMember, UserID: claims.OwnerUserID, Surface: SurfaceSlack}
	return service.reply(ctx, request, text, decider, "", claims.Choice)
}

// reply records a reply of the request's owner, once who is replying is proved
// to be them: by their session (ReplyAsOwner) or a reply ticket of theirs
// (ReplyFromSlack). stage is the status the reply was made for, or empty for
// either.
//
// A reply to a request made in a teamspace reads the teamspace again first,
// with token, the member's own, as approving it (ownerMayApprove) and sending
// its held answer (mayStillSend) do: one that no longer stands there is closed,
// or its held answer withheld, by Oryxa instead (ErrClosed). Without a token,
// which a reply from Slack never carries, such a request is not replied to
// (ErrInvalid); a request asked in Slack is never made in a teamspace.
func (service *Service) reply(ctx context.Context, request agents.Interaction, text string, decider agents.InteractionDecider, token, stage string) (agents.Interaction, error) {
	switch {
	case !replyLands(request, stage):
		return request, ErrAlreadyDecided
	case !request.ExpiresAt.After(service.now()):
		return request, ErrExpired
	}
	if request.TeamspaceID != "" {
		if strings.TrimSpace(token) == "" {
			return request, fmt.Errorf("%w: member token is required to reply to a request made in a teamspace", ErrInvalid)
		}
		var err error
		if request.Status == StatusPending {
			err = service.ownerMayApprove(ctx, request, token)
		} else {
			err = service.mayStillSend(ctx, request, token)
		}
		if err != nil {
			return request, err
		}
	}
	stored, err := service.repository.Reply(ctx, request.ID, ReplyRecord{
		Answer: service.ownerAnswer(ctx, request, text), Decider: decider, Stage: stage,
	}, service.now().UTC())
	if errors.Is(err, ErrConflict) {
		// Decided, reviewed or replied to while this call was reading it: the
		// request as it stands is the winner's.
		current, getErr := service.repository.Get(ctx, request.ID)
		if getErr != nil {
			return request, getErr
		}
		return current, ErrAlreadyDecided
	}
	if err != nil {
		return request, err
	}
	service.logger.InfoContext(ctx, "an owner replied to a request themself",
		"request_id", request.ID, "ask_id", request.AskID, "was", request.Status, "surface", decider.Surface,
		"guard", guardState(stored.Guard))
	return stored, nil
}

// guardState is what a guard record says, for a log line: its state, or none.
func guardState(guard *agents.InteractionGuard) string {
	if guard == nil {
		return "none"
	}
	return guard.State
}

// ownerAnswer is an owner's reply as the ledger records it: completed, written
// by its owner, and read by the guards for what is never let out alone.
//
// The answer guard, when there is one and the reply is for another member,
// reads it at IntensitySecrets: the kinds it always masks are masked, and what
// it only holds for an agent's answer, an email address or a phone number the
// owner chose to give, is left as they wrote it. It is not asked about
// relevance: the owner is the one who knows what was asked of them. A reply it
// could not read is delivered all the same, since holding it would only hold it
// for the one who wrote it, and the log says so. Then every credential is
// taken out of it (consentguard.Strip), whether or not a guard read it.
//
// What was masked is recorded as the answer's guard, as it is for every answer
// a guard read: masked, with the kinds; clear when the answer guard read it and
// nothing was; and none when nothing read it and nothing was masked.
func (service *Service) ownerAnswer(ctx context.Context, request agents.Interaction, text string) Answer {
	result := agents.InteractionResult{
		Status: StatusCompleted, Summary: text, Content: "", ToolsUsed: []string{}, Artifacts: []string{}, WrittenByOwner: true,
	}
	texts := answerTexts(result)
	var guard *agents.InteractionGuard
	if service.guard != nil && request.Requester.UserID != request.Target.UserID {
		verdict := service.guard.CheckWith(ctx, texts, nil)
		outcome := verdict.At(len(texts), answerguard.IntensitySecrets)
		switch {
		case verdict.State != answerguard.StateClear && verdict.State != answerguard.StateMasked && verdict.State != answerguard.StateHeld,
			outcome.Shareable == nil || len(outcome.Shareable) != len(texts):
			service.logger.WarnContext(ctx, "the answer guard could not read an owner's reply; it is delivered with its credentials taken out",
				"request_id", request.ID, "kinds", verdict.Kinds)
		default:
			texts = outcome.Shareable
			guard = &agents.InteractionGuard{State: outcome.State, Kinds: outcome.Kinds}
		}
	}
	stripped, credentials := consentguard.Strip(texts)
	if len(credentials) > 0 {
		kinds := append([]string{}, credentials...)
		if guard != nil {
			kinds = append(kinds, guard.Kinds...)
		}
		slices.Sort(kinds)
		guard = &agents.InteractionGuard{State: answerguard.StateMasked, Kinds: slices.Compact(kinds)}
	}
	result = withTexts(result, stripped)
	return Answer{Result: &result, Guard: guard}
}
