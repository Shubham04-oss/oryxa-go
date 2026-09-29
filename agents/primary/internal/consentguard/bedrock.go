package consentguard

import (
	"context"
	"errors"
	"math"
	"slices"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime/types"
	"github.com/aws/smithy-go"
)

// What Amazon Bedrock Guardrails takes for its contextual grounding check, in
// characters: 100,000 for the grounding source, 1,000 for the query and 5,000
// for the response (docs.aws.amazon.com/bedrock/latest/userguide/
// guardrails-contextual-grounding-check.html). An answer is read in pieces
// below the last, with room to spare should a character count differently.
const (
	maxSourceChars = 100_000
	maxQueryChars  = 1_000
	chunkChars     = 4_000
	// maxChunks is how many pieces one answer may be read in: at 4,000
	// characters, an answer of 32,000, more than the ledger takes of one.
	maxChunks = 8
)

// BedrockClient is Bedrock's runtime; *bedrockruntime.Client is one.
type BedrockClient interface {
	ApplyGuardrail(context.Context, *bedrockruntime.ApplyGuardrailInput, ...func(*bedrockruntime.Options)) (*bedrockruntime.ApplyGuardrailOutput, error)
}

// BedrockGrounder asks one guardrail, through ApplyGuardrail, how well an
// answer is grounded in what the run read and whether it answers the question,
// and whether it is about a denied topic. The guardrail is set up for it
// (deploy/production/answer_guard.tf): a contextual grounding policy with both
// its filters, and the topics that are never let out. It is not the answer
// guard's guardrail, which is called with no grounding source and would answer
// none of this.
type BedrockGrounder struct {
	client     BedrockClient
	identifier string
	version    string
}

// NewBedrockGrounder builds a grounder for the guardrail named by identifier,
// its id or ARN, and version, a number. It contacts nothing until it is asked.
func NewBedrockGrounder(client BedrockClient, identifier, version string) (*BedrockGrounder, error) {
	identifier, version = strings.TrimSpace(identifier), strings.TrimSpace(version)
	if client == nil || identifier == "" || version == "" {
		return nil, errors.New("consentguard: a Bedrock client, a guardrail identifier and a version are required")
	}
	return &BedrockGrounder{client: client, identifier: identifier, version: version}, nil
}

// Ground reads the answer in pieces of at most chunkChars, each against the
// same sources and question. The answer stands on its sources as well as its
// worst piece does, is about a denied topic if any piece is, and answers the
// question as well as its best piece does, which is how Bedrock reads a
// response's own chunks for relevance.
//
// It says why it could not tell without repeating anything it was sent:
// Bedrock's own messages can quote a text.
func (grounder *BedrockGrounder) Ground(ctx context.Context, in GroundingInput) (Grounding, error) {
	source := fitSources(in.Sources, maxSourceChars)
	if source == "" {
		return Grounding{}, errors.New("there is no source text to read the answer against")
	}
	query := clip(strings.TrimSpace(in.Question), maxQueryChars)
	if query == "" {
		return Grounding{}, errors.New("there is no question to read the answer against")
	}
	pieces := chunks(in.Answer, chunkChars)
	switch {
	case len(pieces) == 0:
		return Grounding{}, errors.New("there is no answer to read")
	case len(pieces) > maxChunks:
		return Grounding{}, errors.New("the answer is too long to read")
	}

	readings := make([]Grounding, len(pieces))
	failures := make([]error, len(pieces))
	var wait sync.WaitGroup
	for index, piece := range pieces {
		wait.Go(func() { readings[index], failures[index] = grounder.read(ctx, source, query, piece) })
	}
	wait.Wait()
	for _, failure := range failures {
		if failure != nil {
			return Grounding{}, failure
		}
	}
	return combine(readings), nil
}

// read asks the guardrail about one piece of the answer.
func (grounder *BedrockGrounder) read(ctx context.Context, source, query, piece string) (Grounding, error) {
	output, err := grounder.client.ApplyGuardrail(ctx, &bedrockruntime.ApplyGuardrailInput{
		GuardrailIdentifier: aws.String(grounder.identifier),
		GuardrailVersion:    aws.String(grounder.version),
		Source:              types.GuardrailContentSourceOutput,
		// Without the full scope the response lists only what was detected, which
		// leaves a filter that found nothing out of it, and a score with it.
		OutputScope: types.GuardrailOutputScopeFull,
		Content: []types.GuardrailContentBlock{
			textBlock(source, types.GuardrailContentQualifierGroundingSource),
			textBlock(query, types.GuardrailContentQualifierQuery),
			textBlock(piece, types.GuardrailContentQualifierGuardContent),
		},
	})
	if err != nil {
		return Grounding{}, callFailure(err)
	}
	return readGrounding(output)
}

func textBlock(text string, qualifier types.GuardrailContentQualifier) types.GuardrailContentBlock {
	return &types.GuardrailContentBlockMemberText{Value: types.GuardrailTextBlock{
		Text: aws.String(text), Qualifiers: []types.GuardrailContentQualifier{qualifier},
	}}
}

// callFailure says why a call failed without repeating its message: a timeout,
// a cancellation, or the error code Bedrock answered with.
func callFailure(err error) error {
	var api smithy.APIError
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return errors.New("the guardrail did not answer in time")
	case errors.Is(err, context.Canceled):
		return errors.New("the check was cancelled")
	case errors.As(err, &api):
		return errors.New("the guardrail answered " + api.ErrorCode())
	default:
		return errors.New("the guardrail could not be reached")
	}
}

