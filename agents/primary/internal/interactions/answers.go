package interactions

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/agents"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/answerguard"
)

// Every answer another member will read is read first by the answer guard,
// when there is one: an answer in which it finds nothing is delivered as
// given; one with only the kinds that are always masked is delivered as the
// guardrail masked it; and one with a kind held for review (an email
// address, a phone number, an address), or one it could not check, waits in
// review for the addressed member, who sends it as it was held, sends its
// fully masked version, or withholds it. A request that expires in review is
// withheld by Oryxa.

const (
	// StatusReview is an answer held for the addressed member's review. The
	// member who asked sees the status and no result. It is not terminal:
	// the member's review, or the request's expiry, ends it.
	StatusReview = "review"
	// StatusWithheld is a request whose answer its addressed member, or its
	// expiry, withheld: the member who asked never reads it.
	StatusWithheld = "withheld"

	// The addressed member's review of a held answer.
	ReviewSend       = "send"
	ReviewSendMasked = "send_masked"
	ReviewWithhold   = "withhold"

	// ExpiredBeforeReview is the reason Oryxa records when it withholds an
	// answer whose request expired while it waited in review.
	ExpiredBeforeReview = "expired before review"
)

// Guard checks the texts of an answer another member will read, before they
// read it. *answerguard.Guard implements it; it never fails, holding what it
// cannot check.
type Guard interface {
	Check(ctx context.Context, texts []string) answerguard.Verdict
}

// Answer is a response as the ledger records it, once the guard has read
// it: delivered (Result) or held (Held), one or the other.
type Answer struct {
	// Result is what the member who asked reads: the response as given, or
	// as the guard masked it. Nil while it is held.
	Result *agents.InteractionResult
	// Held is the response the guard held for the addressed member's review,
	// in the two versions they may send; nil unless held.
	Held *agents.InteractionHeld
	// Guard is what the guard found; nil when no guard read the response.
	Guard *agents.InteractionGuard
}

// Unguarded is a response recorded as given, as it is where no guard reads
// answers.
func Unguarded(input agents.InteractionResponseInput) Answer {
	result := resultOf(input)
	return Answer{Result: &result}
}

// status is the status an answer leaves its request in: its result's, or
// review while it is held.
func (answer Answer) status() (string, error) {
	switch {
	case answer.Result != nil && answer.Held == nil && (answer.Result.Status == StatusCompleted || answer.Result.Status == StatusFailed):
		return answer.Result.Status, nil
	case answer.Result == nil && answer.Held != nil && (answer.Held.Result.Status == StatusCompleted || answer.Held.Result.Status == StatusFailed):
		return StatusReview, nil
	default:
		return "", fmt.Errorf("%w: an answer is completed or failed, and delivered or held, one or the other", ErrInvalid)
	}
}

func resultOf(input agents.InteractionResponseInput) agents.InteractionResult {
	return agents.InteractionResult{
		Status: input.Status, Summary: input.Summary, Content: input.Content,
		ToolsUsed: nonNilStrings(input.ToolsUsed), Artifacts: nonNilStrings(input.Artifacts),
	}
}

// answer is what the ledger records for a response to request. Without a
// guard it is the response as given. Otherwise the guard reads every text of
// it the member who asked would: the summary, the content and the artifact
// references (tools_used is left out: it can only name tools the approval
// allowed, from the requester's own vocabulary). The guard never fails: an
// answer it cannot check is held, and so is one this code cannot make sense
// of the verdict on.
func (service *Service) answer(ctx context.Context, request agents.Interaction, input agents.InteractionResponseInput) Answer {
	result := resultOf(input)
	if service.guard == nil || request.Requester.UserID == request.Target.UserID {
		return Answer{Result: &result}
	}
	texts := answerTexts(result)
	verdict := service.guard.Check(ctx, texts)
	if len(verdict.Texts) != len(texts) || verdict.Masked != nil && len(verdict.Masked) != len(texts) ||
		verdict.State != answerguard.StateClear && verdict.State != answerguard.StateMasked && verdict.State != answerguard.StateHeld {
		verdict = answerguard.Verdict{State: answerguard.StateHeld, Kinds: []string{answerguard.Unchecked}, Texts: texts}
	}
	// What it found is logged by kind, never by value.
	service.logger.InfoContext(ctx, "the answer guard read an answer", "request_id", request.ID, "state", verdict.State, "kinds", verdict.Kinds)
	guard := &agents.InteractionGuard{State: verdict.State, Kinds: verdict.Kinds}
	switch verdict.State {
	case answerguard.StateClear:
		return Answer{Result: &result, Guard: guard}
	case answerguard.StateMasked:
		masked := withTexts(result, verdict.Texts)
		return Answer{Result: &masked, Guard: guard}
	}
	held := &agents.InteractionHeld{Result: withTexts(result, verdict.Texts)}
	if verdict.Masked != nil {
		fully := withTexts(result, verdict.Masked)
		held.MaskedResult = &fully
	}
	return Answer{Held: held, Guard: guard}
}

