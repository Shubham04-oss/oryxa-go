package consentguard

import (
	"context"
	"fmt"
	"log/slog"
	"reflect"
	"slices"
	"strings"
	"time"
)

// Level is how far the owner let an answer go.
type Level string

const (
	// LevelSummary is an AI summary alone (the owner's consent:summary): no run
	// of CopyRun or more words copied from what the run read, and no links.
	LevelSummary Level = "summary"
	// LevelAccess is excerpts as well (consent:access, which a lease's follow-ups
	// and a bypass carry too): what the run read may be quoted.
	LevelAccess Level = "access"
)

// The kinds a held answer is held for, beside the kinds of credential that
// were masked (KindSlackToken and the rest). They are what the ledger records
// as the answer's guard kinds, so they are spelled as answerguard spells its
// own: in capitals, and never a value.
const (
	// KindCopied is a summary that copies CopyRun or more words in a row from
	// what the run read.
	KindCopied = "COPIED_FROM_SOURCE"
	// KindLink is a summary with a link.
	KindLink = "LINK"
	// KindNoSources is an answer from a run that read nothing to stand on.
	KindNoSources = "NO_SOURCES"
	// KindUngrounded is an answer that says something that what the run read
	// does not support.
	KindUngrounded = "UNGROUNDED"
	// KindIrrelevant is an answer that does not answer the question.
	KindIrrelevant = "IRRELEVANT"
	// KindDeniedTopic is an answer about a topic that is never let out.
	KindDeniedTopic = "DENIED_TOPIC"
	// KindUnchecked is an answer that could not be checked, which is the same
	// word answerguard uses for one it could not read.
	KindUnchecked = "UNCHECKED"
)

const (
	// DefaultMinGrounding is the lowest grounding score an answer may have.
	DefaultMinGrounding = 0.75
	// DefaultMinRelevance is the lowest relevance score an answer may have.
	DefaultMinRelevance = 0.5
	// DefaultTimeout bounds asking the Grounder. Contextual grounding reads the
	// whole of what a run read, which takes longer than the answer guard's read
	// of a text.
	DefaultTimeout = 10 * time.Second
)

// Source is what one tool returned to the run an answer comes from.
type Source struct {
	// Tool is the tool's name. It is only ever logged.
	Tool string
	// Parts are what it returned, one text for each value in the result. A run
	// of words copied from a source is looked for within one part, since two
	// values were never in a sentence together.
	Parts []string
}

// Input is what Check is told about one answer.
type Input struct {
	// Level is how far the owner let the answer go. Anything but LevelAccess
	// is read as LevelSummary, so that no level is the stricter one.
	Level Level
	// Question is what the asker wrote, in their words: what the answer has to
	// answer.
	Question string
	// Texts are the answer's texts, as they would be delivered. The result has
	// as many, in the same order.
	Texts []string
	// Sources are what the run's tools returned, which the answer has to stand
	// on. From the run's own record, never from what the answer says it read.
	// With none, an answer is held (unless it says a run failed, or its owner
	// wrote it).
	Sources []Source
	// Failed is an answer that reports a failure: it claims nothing about what
	// the run read, so it needs no sources to stand on and is not read for
	// grounding. It is still read for credentials, and, at LevelSummary, for a
	// copy of the sources and for links.
	Failed bool
	// WrittenByOwner is an answer the owner wrote themself, in their own
	// session, with no run behind it: there is nothing for it to stand on and it
	// is their own words. Only credentials are taken out of it.
	WrittenByOwner bool

	// RequestID and OrganizationID say whose answer this is, for the one line
	// logged about it. Either may be empty.
	RequestID, OrganizationID string
}

// Action is what Check decided.
type Action string

const (
	// ActionPass is an answer nothing was found in: it is delivered as it was.
	ActionPass Action = "pass"
	// ActionMask is an answer with only credentials in it, which are masked in
	// Result.Texts: it is delivered as they are.
	ActionMask Action = "mask"
	// ActionHold is an answer that waits for its owner's review, the texts of
	// which are Result.Texts, and for which it is held are Result.Reasons.
	ActionHold Action = "hold"
)

// Reason is why an answer is held.
type Reason struct {
	// Kind is a short code, one of the Kind constants.
	Kind string
	// Detail says it in words and numbers, for the log. It never holds a text
	// of the answer, of what the run read, or of the question.
	Detail string
}

// Result is what Check decided about an answer's texts.
type Result struct {
	Action Action
	// Texts are the answer's texts with every credential masked: as they may be
	// delivered for ActionPass and ActionMask, and as they are kept for the
	// owner's review for ActionHold. Never fewer than were given.
	Texts []string
	// Masked are the kinds of credential that were masked, sorted, never a
	// value.
	Masked []string
	// Reasons are why an answer is held, in the order they were found; none
	// unless the Action is ActionHold.
	Reasons []Reason
}

