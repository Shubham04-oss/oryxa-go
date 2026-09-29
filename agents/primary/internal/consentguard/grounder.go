package consentguard

import (
	"context"
	"math"
)

// Grounder says how well an answer stands on what the run behind it read,
// whether it answers the question it was asked, and whether it is about a topic
// that is never let out. *BedrockGrounder is one, on Amazon Bedrock Guardrails'
// contextual grounding check and denied topics.
//
// An error means it could not tell, and the answer is held. Its message is
// logged: it must say why without quoting anything it was given. The Grounding
// that comes with an error may still name the topics it found before it failed.
type Grounder interface {
	Ground(ctx context.Context, in GroundingInput) (Grounding, error)
}

// GrounderFunc is a Grounder that is a function.
type GrounderFunc func(context.Context, GroundingInput) (Grounding, error)

// Ground implements Grounder.
func (ground GrounderFunc) Ground(ctx context.Context, in GroundingInput) (Grounding, error) {
	return ground(ctx, in)
}

// GroundingInput is what a Grounder reads.
type GroundingInput struct {
	// Sources are what the run's tools returned, one text for each result, the
	// oldest first. They are what the answer must stand on.
	Sources []string
	// Question is what the asker wrote, which the answer must answer.
	Question string
	// Answer is what is to be read: the answer's texts, as they would be shown.
	Answer string
}

// Grounding is what a Grounder found.
type Grounding struct {
	// Grounded is how well the sources support the answer. An answer that
	// says something they do not is ungrounded, whether or not it is true.
	Grounded Score
	// Relevant is how well the answer answers the question.
	Relevant Score
	// Topics are the names of the denied topics the answer is about, none of
	// them anything the answer said.
	Topics []string
}

// Score is one of a Grounder's findings on a scale from 0 to 1.
type Score struct {
	// Value is the score: 1 is as good as it gets.
	Value float64
	// Threshold is the lowest score the Grounder's own configuration accepts,
	// or 0 when it has none.
	Threshold float64
	// Failed is whether the Grounder itself found the score wanting, by its own
	// configuration.
	Failed bool
}

// below reports whether the score is lower than the answer needs to be: lower
// than the guard's minimum or than the Grounder's own threshold, whichever asks
// more, or a score the Grounder itself found wanting. A score, or a threshold,
// that is not a number is below every minimum.
func (score Score) below(minimum float64) bool {
	return score.Failed || !(score.Value >= score.needs(minimum))
}

// needs is the lowest score that passes: the greater of the guard's minimum and
// the Grounder's threshold. It is not a number when the threshold is not, which
// no score reaches.
func (score Score) needs(minimum float64) float64 {
	return math.Max(score.Threshold, minimum)
}