// readGrounding is what one response says of a piece: the score of each of the
// two contextual grounding filters, and the denied topics detected. A response
// that does not score both is one the guardrail was not set up to give, and
// says nothing of the answer.
func readGrounding(output *bedrockruntime.ApplyGuardrailOutput) (Grounding, error) {
	if output == nil {
		return Grounding{}, errors.New("the guardrail returned nothing")
	}
	var grounded, relevant *Score
	var topics []string
	for _, assessment := range output.Assessments {
		if policy := assessment.ContextualGroundingPolicy; policy != nil {
			for _, filter := range policy.Filters {
				score, err := scoreOf(filter)
				if err != nil {
					return Grounding{}, err
				}
				switch filter.Type {
				case types.GuardrailContextualGroundingFilterTypeGrounding:
					grounded = lowest(grounded, score)
				case types.GuardrailContextualGroundingFilterTypeRelevance:
					relevant = lowest(relevant, score)
				}
			}
		}
		if policy := assessment.TopicPolicy; policy != nil {
			for _, topic := range policy.Topics {
				if topic.Detected == nil || *topic.Detected {
					topics = append(topics, aws.ToString(topic.Name))
				}
			}
		}
	}
	if grounded == nil || relevant == nil {
		return Grounding{}, errors.New("the guardrail did not score both grounding and relevance")
	}
	slices.Sort(topics)
	return Grounding{Grounded: *grounded, Relevant: *relevant, Topics: slices.Compact(topics)}, nil
}

// scoreOf is one filter's finding. A filter with no score or threshold, or one
// that is not a number, says nothing.
func scoreOf(filter types.GuardrailContextualGroundingFilter) (Score, error) {
	if filter.Score == nil || filter.Threshold == nil || math.IsNaN(*filter.Score) || math.IsNaN(*filter.Threshold) {
		return Score{}, errors.New("the guardrail gave a grounding filter no usable score")
	}
	return Score{
		Value: *filter.Score, Threshold: *filter.Threshold,
		Failed: filter.Action == types.GuardrailContextualGroundingPolicyActionBlocked || filter.Detected != nil && *filter.Detected,
	}, nil
}

// lowest is the lower of two scores, where none yet is the higher.
func lowest(current *Score, next Score) *Score {
	if current == nil || next.Value < current.Value || next.Failed && !current.Failed {
		return &next
	}
	return current
}

// combine is what a whole answer comes to from what each of its pieces did.
func combine(readings []Grounding) Grounding {
	combined := readings[0]
	for _, reading := range readings[1:] {
		if reading.Grounded.Value < combined.Grounded.Value || reading.Grounded.Failed && !combined.Grounded.Failed {
			combined.Grounded = reading.Grounded
		}
		if reading.Relevant.Value > combined.Relevant.Value {
			combined.Relevant = reading.Relevant
		}
		combined.Topics = append(combined.Topics, reading.Topics...)
	}
	slices.Sort(combined.Topics)
	combined.Topics = slices.Compact(combined.Topics)
	return combined
}

// fitSources is sources as one grounding source of at most limit characters:
// the newest results, whole, and the start of the oldest that still fits, in the
// order they were read. What does not fit is left out, so an answer that stood
// on it is held as ungrounded, and never read as though it had been seen.
func fitSources(sources []string, limit int) string {
	var kept []string
	remaining := limit
	for index := len(sources) - 1; index >= 0 && remaining > 0; index-- {
		text := strings.TrimSpace(sources[index])
		if text == "" {
			continue
		}
		text = clip(text, remaining)
		kept = append(kept, text)
		// Two characters more for the blank line between them.
		remaining -= utf8.RuneCountInString(text) + 2
	}
	slices.Reverse(kept)
	return strings.Join(kept, "\n\n")
}

// clip is at most limit characters of text, from its start.
func clip(text string, limit int) string {
	if utf8.RuneCountInString(text) <= limit {
		return text
	}
	return string([]rune(text)[:limit])
}

// chunks are text in pieces of at most limit characters, broken where a break
// reads best in the second half of a piece: between paragraphs, between lines,
// after a sentence, or between words; and, for text with none, at the limit.
func chunks(text string, limit int) []string {
	limit = max(limit, 1)
	runes := []rune(strings.TrimSpace(text))
	var pieces []string
	for len(runes) > 0 {
		cut := len(runes)
		if cut > limit {
			cut = breakBefore(runes[:limit])
		}
		if piece := strings.TrimSpace(string(runes[:cut])); piece != "" {
			pieces = append(pieces, piece)
		}
		runes = runes[cut:]
		for len(runes) > 0 && unicode.IsSpace(runes[0]) {
			runes = runes[1:]
		}
	}
	return pieces
}

// breakBefore is where to cut window: after the last paragraph break, else line
// break, else sentence end, else space in its second half, or at its end.
func breakBefore(window []rune) int {
	half := len(window) / 2
	for _, separator := range []func(index int) bool{
		func(index int) bool { return window[index] == '\n' && index > 0 && window[index-1] == '\n' },
		func(index int) bool { return window[index] == '\n' },
		func(index int) bool {
			return unicode.IsSpace(window[index]) && index > 0 && strings.ContainsRune(".!?", window[index-1])
		},
		func(index int) bool { return unicode.IsSpace(window[index]) },
	} {
		for index := len(window) - 1; index > half; index-- {
			if separator(index) {
				return index + 1
			}
		}
	}
	return len(window)
}
