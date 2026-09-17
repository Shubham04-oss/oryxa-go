// Package generation writes the answer from the passages retrieval found.
//
// The whole point of retrieval-augmented generation is that the answer comes
// from the documents rather than from the model's memory, so the rules here
// are about keeping that true. Passages are given to the model as numbered,
// clearly delimited sources; the instruction is to answer only from them and
// to say so when they do not contain the answer; and when retrieval found
// nothing worth using, no model is called at all.
//
// Citations are checked rather than trusted. A model that cites source seven
// when six were supplied has invented it, and an answer whose citations cannot
// be resolved is indistinguishable from a correct one until someone looks.
package generation

import (
	"context"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/Shubham04-oss/oryxa-go/rag/internal/domain"
)

// Generator writes one grounded answer.
type Generator interface {
	// Generate answers the question from the passages, or says it cannot.
	Generate(ctx context.Context, request Request) (Response, error)
	// Model names what answered, for the response and for cost accounting.
	Model() string
}

// Request is a question and what was retrieved for it.
type Request struct {
	Question string
	Passages []Passage
	// Instructions is an organization's own addition to the system prompt —
	// tone, house terminology, what to do with an unanswerable question. It
	// never replaces the grounding rules, which are not negotiable.
	Instructions string
	MaxTokens    int
}

// Passage is one numbered source the answer may draw on.
type Passage struct {
	Index   int
	Title   string
	Heading string
	Text    string
}

// Response is what the model returned.
type Response struct {
	Text  string
	Usage domain.Usage
}

// MaxAnswerTokens bounds one answer.
const MaxAnswerTokens = 2048

// systemPrompt is the grounding contract.
//
// It is deliberately about what to do with the sources rather than about tone.
// The failure this guards against is not a badly worded answer, it is a
// confident answer to a question the documents do not address, which reads
// exactly like a good one.
const systemPrompt = `You answer questions using only the numbered sources provided.

Rules:
- Use only what the sources say. Do not add facts from your own knowledge, even when you are confident they are correct.
- Cite every claim with the source number in square brackets, like [2]. A sentence drawing on two sources cites both, like [1][3].
- Only cite numbers that were actually provided.
- If the sources do not contain the answer, say so plainly and stop. Do not guess, and do not offer a partial answer as though it were complete.
- If the sources disagree, say that they disagree and cite each side.
- Answer in the question's own language.
- Be direct. Do not restate the question or describe what you are about to do.`

// Prompt builds the system and user messages for one request.
//
// The passages are wrapped in explicit delimiters and numbered. That structure
// is what lets the model tell the sources apart from each other and from the
// question, and it is what makes a citation number mean something checkable.
func Prompt(request Request) (system, user string) {
	system = systemPrompt
	if instructions := strings.TrimSpace(request.Instructions); instructions != "" {
		system += "\n\nAdditional instructions from this organization:\n" + instructions
	}
	var builder strings.Builder
	builder.WriteString("Sources:\n\n")
	for _, passage := range request.Passages {
		builder.WriteString("<source id=\"" + strconv.Itoa(passage.Index) + "\"")
		if passage.Title != "" {
			builder.WriteString(" title=" + strconv.Quote(passage.Title))
		}
		if passage.Heading != "" {
			builder.WriteString(" section=" + strconv.Quote(passage.Heading))
		}
		builder.WriteString(">\n")
		builder.WriteString(passage.Text)
		builder.WriteString("\n</source>\n\n")
	}
	builder.WriteString("Question: " + request.Question)
	return system, builder.String()
}

// PassagesFrom turns matches into numbered passages, in the order they were
// ranked. The numbering is one-based because that is what the model is asked
// to cite and what a reader expects to see.
func PassagesFrom(matches []domain.Match) []Passage {
	passages := make([]Passage, 0, len(matches))
	for position, match := range matches {
		passages = append(passages, Passage{
			Index:   position + 1,
			Title:   match.Document.Title,
			Heading: strings.Join(match.Chunk.HeadingPath, " > "),
			Text:    match.Chunk.Text,
		})
	}
	return passages
}

// citationPattern finds the bracketed source numbers in an answer.
var citationPattern = regexp.MustCompile(`\[(\d{1,3})\]`)

// Cited returns the source numbers an answer actually used, in the order they
// first appear, dropping any that were never supplied.
//
// Dropping rather than failing is the right trade: an answer that is right
// about three sources and invented a fourth is still worth showing, with the
// invention removed. What must not happen is a citation rendering as a link to
// a passage the answer never saw.
func Cited(answer string, supplied int) []int {
	var used []int
	for _, match := range citationPattern.FindAllStringSubmatch(answer, -1) {
		number, err := strconv.Atoi(match[1])
		if err != nil || number < 1 || number > supplied {
			continue
		}
		if !slices.Contains(used, number) {
			used = append(used, number)
		}
	}
	return used
}

// Answer runs one grounded generation over retrieved matches.
//
// With nothing retrieved it returns an ungrounded refusal without calling a
// model at all. That is not only cheaper: a model given no sources and asked a
// question will answer it from memory, which is the exact failure this whole
// package exists to prevent.
func Answer(ctx context.Context, generator Generator, question string, matches []domain.Match, instructions string) (domain.Answer, error) {
	if len(matches) == 0 {
		return domain.Answer{
			Text:      "I could not find anything in this corpus that answers that.",
			Citations: []domain.Citation{},
			Grounded:  false,
		}, nil
	}
	passages := PassagesFrom(matches)
	response, err := generator.Generate(ctx, Request{
		Question: question, Passages: passages, Instructions: instructions, MaxTokens: MaxAnswerTokens,
	})
	if err != nil {
		return domain.Answer{}, fmt.Errorf("generate an answer: %w", err)
	}
	all := domain.Citations(matches)
	cited := Cited(response.Text, len(passages))

	// Only the sources the answer actually used are returned. Listing every
	// retrieved passage as a citation would make the answer look better
	// supported than it is.
	citations := make([]domain.Citation, 0, len(cited))
	for _, number := range cited {
		citations = append(citations, all[number-1])
	}
	return domain.Answer{
		Text:      strings.TrimSpace(response.Text),
		Citations: citations,
		Model:     generator.Model(),
		Usage:     response.Usage,
		Grounded:  len(citations) > 0,
	}, nil
}
