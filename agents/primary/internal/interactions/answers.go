package interactions

import (
	"context"
	"fmt"
	"log/slog"
	"reflect"
	"slices"
	"strings"
	"time"

	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/agents"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/answerguard"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/consentguard"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/guardpolicy"
)

// Every answer another member will read is read first by the answer guard,
// when there is one, at the level its owner reads answers at: an answer in
// which it finds nothing is delivered as given; one with only the kinds that
// are always masked is delivered as the guardrail masked it; and one with a
// kind held for review (an email address, a phone number, an address), one it
// could not check, or one the relevance check held for carrying private
// material its request did not ask for, waits in review for the addressed
// member, who sends it as it was held, sends its fully masked version (which
// an answer held for relevance has none of), or withholds it. A request that
// expires in review is withheld by Oryxa.
//
// The answer to a request made from Slack is read a second time, by the consent
// guard, when there is one: it is going into a channel, and it must keep to what
// its owner allowed (a summary, or excerpts), stand on what its run read, and
// carry no credential. What it holds waits in the same review; what it finds
// only to mask is delivered masked.

const (
	// ReviewWindow starts when completed work is held. Consent expiry bounds
	// execution; it must not consume the member's time to inspect the answer.
	ReviewWindow = 24 * time.Hour

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
// read it, for personal data and, as it is told to, for whether they carry
// private material their request did not ask for. *answerguard.Guard
// implements it; it never fails, holding what it cannot check.
type Guard interface {
	CheckWith(ctx context.Context, texts []string, relevance *answerguard.RelevanceInput) answerguard.Verdict
}

// ConsentGuard reads the answer to a request made from Slack, once the answer
// guard has, before the member who asked reads it there: whether it keeps to
// how far its owner let it go, stands on what its run read, is about no topic
// that is never let out, and has no credential in it. *consentguard.Guard
// implements it; it never fails, holding what it cannot check.
type ConsentGuard interface {
	Check(ctx context.Context, input consentguard.Input) consentguard.Result
}

// isNoConsentGuard reports whether guard is nil, or a nil pointer, map, slice or
// func in the interface, which would read as a guard and panic when asked: the
// way a guard that was never built gets past a nil check.
func isNoConsentGuard(guard ConsentGuard) bool {
	if guard == nil {
		return true
	}
	switch value := reflect.ValueOf(guard); value.Kind() {
	case reflect.Pointer, reflect.Map, reflect.Slice, reflect.Func, reflect.Chan:
		return value.IsNil()
	}
	return false
}

// GuardPolicy says how the answer guard treats the answers a member gives:
// how strictly it reads them, and what its relevance check does with them,
// once the organization's minimum and the deployment's ceiling are counted in.
// *guardpolicy.Service implements it.
type GuardPolicy interface {
	// Effective fails only when the member's own levels cannot be read, and
	// then it is still the strictest reading, so a caller that has to go on
	// goes on strictly.
	Effective(ctx context.Context, organizationID, userID string) (guardpolicy.Effective, error)
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

// answer is what the ledger records for a response to request: what the answer
// guard makes of it (guardedAnswer) and, for a request made from Slack, what the
// consent guard makes of that (consentRead). actor is who is answering: the
// member themself in their own session, or an agent in a run.
func (service *Service) answer(ctx context.Context, actor agents.InteractionActor, request agents.Interaction, input agents.InteractionResponseInput) Answer {
	answer := service.guardedAnswer(ctx, request, input)
	if service.consentGuard == nil || request.Origin != OriginSlack || request.Requester.UserID == request.Target.UserID {
		return answer
	}
	return service.consentRead(ctx, actor, request, answer)
}

// guardedAnswer is what the answer guard makes of a response to request. Without
// a guard it is the response as given. Otherwise the guard reads every text of
// it the member who asked would: the summary, the content and the artifact
// references (tools_used is left out: it can only name tools the approval
// allowed, from the requester's own vocabulary). The guard never fails: an
// answer it cannot check is held, and so is one this code cannot make sense
// of the verdict on.
//
// What is done with what the guard found follows the level its owner reads
// answers at for another member (answerSettings): at the strictest, a held kind
// holds the answer for its owner's review; at the lower level only the kinds
// that are always masked are taken out, and the rest is delivered as written.
// An answer the relevance check held is held whatever the level, and has no
// masked version to send. The relevance check is told the request in its
// asker's words, and the tools the run called, from the run's own record
// (agents.RunSources); an answer that does not come from a run has none, and is
// not judged.
func (service *Service) guardedAnswer(ctx context.Context, request agents.Interaction, input agents.InteractionResponseInput) Answer {
	result := resultOf(input)
	if service.guard == nil || request.Requester.UserID == request.Target.UserID {
		return Answer{Result: &result}
	}
	texts := answerTexts(result)
	settings := service.answerSettings(ctx, request)
	verdict := service.guard.CheckWith(ctx, texts, &answerguard.RelevanceInput{
		Request: request.Task, Sources: agents.RunSources(ctx),
		Regulated: settings.regulated, Mode: settings.relevance,
		OrganizationID: request.OrganizationID, UserID: request.Target.UserID,
	})
	if len(verdict.Texts) != len(texts) || verdict.Masked != nil && len(verdict.Masked) != len(texts) ||
		verdict.State != answerguard.StateClear && verdict.State != answerguard.StateMasked && verdict.State != answerguard.StateHeld {
		verdict = answerguard.Verdict{State: answerguard.StateHeld, Kinds: []string{answerguard.Unchecked}, Texts: texts}
	}
	outcome := verdict.At(len(texts), settings.intensity)
	// What it found is logged by kind, never by value.
	relevanceHeld := verdict.Relevance != nil && verdict.Relevance.Held
	service.logger.InfoContext(ctx, "the answer guard read an answer", "request_id", request.ID, "state", outcome.State, "kinds", outcome.Kinds,
		"intensity", settings.intensity, "relevance_held", relevanceHeld)
	guard := &agents.InteractionGuard{State: outcome.State, Kinds: outcome.Kinds}
	// Clear, or with only what is always masked taken out at this level, the
	// answer is delivered: as it was given when nothing was taken out, and
	// otherwise as the texts that may be shared.
	if outcome.State != answerguard.StateHeld && outcome.Shareable != nil {
		delivered := result
		if !slices.Equal(outcome.Shareable, texts) {
			delivered = withTexts(result, outcome.Shareable)
		}
		return Answer{Result: &delivered, Guard: guard}
	}
	held := &agents.InteractionHeld{Result: withTexts(result, verdict.Texts)}
	if verdict.Masked != nil && !relevanceHeld {
		fully := withTexts(result, verdict.Masked)
		held.MaskedResult = &fully
	}
	return Answer{Held: held, Guard: guard}
}

// consentRead is answer as the consent guard leaves it. It reads the texts the
// answer guard left to be shown, or, for an answer held already, to be reviewed,
// with what the run's tools returned (agents.RunResults, from the run's own
// record and not from anything the answer says), the request in its asker's
// words, and how far the owner let the answer go:
//
//   - clear, it is as it was;
//   - with only credentials found, it is delivered with them masked;
//   - held, it waits for its owner's review with the credentials masked, whatever
//     the answer guard made of it, and with the kinds it was held for beside its
//     own.
//
// The fully masked version an answer guard's hold has is stripped of credentials
// as well, since Slack is where the owner's sending it ends. A guard that does
// not give back a text for each text it was given cannot be read, and holds the
// answer as unchecked.
func (service *Service) consentRead(ctx context.Context, actor agents.InteractionActor, request agents.Interaction, answer Answer) Answer {
	var shown agents.InteractionResult
	switch {
	case answer.Result != nil:
		shown = *answer.Result
	case answer.Held != nil:
		shown = answer.Held.Result
	default:
		return answer
	}
	texts := answerTexts(shown)
	checked := service.consentGuard.Check(ctx, consentguard.Input{
		Level:          consentLevel(request),
		Question:       request.Task,
		Texts:          texts,
		Sources:        consentSources(agents.RunResults(ctx)),
		Failed:         shown.Status == StatusFailed,
		WrittenByOwner: actor.Session == agents.SessionMember,
		RequestID:      request.ID,
		OrganizationID: request.OrganizationID,
	})
	if len(checked.Texts) != len(texts) {
		stripped, masked := consentguard.Strip(texts)
		checked = consentguard.Result{
			Action: consentguard.ActionHold, Texts: stripped, Masked: masked,
			Reasons: []consentguard.Reason{{Kind: consentguard.KindUnchecked, Detail: "the consent guard did not give back every text"}},
		}
	}
	guard := consentGuardRecord(answer.Guard, checked)
	if checked.Action == consentguard.ActionHold || answer.Held != nil {
		held := &agents.InteractionHeld{Result: withTexts(shown, checked.Texts)}
		if answer.Held != nil && answer.Held.MaskedResult != nil {
			stripped, _ := consentguard.Strip(answerTexts(*answer.Held.MaskedResult))
			fully := withTexts(*answer.Held.MaskedResult, stripped)
			held.MaskedResult = &fully
		}
		return Answer{Held: held, Guard: guard}
	}
	delivered := *answer.Result
	if !slices.Equal(checked.Texts, texts) {
		delivered = withTexts(delivered, checked.Texts)
	}
	return Answer{Result: &delivered, Guard: guard}
}

// consentLevel is how far an owner let the answer to request go: as far as
// excerpts only when they said so, and a summary in every other case, so that no
// consent at all is the stricter one.
func consentLevel(request agents.Interaction) consentguard.Level {
	if consentOf(request) == ConsentAccess {
		return consentguard.LevelAccess
	}
	return consentguard.LevelSummary
}

// consentSources are what a run's tools returned, as the consent guard reads it.
func consentSources(results []agents.ToolResult) []consentguard.Source {
	sources := make([]consentguard.Source, len(results))
	for index, result := range results {
		sources[index] = consentguard.Source{Tool: result.Tool, Parts: result.Parts}
	}
	return sources
}

// consentGuardRecord is what the ledger records the answer guard found in an
// answer, and the consent guard beside it: held when either held it, masked when
// either masked something and neither held it, and the kinds of both. It is
// what the answer guard recorded, and none, when the consent guard found nothing.
func consentGuardRecord(previous *agents.InteractionGuard, checked consentguard.Result) *agents.InteractionGuard {
	kinds := checked.Kinds()
	if len(kinds) == 0 {
		return previous
	}
	state := answerguard.StateMasked
	if checked.Action == consentguard.ActionHold {
		state = answerguard.StateHeld
	}
	if previous != nil {
		kinds = append(kinds, previous.Kinds...)
		if previous.State == answerguard.StateHeld {
			state = answerguard.StateHeld
		}
	}
	slices.Sort(kinds)
	return &agents.InteractionGuard{State: state, Kinds: slices.Compact(kinds)}
}

// answerSettings are how the guard reads one answer: the level its owner reads
// answers another member reads at, what its relevance check does with what it
// finds, and whether the organization is regulated.
type answerSettings struct {
	intensity answerguard.Intensity
	relevance string
	regulated bool
}

// answerSettings reads them for the owner of request. Where no policy is kept
// the answer is read as strictly as can be and the relevance check is off, as
// the check route reads where none are kept. Where the owner's own levels
// cannot be read, Effective answers the strictest reading with its error: the
// answer is read with that, and the log says so, by who and why, never with a
// text.
func (service *Service) answerSettings(ctx context.Context, request agents.Interaction) answerSettings {
	if service.guardPolicy == nil {
		return answerSettings{intensity: answerguard.IntensityStrict, relevance: answerguard.RelevanceOff}
	}
	effective, err := service.guardPolicy.Effective(ctx, request.OrganizationID, request.Target.UserID)
	if err != nil {
		service.logger.WarnContext(ctx, "an answer was read as strictly as it can be, since its owner's answer guard settings could not be read",
			"request_id", request.ID, "organization_id", request.OrganizationID, "user_id", request.Target.UserID, "error", err)
	}
	return answerSettings{
		intensity: intensityOf(effective.For(guardpolicy.OriginPeer)),
		relevance: relevanceModeOf(effective.Relevance),
		regulated: effective.Regulated,
	}
}

// intensityOf is the intensity that reads at a level. It is only ever secrets
// or strict: a level nobody knows, or none, is strict, so an answer is never
// shared with the kinds that are always masked left in it.
func intensityOf(level guardpolicy.Level) answerguard.Intensity {
	if level == guardpolicy.LevelSecrets {
		return answerguard.IntensitySecrets
	}
	return answerguard.IntensityStrict
}

// relevanceModeOf is the mode the guard is asked to check relevance in. It is
// only ever hold, shadow or off: a mode nobody knows, or none, is hold, so a
// check never does less than the settings could have asked of it.
func relevanceModeOf(mode guardpolicy.RelevanceMode) string {
	switch mode {
	case guardpolicy.RelevanceOff:
		return answerguard.RelevanceOff
	case guardpolicy.RelevanceShadow:
		return answerguard.RelevanceShadow
	default:
		return answerguard.RelevanceHold
	}
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