// answerTexts are the texts of a result the member who asked reads: its
// summary, its content and its artifact references, one to a line (each is
// one line: cleanList).
func answerTexts(result agents.InteractionResult) []string {
	return []string{result.Summary, result.Content, strings.Join(result.Artifacts, "\n")}
}

// withTexts is result with its texts as the guard gave them back.
func withTexts(result agents.InteractionResult, texts []string) agents.InteractionResult {
	result.Summary, result.Content = texts[0], texts[1]
	result.Artifacts = []string{}
	for _, line := range strings.Split(texts[2], "\n") {
		if line = strings.TrimSpace(line); line != "" {
			result.Artifacts = append(result.Artifacts, line)
		}
	}
	return result
}

// ReviewAsOwner records the addressed member's review of an answer the guard
// held for them: ReviewSend delivers it as it was held, ReviewSendMasked its
// fully masked version, and either completes the request (or fails it, when
// the answer held was a failure); ReviewWithhold ends it withheld, with no
// answer delivered. Only the member's own signed-in session reviews, exactly
// as only it decides as the owner (DecideAsOwner): the actor must be marked
// agents.SessionMember, which only the HTTP API's member path does, and
// agents' tools see agents.InteractionService, which has no such method. It
// reviews only a request in review that has not expired.
//
// Sending the answer to a request made in a teamspace reads the teamspace
// again first, with token, the member's own, as approving it did
// (ownerMayApprove): an answer whose request no longer stands there, the
// member who asked having left its team say, is withheld by Oryxa with the
// reason instead, and the member told so (ErrClosed). Withholding needs
// nothing to remain.
func (service *Service) ReviewAsOwner(ctx context.Context, actor agents.InteractionActor, token, id string, input agents.InteractionReviewInput) (agents.Interaction, error) {
	if actor.Session != agents.SessionMember {
		return agents.Interaction{}, fmt.Errorf("%w: only the member, in their own signed-in session, reviews an answer held for them", ErrForbidden)
	}
	request, err := service.Get(ctx, actor, id)
	if err != nil {
		return agents.Interaction{}, err
	}
	if request.Target.UserID != actor.UserID {
		return agents.Interaction{}, ErrForbidden
	}
	action := strings.ToLower(strings.TrimSpace(input.Action))
	if action != ReviewSend && action != ReviewSendMasked && action != ReviewWithhold {
		return agents.Interaction{}, fmt.Errorf("%w: action must be send, send_masked or withhold", ErrInvalid)
	}
	now := service.now().UTC()
	switch {
	case !request.ExpiresAt.After(now):
		// An answer still in review past its request's expiry reads as
		// withheld already (view), and is no longer the member's to send.
		return agents.Interaction{}, ErrExpired
	case request.Status != StatusReview:
		return agents.Interaction{}, fmt.Errorf("%w: only an answer held for review is reviewed", ErrConflict)
	case action == ReviewSendMasked && (request.Held == nil || request.Held.MaskedResult == nil):
		return agents.Interaction{}, fmt.Errorf("%w: this answer could not be checked, so it has no masked version to send; send it as it is, or withhold it", ErrConflict)
	}
	if request.TeamspaceID != "" && action != ReviewWithhold {
		if err := service.mayStillSend(ctx, request, token); err != nil {
			return agents.Interaction{}, err
		}
	}
	return service.viewed(actor)(service.repository.Review(ctx, request.ID, agents.InteractionReview{
		Action: action, DecidedBy: agents.InteractionDecider{Kind: DecidedByMember, UserID: actor.UserID},
	}, now))
}

