package answerguard

import (
	"context"
	"log/slog"
	"math"
	"reflect"
	"slices"

	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/relevance"
)

// OffTopic is the kind a verdict carries when the relevance check held the
// answer: it carried private material its request did not ask for, or could
// not be checked. It comes with the categories of what the answer carried,
// named as relevance names them ("health", "hr", "personal_life", and so on),
// and with Unchecked when the judge could not tell. It is never a value.
const OffTopic = "OFF_TOPIC"

// The modes of a RelevanceInput, spelled as the deployment and each
// organization's policy spell them, so that this package need not know either.
const (
	// RelevanceHold holds an answer the judge would hold.
	RelevanceHold = "hold"
	// RelevanceShadow runs the judge and records what it would have held,
	// and never holds an answer for it.
	RelevanceShadow = "shadow"
	// RelevanceOff does not run the judge.
	RelevanceOff = "off"
)

// RelevanceInput is what CheckWith is told about an answer for the relevance
// check: what was asked of the agent that gave it, where its material may
// have come from, and what the check is to do with what it finds.
type RelevanceInput struct {
	// Request is what was asked, in the asker's words. It is the one thing
	// that says what the answer may carry, and is shown to the judge, which
	// reads at most relevance.MaxRequestRunes of it.
	Request string
	// Context is up to relevance.MaxContextLines lines of the conversation
	// around the request, each of at most relevance.MaxContextLineRunes. It
	// helps the judge read the request and asks for nothing.
	Context []string
	// Sources are the names of the tools the run called, from what the run
	// recorded (never from what the agent says it called), plus
	// relevance.MemoryPreload and relevance.ThreadHistory when the run had
	// those. relevance.NeedsJudging reads them: an answer no source may have
	// read anything private for is not judged, and costs no model call. With
	// none, nothing is judged.
	Sources []string
	// Regulated is set for an organization on the regulated preset, whose
	// answers are held for carrying health, HR or pay details even when the
	// request asked for them (relevance.ShouldHold).
	Regulated bool
	// Mode is what to do with what the judge finds: RelevanceHold,
	// RelevanceShadow or RelevanceOff. A mode this code does not know is
	// RelevanceHold, so a mistyped one never checks less than it was asked to.
	Mode string

	// OrganizationID and UserID are the member the check is for, and are
	// only ever logged beside what the check decided, so that a line can be
	// found by whose answer it was. Either may be empty.
	OrganizationID, UserID string
}

// RelevanceOutcome is what the relevance check made of an answer. It carries
// decisions and category names, never a text.
type RelevanceOutcome struct {
	// Judged is whether a judge read the answer: false when the mode was
	// off, no judge is configured, or no source may have read anything
	// private. It is true for a judge that was asked and could not tell.
	Judged bool
	// WouldHold is whether the check holds the answer, by relevance.ShouldHold
	// and the judge's own word: for any extra, for a judge that could not tell,
	// and, in a regulated organization, for a request that asked for health,
	// HR or pay. It is recorded in shadow mode too, where nothing is held.
	WouldHold bool
	// Held is whether the check did hold the answer: WouldHold, in hold mode.
	// The verdict is then StateHeld with OffTopic among its Kinds, and shares
	// nothing (Verdict.Shareable, Verdict.At).
	Held bool
	// Extras are the private categories the answer carried that its request
	// did not ask for, as relevance names them, in relevance.Categories'
	// order; nil when nothing was judged.
	Extras []string
	// Certainty is the judge's own confidence, 0 to 1. It is logged for
	// tuning and decides nothing.
	Certainty float64
	// Err is why the judge could not tell, as relevance.ErrKind names it
	// ("timeout", "unavailable", "blocked", ...), or "" when it could.
	Err string
}

// Option changes how New builds a guard.
type Option func(*Guard)

// WithRelevance gives the guard a judge for the relevance check that
// CheckWith runs. Without one, or with nil, the check does not run whatever
// a RelevanceInput asks: there is nobody to ask.
func WithRelevance(judge relevance.Judge) Option {
	return func(guard *Guard) {
		if isNothing(judge) {
			guard.judge = nil
			return
		}
		guard.judge = judge
	}
}

// isNothing reports whether judge is nil, or a nil pointer, map, slice or func
// in the interface, which would read as a judge and panic when asked: the way
// one that was never built gets past a nil check.
func isNothing(judge relevance.Judge) bool {
	if judge == nil {
		return true
	}
	switch value := reflect.ValueOf(judge); value.Kind() {
	case reflect.Pointer, reflect.Map, reflect.Slice, reflect.Func, reflect.Chan:
		return value.IsNil()
	}
	return false
}