// Kinds are the kinds the ledger records for the result: the credentials
// masked and what it was held for, sorted, with none twice.
func (result Result) Kinds() []string {
	kinds := slices.Clone(result.Masked)
	for _, reason := range result.Reasons {
		kinds = append(kinds, reason.Kind)
	}
	slices.Sort(kinds)
	return slices.Compact(kinds)
}

// Config sets how a guard reads.
type Config struct {
	// MinGrounding and MinRelevance are the lowest grounding and relevance
	// scores an answer may have, above 0 and no more than 1. A zero, or a
	// number outside that, is DefaultMinGrounding or DefaultMinRelevance. A
	// Grounder's own thresholds count as well, and the higher one wins.
	MinGrounding, MinRelevance float64
	// Timeout bounds asking the Grounder; DefaultTimeout when zero.
	Timeout time.Duration
	// Logger records what was decided, by kind and by score, never by text;
	// discarded when nil.
	Logger *slog.Logger
}

// Guard reads the answers an owner's agent gives a question asked in Slack,
// before the asker reads them there. It is built with New, holds no state and
// is safe for concurrent use.
type Guard struct {
	grounder     Grounder
	minGrounding float64
	minRelevance float64
	timeout      time.Duration
	logger       *slog.Logger
}

// New builds a guard that asks grounder about every answer that has sources to
// stand on. With a nil grounder, no answer can be checked for grounding, so
// each one that needs it is held as unchecked: the guard fails closed rather
// than deliver what it could not read.
func New(grounder Grounder, config Config) *Guard {
	guard := &Guard{
		grounder: grounder, minGrounding: config.MinGrounding, minRelevance: config.MinRelevance,
		timeout: config.Timeout, logger: config.Logger,
	}
	if isNothing(grounder) {
		guard.grounder = nil
	}
	if guard.minGrounding <= 0 || guard.minGrounding > 1 {
		guard.minGrounding = DefaultMinGrounding
	}
	if guard.minRelevance <= 0 || guard.minRelevance > 1 {
		guard.minRelevance = DefaultMinRelevance
	}
	if guard.timeout <= 0 {
		guard.timeout = DefaultTimeout
	}
	if guard.logger == nil {
		guard.logger = slog.New(slog.DiscardHandler)
	}
	return guard
}

// isNothing reports whether grounder is nil, or a nil pointer, map, slice or
// func in the interface, which would read as a grounder and panic when asked.
func isNothing(grounder Grounder) bool {
	if grounder == nil {
		return true
	}
	switch value := reflect.ValueOf(grounder); value.Kind() {
	case reflect.Pointer, reflect.Map, reflect.Slice, reflect.Func, reflect.Chan:
		return value.IsNil()
	}
	return false
}

// Check reads an answer. It never fails: what it cannot check it holds.
//
// Credentials come out of every text first, whatever else is found: Slack's
// tokens and the fixed shapes of other keys, beside the kinds Bedrock masks
// (which the answer guard has already read for). Then:
//
//   - at LevelSummary, a run of CopyRun or more words copied from what the run
//     read, and a link, each hold the answer;
//   - an answer that is not a failure needs something to stand on: with no
//     source, it is held, and otherwise the Grounder reads it against them, and
//     holds it for a grounding or relevance score below what is asked, for a
//     denied topic, and for a Grounder that could not tell.
//
// The Grounder is not asked about an answer that is held already, nor one with
// nothing to stand on: it could add nothing.
func (guard *Guard) Check(ctx context.Context, in Input) Result {
	texts, masked := Strip(in.Texts)
	result := Result{Action: ActionPass, Texts: texts, Masked: masked}
	if len(masked) > 0 {
		result.Action = ActionMask
	}
	var grounding *Grounding
	if !in.WrittenByOwner && anyText(texts) {
		if in.Level != LevelAccess {
			result.Reasons = append(result.Reasons, summaryRule(texts, in.Sources)...)
		}
		if !in.Failed {
			var reasons []Reason
			reasons, grounding = guard.stand(ctx, in, texts, len(result.Reasons) > 0)
			result.Reasons = append(result.Reasons, reasons...)
		}
	}
	if len(result.Reasons) > 0 {
		result.Action = ActionHold
	}
	guard.log(ctx, in, result, grounding)
	return result
}

