// Package judge decides whether a document from a member's connected apps is
// worth indexing, with a Gemini model on Vertex AI.
//
// Knowledge asks before it publishes anything a connected app yields, so what
// Krude searches is what a member would actually want found, not every
// promotion, notification and empty file their inbox and drive accumulate.
package judge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"slices"
	"strings"
	"time"

	"cloud.google.com/go/auth"
	"google.golang.org/genai"

	"github.com/Shubham04-oss/oryxa-go/knowledge/internal/domain"
	"github.com/Shubham04-oss/oryxa-go/knowledge/internal/transform"
)

// instructions are what "worth indexing" means. Changing them materially is a
// new quality policy: see application's qualityPolicy.
const instructions = `You screen documents from a member's connected apps (Gmail, Google Drive, Slack, Notion, GitHub) before they are indexed for Krude, the member's AI assistant, to search when it answers their questions.

Keep a document when the member could plausibly want Krude to find or use it later:
- correspondence with real people, and anything the member wrote;
- decisions, plans, meeting notes, specs, proposals, reports, contracts;
- receipts, invoices, orders, bookings, statements and confirmations with specific details;
- technical discussion, and information about their accounts, projects or work.

Exclude noise:
- promotions, marketing, sales outreach, and newsletters or announcements sent to many people;
- automated notifications with nothing lasting in them: sign-in alerts, verification codes, likes, follows and other social updates, reminders to upgrade or renew;
- spam and scams;
- empty, untitled or placeholder files, test or scratch files, and automatic copies.

When you are unsure, keep it. Judge only from what you are shown. Give a reason of at most ten words.
Also choose one category for the document from the supplied category list. The category describes its content; it does not decide who may read it.`

const (
	// textBudget is how much of a document's text the model reads: enough to
	// tell a thread from a promotion, far less than a long report's whole.
	textBudget = 8000
	// maxInlinePDF is the largest PDF sent to the model to read: larger ones
	// are judged by their name and details alone.
	maxInlinePDF = 7 << 20
	// maxMetadataValue bounds one detail as the model reads it.
	maxMetadataValue = 300
	// callTimeout bounds one judgment, a large PDF included.
	callTimeout = 90 * time.Second
)

// models is the slice of the Gen AI client this uses, which a test stands in
// for.
type models interface {
	GenerateContent(ctx context.Context, model string, contents []*genai.Content, config *genai.GenerateContentConfig) (*genai.GenerateContentResponse, error)
}

// Config names the model and where it runs.
type Config struct {
	Project  string
	Location string
	Model    string
	// Credentials reach Google Cloud; nil means Application Default
	// Credentials.
	Credentials *auth.Credentials
}

// Gemini judges with a Gemini model.
type Gemini struct {
	models models
	model  string
	config *genai.GenerateContentConfig
}

// New builds a judge on Vertex AI. It contacts nothing until it judges.
func New(ctx context.Context, config Config) (*Gemini, error) {
	if strings.TrimSpace(config.Model) == "" {
		return nil, errors.New("a judge needs a model")
	}
	if strings.TrimSpace(config.Project) == "" {
		return nil, errors.New("a judge needs a Google Cloud project: Vertex AI serves the model")
	}
	client, err := genai.NewClient(ctx, &genai.ClientConfig{
		Backend: genai.BackendVertexAI, Project: config.Project, Location: config.Location,
		Credentials: config.Credentials,
	})
	if err != nil {
		return nil, fmt.Errorf("build the judge's Gen AI client: %w", err)
	}
	return newGemini(client.Models, config.Model), nil
}

func newGemini(models models, model string) *Gemini {
	safety := make([]*genai.SafetySetting, 0, 4)
	for _, category := range []genai.HarmCategory{
		genai.HarmCategoryHarassment, genai.HarmCategoryHateSpeech,
		genai.HarmCategorySexuallyExplicit, genai.HarmCategoryDangerousContent,
	} {
		// A member's own mail and files are what is being judged, not
		// generated: nothing in them is the model's to refuse to read.
		safety = append(safety, &genai.SafetySetting{Category: category, Threshold: genai.HarmBlockThresholdOff})
	}
	config := &genai.GenerateContentConfig{
		SystemInstruction: &genai.Content{Parts: []*genai.Part{{Text: instructions}}},
		ResponseMIMEType:  "application/json",
		ResponseSchema: &genai.Schema{
			Type: genai.TypeObject,
			Properties: map[string]*genai.Schema{
				"reason":   {Type: genai.TypeString, Description: "Why, in at most ten words."},
				"keep":     {Type: genai.TypeBoolean, Description: "Whether the document is worth indexing."},
				"category": {Type: genai.TypeString, Description: "The document's semantic kind.", Enum: categoryNames()},
			},
			// The reason first, so the decision follows from it.
			PropertyOrdering: []string{"reason", "category", "keep"},
			Required:         []string{"reason", "category", "keep"},
		},
		SafetySettings:  safety,
		MaxOutputTokens: 2048,
	}
	// Gemini 2.5 rejects thinking_level. Gemini 3 accepts it and low keeps
	// a document classification inexpensive. Other models use their default.
	if strings.HasPrefix(model, "gemini-3") {
		config.ThinkingConfig = &genai.ThinkingConfig{ThinkingLevel: genai.ThinkingLevelLow}
	}
	return &Gemini{models: models, model: model, config: config}
}

