// Package jev is a small client for TypeSafe's System One API: the service
// behind Jev, which sorts a caller's own state into the options it is given,
// with calibrated probabilities, in well under a second and for a fraction
// of what a language model turn costs.
//
// A single [Request] can carry several independent [Question]s over the
// same [Request.State]. TypeSafe evaluates them in parallel — none can see
// another's answer — which is the point: a caller with more than one
// judgment to make about the same situation asks for all of them in one
// round trip rather than paying for several turns. Combining the answers
// into a decision is the caller's own policy; this package only asks Jev
// and parses what it says back.
//
// Only the [TypeChoice] and [TypeNoul] primitives are supported, since
// nothing in this codebase has yet needed Score. See
// https://docs.typesafe.ai/primitives for the concepts behind each one, and
// how to write instructions and criteria that read well to the model.
//
// A message, a document, or anything else read from outside the caller's
// own code belongs in Request.State, never folded into Instructions: State
// is data Jev is shown, Instructions and Criteria are the policy applied to
// it, and only data can safely carry text nobody here wrote.
package jev

// Type is a System One question primitive.
type Type string

const (
	// TypeChoice picks one option from Criteria and returns a probability
	// distribution over every option it was given.
	TypeChoice Type = "choice"
	// TypeNoul answers a yes/no question with a single probability of yes.
	TypeNoul Type = "noul"
)

// Question is one independent judgment asked in a [Request]. Every Question
// in a Request is evaluated against the same State, in parallel with every
// other Question in it — so word Instructions as a self-contained judgment;
// it cannot refer to what another question decided.
type Question struct {
	Type Type
	// Instructions is the judgment to make, in plain language.
	Instructions string
	// Criteria defines the possible answers.
	//
	// For [TypeChoice] it is a map of option name to a description of that
	// option: a plain string for an option unlikely to be confused with
	// another, or — better whenever two options could be mistaken for one
	// another — a struct such as [Option] spelling out what the option
	// covers, what belongs elsewhere instead, and a couple of concrete
	// examples. Up to 255 options; include a catch-all such as "other" or
	// "nobody" whenever the given options might not cover everything.
	//
	// For [TypeNoul] it is optional: nil lets Instructions alone carry the
	// question, or a map with "true" and "false" keys can clarify what each
	// side means.
	//
	// Marshaled to JSON exactly as given; the caller's own field names are
	// what the model reads, so name them for a reader rather than for code.
	Criteria any
}

// Option describes one answer a Choice question may pick, in the structured
// form TypeSafe recommends whenever an option could be confused with
// another: what it covers, what belongs somewhere else instead, and a
// couple of concrete examples. Jev reads all of it literally — an Option is
// exactly as good as its wording.
type Option struct {
	What     string   `json:"what"`
	NotFor   string   `json:"not_for,omitempty"`
	Examples []string `json:"examples,omitempty"`
}

// Request is one call to Jev: a batch of independent [Question]s asked over
// the same State.
type Request struct {
	// State is what Jev is shown: a string, or — recommended whenever it
	// has more than one part — an object, so each part keeps its own name
	// and its relationship to the rest stays clear. Marshaled as given.
	State any
	// Questions are asked together in one HTTP call and evaluated in
	// parallel: this is what makes several questions cost one round trip.
	// Keyed by the caller's own question ID, echoed back in
	// [Result.Answers]; IDs are for code and are never shown to the model,
	// so they carry no meaning of their own.
	Questions map[string]Question
}

// Answer is Jev's raw judgment for one [Question]: a distribution and a
// choice for [TypeChoice], or a single probability for [TypeNoul]. Turning
// an Answer into a decision — what confidence is high enough to act on,
// what counts as close enough, what to do when nothing is confident — is
// the caller's policy. This package hands back what Jev said, never what to
// make of it.
type Answer struct {
	Type Type
	// Choice is the option a Choice question picked. Empty for Noul.
	Choice string
	// Probabilities is the full distribution a Choice question returned
	// over every option it was given, summing to (approximately) 1. Nil for
	// Noul, which carries its probability in Noul instead.
	Probabilities map[string]float64
	// Confidence is how concentrated Probabilities is, from 0 (spread
	// evenly across the options) to 1 (all of it on Choice). Zero for Noul.
	Confidence float64
	// Noul is the probability of yes, from 0 to 1. Zero for Choice.
	Noul float64
}

// Result is Jev's answer to a [Request].
type Result struct {
	// Model is the exact model version that answered, so a change in
	// behaviour can be attributed to a change in model rather than guessed
	// at.
	Model string
	// InputTokens is what Jev read to answer every question in the
	// request, for cost accounting.
	InputTokens int
	// Answers is keyed by the same question IDs the [Request] used, with
	// exactly one entry per question asked.
	Answers map[string]Answer
}