// summaryRule is what holds an answer whose owner let it go as far as a
// summary: no run of CopyRun or more words taken word for word from what the
// run read, and no link.
func summaryRule(texts []string, sources []Source) []Reason {
	var reasons []Reason
	if longest := newCopyIndex(sources).longestCopy(strings.Join(texts, "\n")); longest > 0 {
		reasons = append(reasons, Reason{KindCopied, fmt.Sprintf(
			"a run of %d words is copied from what the run read, and a summary has fewer than %d in a row", longest, CopyRun)})
	}
	if slices.ContainsFunc(texts, hasLink) {
		reasons = append(reasons, Reason{KindLink, "the answer has a link, and a summary has none"})
	}
	return reasons
}

// stand reads whether an answer stands on what the run read, and is about
// nothing that is never let out. held is whether it is held already, in which
// case only a missing source is added. The Grounding is nil when it was not
// asked.
func (guard *Guard) stand(ctx context.Context, in Input, texts []string, held bool) ([]Reason, *Grounding) {
	sources := sourceTexts(in.Sources)
	switch {
	case len(sources) == 0:
		return []Reason{{KindNoSources, "the run read nothing the answer could stand on"}}, nil
	case held:
		return nil, nil
	case strings.TrimSpace(in.Question) == "":
		return []Reason{{KindUnchecked, "there is no question to read the answer against"}}, nil
	case guard.grounder == nil:
		return []Reason{{KindUnchecked, "no grounding check is set up"}}, nil
	}
	asking, cancel := context.WithTimeout(ctx, guard.timeout)
	defer cancel()
	grounding, err := guard.grounder.Ground(asking, GroundingInput{
		Sources: sources, Question: in.Question, Answer: strings.Join(nonBlank(texts), "\n\n"),
	})
	if err != nil {
		return []Reason{{KindUnchecked, "the grounding check could not tell: " + err.Error()}}, nil
	}
	var reasons []Reason
	if grounding.Grounded.below(guard.minGrounding) {
		reasons = append(reasons, Reason{KindUngrounded, fmt.Sprintf(
			"the sources support the answer to a degree of %.2f, and it needs %.2f", grounding.Grounded.Value, grounding.Grounded.needs(guard.minGrounding))})
	}
	if grounding.Relevant.below(guard.minRelevance) {
		reasons = append(reasons, Reason{KindIrrelevant, fmt.Sprintf(
			"the answer answers the question to a degree of %.2f, and it needs %.2f", grounding.Relevant.Value, grounding.Relevant.needs(guard.minRelevance))})
	}
	if len(grounding.Topics) > 0 {
		reasons = append(reasons, Reason{KindDeniedTopic, "the answer is about " + strings.Join(grounding.Topics, ", ")})
	}
	return reasons, &grounding
}

// log records one line for an answer that was read, by what was decided and
// why, and never by any text. An answer that could not be checked is a warning.
func (guard *Guard) log(ctx context.Context, in Input, result Result, grounding *Grounding) {
	fields := []any{
		"level", in.Level, "action", result.Action, "kinds", result.Kinds(), "sources", len(in.Sources),
		"failed", in.Failed, "written_by_owner", in.WrittenByOwner,
	}
	if in.RequestID != "" {
		fields = append(fields, "request_id", in.RequestID)
	}
	if in.OrganizationID != "" {
		fields = append(fields, "organization_id", in.OrganizationID)
	}
	if grounding != nil {
		fields = append(fields, "grounded", grounding.Grounded.Value, "relevant", grounding.Relevant.Value, "topics", grounding.Topics)
	}
	level := slog.LevelInfo
	if len(result.Reasons) > 0 {
		fields = append(fields, "reasons", reasonDetails(result.Reasons))
	}
	if slices.ContainsFunc(result.Reasons, func(reason Reason) bool { return reason.Kind == KindUnchecked }) {
		level = slog.LevelWarn
	}
	guard.logger.Log(ctx, level, "the consent guard read an answer", fields...)
}

func reasonDetails(reasons []Reason) []string {
	details := make([]string, len(reasons))
	for index, reason := range reasons {
		details[index] = reason.Kind + ": " + reason.Detail
	}
	return details
}

// anyText reports whether some text is more than blank.
func anyText(texts []string) bool { return len(nonBlank(texts)) > 0 }

// nonBlank are the texts that are more than blank.
func nonBlank(texts []string) []string {
	return slices.DeleteFunc(slices.Clone(texts), func(text string) bool { return strings.TrimSpace(text) == "" })
}

// sourceTexts are what the run read, one text for each result that has any: its
// parts, a line each. A result that returned nothing is nothing to stand on.
func sourceTexts(sources []Source) []string {
	var found []string
	for _, source := range sources {
		if text := strings.TrimSpace(strings.Join(nonBlank(source.Parts), "\n")); text != "" {
			found = append(found, text)
		}
	}
	return found
}