// Judge decides one document. It fails only when the model cannot be reached
// or will not take the request at all, wrapping
// domain.ErrQualityCheckUnavailable; a document the model cannot read is kept
// rather than lost.
func (judge *Gemini) Judge(ctx context.Context, candidate domain.Candidate) (domain.Verdict, error) {
	withPDF := candidate.MIME == transform.MIMEPDF && len(candidate.Original) > 0 && len(candidate.Original) <= maxInlinePDF
	verdict, err := judge.ask(ctx, candidate, withPDF)
	var apiErr genai.APIError
	if withPDF && errors.As(err, &apiErr) && apiErr.Code == http.StatusBadRequest {
		// The model would not take the file itself (damaged, encrypted,
		// more pages than it reads): judge it by the rest.
		return judge.ask(ctx, candidate, false)
	}
	return verdict, err
}

func (judge *Gemini) ask(ctx context.Context, candidate domain.Candidate, withPDF bool) (domain.Verdict, error) {
	ctx, cancel := context.WithTimeout(ctx, callTimeout)
	defer cancel()
	parts := []*genai.Part{genai.NewPartFromText(describe(candidate, withPDF))}
	if withPDF {
		parts = append(parts, genai.NewPartFromBytes(candidate.Original, transform.MIMEPDF))
	}
	response, err := judge.models.GenerateContent(ctx, judge.model,
		[]*genai.Content{genai.NewContentFromParts(parts, genai.RoleUser)}, judge.config)
	if err != nil {
		return domain.Verdict{}, fmt.Errorf("%w: %w", domain.ErrQualityCheckUnavailable, err)
	}
	verdict := verdictOf(response)
	verdict.Judgment = judgment{Gemini: &geminiVerdict{Keep: verdict.Keep, Reason: verdict.Reason, Category: verdict.Category, Model: judge.model}}.encode()
	return verdict, nil
}

// Redecide gives the verdict kept with a document again: the Gemini judge's
// answer does not depend on which kinds a source indexes.
func (judge *Gemini) Redecide(kept []byte, _ []string) (domain.Verdict, bool) {
	return decodeJudgment(kept).gemini(kept)
}

// verdictOf reads the model's answer. One it would not give because of what
// the document holds is an exclusion; one that cannot be read keeps the
// document, since losing something that matters is worse than keeping
// something that does not.
func verdictOf(response *genai.GenerateContentResponse) domain.Verdict {
	if response == nil {
		return domain.Verdict{Keep: true, Reason: "the quality check gave no answer"}
	}
	if feedback := response.PromptFeedback; feedback != nil && feedback.BlockReason != "" {
		return domain.Verdict{Keep: false, Reason: "the model refused to read it: " + strings.ToLower(string(feedback.BlockReason))}
	}
	if len(response.Candidates) > 0 {
		switch reason := response.Candidates[0].FinishReason; reason {
		case genai.FinishReasonSafety, genai.FinishReasonProhibitedContent, genai.FinishReasonBlocklist, genai.FinishReasonSPII:
			return domain.Verdict{Keep: false, Reason: "the model refused to read it: " + strings.ToLower(string(reason))}
		}
	}
	var answer struct {
		Keep     *bool  `json:"keep"`
		Reason   string `json:"reason"`
		Category string `json:"category"`
	}
	if err := json.Unmarshal([]byte(response.Text()), &answer); err != nil || answer.Keep == nil {
		return domain.Verdict{Keep: true, Reason: "the quality check gave no clear answer"}
	}
	category := strings.TrimSpace(answer.Category)
	if !slices.Contains(categoryNames(), category) {
		category = ""
	}
	return domain.Verdict{Keep: *answer.Keep, Reason: strings.TrimSpace(answer.Reason), Category: category}
}

func categoryNames() []string {
	names := make([]string, 0, len(domain.Kinds)+3)
	for _, kind := range domain.Kinds {
		names = append(names, kind.Name)
	}
	return append(names, "spam", "empty", "other")
}

// describe is what the model reads about a document: where it comes from,
// its details, and its text or a note of what stands in for it.
func describe(candidate domain.Candidate, withPDF bool) string {
	var out strings.Builder
	fmt.Fprintf(&out, "Source: %s", candidate.Provider)
	if candidate.Kind != "" {
		fmt.Fprintf(&out, " (%s)", candidate.Kind)
	}
	title := strings.TrimSpace(candidate.Title)
	if title == "" {
		title = "(untitled)"
	}
	fmt.Fprintf(&out, "\nTitle: %s\nType: %s\nSize: %d bytes\n", oneLine(title, maxMetadataValue), candidate.MIME, candidate.Size)
	for _, key := range slices.Sorted(maps.Keys(candidate.Metadata)) {
		value := strings.TrimSpace(candidate.Metadata[key])
		if value == "" || strings.HasSuffix(key, "_id") || strings.Contains(key, "checksum") {
			continue
		}
		fmt.Fprintf(&out, "%s: %s\n", key, oneLine(value, maxMetadataValue))
	}
	switch {
	case withPDF:
		out.WriteString("\nContent: the attached PDF.\n")
	case candidate.Text != "":
		text := candidate.Text
		if runes := []rune(text); len(runes) > textBudget {
			text = string(runes[:textBudget]) + "\n[...]"
		}
		fmt.Fprintf(&out, "\nContent:\n%s\n", text)
	case len(candidate.Original) > 0:
		out.WriteString("\nContent: not shown, judge it by its name and details.\n")
	default:
		out.WriteString("\nContent: none.\n")
	}
	return out.String()
}

// oneLine keeps a detail on one line, and bounded.
func oneLine(value string, limit int) string {
	value = strings.Join(strings.Fields(value), " ")
	if runes := []rune(value); len(runes) > limit {
		return string(runes[:limit]) + "…"
	}
	return value
}
