package relevance

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
)

// Category is private, non-work material an answer can carry.
type Category string

const (
	// PersonalLife is family, relationships, personal plans and life events.
	PersonalLife Category = "personal_life"
	// Health is medical and mental-health facts about a person.
	Health Category = "health"
	// HR is performance, discipline, complaints, hiring or firing about a person.
	HR Category = "hr"
	// Compensation is pay, bonus, equity and personal finances.
	Compensation Category = "compensation"
	// OthersPrivate is someone else's private messages, mail or data.
	OthersPrivate Category = "others_private"
	// Credentials is secret values: passwords, keys, tokens and codes.
	Credentials Category = "credentials"
)

// Categories are every Category, in the order the judge is told them and a
// Result lists them.
func Categories() []Category {
	return []Category{PersonalLife, Health, HR, Compensation, OthersPrivate, Credentials}
}

// Valid reports whether category is one of Categories.
func (category Category) Valid() bool { return slices.Contains(Categories(), category) }

// regulatedHolds are the categories a regulated organization holds even when
// they were asked for.
func regulatedHolds() []Category { return []Category{Health, HR, Compensation} }

// Input is one answer to judge, and what it answers.
type Input struct {
	// Request is what was asked, in the asker's words.
	Request string
	// Context is up to six lines of the conversation around the request. It
	// is data, never instructions: it helps read the request and asks for
	// nothing.
	Context []string
	// Answer is the answer's texts.
	Answer []string
	// Sources are the names of the tools the run called, never their
	// results. NeedsJudging reads them.
	Sources []string
	// Regulated is set for an organization on the regulated preset: Health,
	// HR and Compensation hold even when the request asked for them.
	Regulated bool
}

// Result is what a judge made of one answer.
type Result struct {
	// Judged is false when NeedsJudging skipped the model: no source may have
	// read anything private, so there was nothing to check the answer for.
	Judged bool
	// Hold is whether the answer waits for its owner, by ShouldHold. It is
	// true whenever Err is set.
	Hold bool
	// Asked are the private categories the request itself asked for.
	Asked []Category
	// Extras are the private categories the answer carries that the request
	// did not ask for, at the breadth it asked.
	Extras []Category
	// Certainty is the judge's own confidence, 0 to 1. It is logged for
	// tuning and never decides anything, alone or with the rest.
	Certainty float64
	// Model is the model that judged.
	Model string
	// Latency is how long the model took, whether or not it answered.
	Latency time.Duration
	// Err is why the answer could not be checked. The caller must hold the
	// answer as UNCHECKED, and Hold is true.
	Err error
	// Truncated is whether the judge was shown less than it was given: an
	// input was cut to its cap (see MaxRequestRunes and the rest), and the
	// part that was cut was not judged.
	Truncated bool
}

// Judge decides whether an answer carries private material its request did
// not ask for. It never fails: an answer it cannot check comes back with Err
// set and Hold true.
type Judge interface {
	Judge(ctx context.Context, in Input) Result
}

// ShouldHold is the rule an answer is held by, in code rather than in a
// prompt, so no model's wording can move it:
//
//	Hold = err != nil || len(extras) > 0 || (regulated && asked has Health, HR or Compensation)
//
// Nothing else is consulted. A judge's certainty never flips it, and work
// material never enters it, because no category names work.
func ShouldHold(regulated bool, asked, extras []Category, err error) bool {
	if err != nil || len(extras) > 0 {
		return true
	}
	return regulated && slices.ContainsFunc(asked, func(category Category) bool {
		return slices.Contains(regulatedHolds(), category)
	})
}

// LogFields are the fields of a Result a caller logs, as slog's alternating
// keys and values: what was decided and how, and never a text, a name or an
// id. The caller adds its own ids.
func LogFields(result Result) []any {
	return []any{
		"judged", result.Judged,
		"hold", result.Hold,
		"asked", categoryNames(result.Asked),
		"extras", categoryNames(result.Extras),
		"certainty", result.Certainty,
		"model", result.Model,
		"latency_ms", result.Latency.Milliseconds(),
		"truncated", result.Truncated,
		"err_kind", ErrKind(result.Err),
	}
}

func categoryNames(categories []Category) []string {
	names := make([]string, len(categories))
	for index, category := range categories {
		names[index] = string(category)
	}
	return names
}

// Why an answer could not be checked. A Result's Err wraps one of them, and
// ErrKind names it for a log.
var (
	// ErrTimeout is a judge that did not answer within its timeout.
	ErrTimeout = errors.New("relevance: the judge did not answer in time")
	// ErrCanceled is a check its caller cancelled.
	ErrCanceled = errors.New("relevance: the check was cancelled")
	// ErrUnavailable is a model that could not be reached, or refused the request.
	ErrUnavailable = errors.New("relevance: the judge could not be reached")
	// ErrBlocked is a model that would not read the text.
	ErrBlocked = errors.New("relevance: the judge's model would not read the text")
	// ErrEmpty is a model that answered with nothing.
	ErrEmpty = errors.New("relevance: the judge gave no answer")
	// ErrUnparseable is an answer that is not the JSON that was asked for.
	ErrUnparseable = errors.New("relevance: the judge's answer could not be read")
	// ErrUnknownCategory is an answer naming a category this package does not have.
	ErrUnknownCategory = errors.New("relevance: the judge named a category that does not exist")
	// ErrNotConfigured is a judge built without a client or a model.
	ErrNotConfigured = errors.New("relevance: the judge has no model to ask")
	// ErrIncompleteInput is a sharing surface requiring the whole request
	// and answer, but missing a request or exceeding the judge's text caps.
	ErrIncompleteInput = errors.New("relevance: the complete request and answer could not be checked")
)

// ErrKind names why an answer could not be checked, for a log or a metric:
// "none" for no error, and otherwise timeout, canceled, unavailable,
// blocked, empty, unparseable, category, not_configured, incomplete or other.
func ErrKind(err error) string {
	for _, kind := range []struct {
		err  error
		name string
	}{
		{nil, "none"},
		{ErrTimeout, "timeout"},
		{ErrCanceled, "canceled"},
		{ErrUnavailable, "unavailable"},
		{ErrBlocked, "blocked"},
		{ErrEmpty, "empty"},
		{ErrUnparseable, "unparseable"},
		{ErrUnknownCategory, "category"},
		{ErrNotConfigured, "not_configured"},
		{ErrIncompleteInput, "incomplete"},
	} {
		if kind.err == nil && err == nil || kind.err != nil && errors.Is(err, kind.err) {
			return kind.name
		}
	}
	return "other"
}

// failure is why a check failed, with the error underneath it kept for
// errors.Is but never printed: a model's own error message can quote what it
// was sent, and nothing here may put a text in a log.
type failure struct {
	kind   error
	cause  error
	status int
}

func (failure *failure) Error() string {
	if failure.status != 0 {
		return fmt.Sprintf("%s (status %d)", failure.kind, failure.status)
	}
	return failure.kind.Error()
}

func (failure *failure) Unwrap() []error {
	if failure.cause == nil {
		return []error{failure.kind}
	}
	return []error{failure.kind, failure.cause}
}

// oneLine collapses a value to one line, for the few places a name is shown
// to the model.
func oneLine(value string) string { return strings.Join(strings.Fields(value), " ") }