// mayStillSend reads again, with the member's own token, whether a request
// made in a teamspace still stands there before its held answer is sent;
// one that no longer does has its answer withheld by Oryxa, saying why.
func (service *Service) mayStillSend(ctx context.Context, request agents.Interaction, token string) error {
	if service.teamspaces == nil {
		return fmt.Errorf("%w: requests inside a teamspace are not available on this server", ErrInvalid)
	}
	if strings.TrimSpace(token) == "" {
		return fmt.Errorf("%w: member token is required to send the answer to a request made in a teamspace", ErrInvalid)
	}
	found, err := service.standingOf(ctx, request, token)
	if err != nil {
		return err
	}
	if found.lapsed == "" {
		return nil
	}
	if _, err := service.repository.Review(ctx, request.ID, agents.InteractionReview{
		Action: ReviewWithhold, DecidedBy: agents.InteractionDecider{Kind: DecidedBySystem}, Reason: withheldBecause(found.lapsed),
	}, service.now().UTC()); err != nil {
		return err
	}
	return fmt.Errorf("%w: %s, so Oryxa withheld the answer", ErrClosed, found.lapsed)
}

// withheldBecause is what an answer Oryxa withheld says, as its review's
// reason, when its request no longer stands in its teamspace.
func withheldBecause(lapsed string) string {
	return "Oryxa withheld this answer because " + lapsed + "."
}

// viewFor is request as actor may read it. The member who asked, and their
// agents, never read what the guard holds, and read what it found only
// beside an answer they read: a request in review or withheld shows them its
// status, and its review, alone. The addressed member reads it all in their
// own signed-in session. Their agents read that an answer is held, and what
// for (guard), but not the held texts, which only the member reviews: so no
// run carries them into a model call, its thread or a trace.
func (service *Service) viewFor(actor agents.InteractionActor, request agents.Interaction) agents.Interaction {
	request = service.view(request)
	if actsFor(actor, request.Target) {
		if actor.Session != agents.SessionMember {
			request.Held = nil
		}
		return request
	}
	request.Held = nil
	if request.Result == nil || request.Status == StatusReview || request.Status == StatusWithheld {
		request.Result, request.Guard = nil, nil
	}
	return request
}

// viewed is a ledger change's result as actor may read it (viewFor), for
// returning what a change recorded to whoever made it.
func (service *Service) viewed(actor agents.InteractionActor) func(agents.Interaction, error) (agents.Interaction, error) {
	return func(request agents.Interaction, err error) (agents.Interaction, error) {
		if err != nil {
			return agents.Interaction{}, err
		}
		return service.viewFor(actor, request), nil
	}
}

// expiredReviews withholds answers whose request expired in review.
// Store implements it.
type expiredReviews interface {
	WithholdExpired(ctx context.Context, reason string, at time.Time, limit int) ([]agents.Interaction, error)
}

// expiredReviewsPerSweep bounds how many answers one sweep withholds; the
// rest wait for the next.
const expiredReviewsPerSweep = 20

// SweepExpiredReviews withholds, every interval until ctx ends, the answers
// whose request expired while they waited in review, as Oryxa and saying
// so. The interaction waker's poll does the same where the events queue
// runs it; without the queue there is no waker, and this is what ends such a
// request instead, and what tells the member who asked (its event).
func SweepExpiredReviews(ctx context.Context, reviews expiredReviews, interval time.Duration, logger *slog.Logger) {
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		withheld, err := reviews.WithholdExpired(ctx, ExpiredBeforeReview, time.Now().UTC(), expiredReviewsPerSweep)
		if err != nil && ctx.Err() == nil {
			logger.ErrorContext(ctx, "withhold answers whose request expired in review", "error", err)
		}
		for _, request := range withheld {
			logger.WarnContext(ctx, "withheld an answer whose request expired before its member reviewed it", "request_id", request.ID)
		}
	}
}