// CheckWith is Check, and then the relevance check when rel asks for it: the
// personal data read runs first and is never changed by what follows, except
// that a hold for relevance holds the answer whatever the read found.
//
// With a nil rel it is exactly Check, and the verdict's Relevance is nil.
// Otherwise Relevance is set, zero unless a judge ran, and the judge runs only
// when rel.Mode is not RelevanceOff, the guard was given one (WithRelevance),
// and relevance.NeedsJudging says a source may have read something private.
//
//   - In RelevanceHold, an answer the judge would hold (Relevance.WouldHold:
//     any extra, a judge that could not tell, or, when regulated, health, HR or
//     pay asked for) is held: StateHeld, with OffTopic, the extras' category
//     names and, when the judge could not tell, Unchecked among its Kinds, beside
//     any kinds the read held it for. It shares nothing at any Intensity, and
//     never a masked version of an answer that is off topic.
//   - In RelevanceShadow the verdict is what the read made it, and Relevance
//     records what the judge would have done.
//
// The judge is handed the texts as they were given, so that it judges what the
// agent said. One decision is logged for each answer a judge read, by what was
// decided and never by a text.
func (guard *Guard) CheckWith(ctx context.Context, texts []string, rel *RelevanceInput) Verdict {
	verdict := guard.Check(ctx, texts)
	if rel == nil {
		return verdict
	}
	return guard.checkRelevance(ctx, texts, verdict, rel)
}

func (guard *Guard) checkRelevance(ctx context.Context, texts []string, verdict Verdict, rel *RelevanceInput) Verdict {
	outcome := &RelevanceOutcome{}
	verdict.Relevance = outcome
	mode := relevanceMode(rel.Mode)
	if mode == RelevanceOff || guard.judge == nil || !relevance.NeedsJudging(rel.Sources) {
		return verdict
	}

	result := guard.judge.Judge(ctx, relevance.Input{
		Request:   rel.Request,
		Context:   rel.Context,
		Answer:    slices.Clone(texts),
		Sources:   rel.Sources,
		Regulated: rel.Regulated,
	})
	failed := result.Err != nil
	outcome.Judged = result.Judged || failed
	// The rule is applied here as well as by the judge: a judge that came
	// back without its Hold set, or with an error and no hold, cannot loosen
	// it, and one that holds for a reason of its own is believed.
	outcome.WouldHold = failed || result.Hold || relevance.ShouldHold(rel.Regulated, result.Asked, result.Extras, result.Err)
	outcome.Extras = categoryNames(result.Extras)
	outcome.Certainty = certainty(result.Certainty)
	if failed {
		outcome.Err = relevance.ErrKind(result.Err)
	}

	if mode == RelevanceHold && outcome.WouldHold {
		verdict = holdForRelevance(verdict, outcome, failed)
	}
	if outcome.Judged {
		guard.logRelevance(ctx, rel, mode, result, outcome)
	}
	return verdict
}

// relevanceMode is mode as the check reads it: shadow and off as they are, and
// hold for that and for anything this code does not know.
func relevanceMode(mode string) string {
	switch mode {
	case RelevanceOff, RelevanceShadow:
		return mode
	default:
		return RelevanceHold
	}
}

// holdForRelevance is verdict held for the relevance check: whatever the read
// made of it, it waits for its owner, and nothing of it is shared. What it was
// held for is what the read held it for, if it did, and OffTopic beside the
// extras' categories, and Unchecked when the judge could not tell. The texts,
// the masked version and the kinds always masked are the read's, kept for the
// owner who reviews it; sharing them is Shareable's and At's to refuse.
func holdForRelevance(verdict Verdict, outcome *RelevanceOutcome, failed bool) Verdict {
	kinds := append([]string{OffTopic}, outcome.Extras...)
	if failed {
		kinds = append(kinds, Unchecked)
	}
	if verdict.State == StateHeld {
		kinds = append(kinds, verdict.Kinds...)
	}
	verdict.State = StateHeld
	verdict.Kinds = sortedUnique(kinds)
	outcome.Held = true
	return verdict
}

// heldForRelevance reports whether the relevance check held the answer, which
// makes every way of sharing it refuse.
func (verdict Verdict) heldForRelevance() bool {
	return verdict.Relevance != nil && verdict.Relevance.Held
}

// relevanceHeld is what a verdict held for relevance comes to at any
// intensity: held, for what it names and always for OffTopic, and sharing
// nothing, whatever the read found and whatever masked version it made.
func (verdict Verdict) relevanceHeld() Outcome {
	return Outcome{State: StateHeld, Kinds: sortedUnique(append(slices.Clone(verdict.Kinds), OffTopic))}
}

// logRelevance records what the check decided for one answer a judge read: the
// judge's own fields (relevance.LogFields, which name no text) with the mode,
// whether the answer was held for it, and whose answer it was when the caller
// said. A judge that could not tell is a warning, as a guardrail that could
// not is.
func (guard *Guard) logRelevance(ctx context.Context, rel *RelevanceInput, mode string, result relevance.Result, outcome *RelevanceOutcome) {
	fields := append(relevance.LogFields(result), "mode", mode, "held", outcome.Held)
	if rel.OrganizationID != "" {
		fields = append(fields, "organization_id", rel.OrganizationID)
	}
	if rel.UserID != "" {
		fields = append(fields, "user_id", rel.UserID)
	}
	level := slog.LevelInfo
	if result.Err != nil {
		level = slog.LevelWarn
	}
	guard.logger.Log(ctx, level, "the answer guard's relevance check read an answer", fields...)
}

// categoryNames are the names of categories, as relevance spells them.
func categoryNames(categories []relevance.Category) []string {
	names := make([]string, len(categories))
	for index, category := range categories {
		names[index] = string(category)
	}
	return names
}

// certainty is a judge's confidence within 0 and 1, and 0 for one that is not
// a number, so that an outcome can always be encoded.
func certainty(value float64) float64 {
	if math.IsNaN(value) {
		return 0
	}
	return min(max(value, 0), 1)
}
