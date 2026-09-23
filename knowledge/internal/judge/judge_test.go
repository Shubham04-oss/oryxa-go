package judge

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"google.golang.org/genai"

	"github.com/Shubham04-oss/oryxa-go/knowledge/internal/domain"
	"github.com/Shubham04-oss/oryxa-go/knowledge/internal/transform"
)

type request struct {
	model    string
	contents []*genai.Content
	config   *genai.GenerateContentConfig
}

// fakeModels answers each call with the next scripted response or error, and
// records what it was asked.
type fakeModels struct {
	responses []*genai.GenerateContentResponse
	errs      []error
	requests  []request
}

func (fake *fakeModels) GenerateContent(_ context.Context, model string, contents []*genai.Content, config *genai.GenerateContentConfig) (*genai.GenerateContentResponse, error) {
	index := len(fake.requests)
	fake.requests = append(fake.requests, request{model: model, contents: contents, config: config})
	if index < len(fake.errs) && fake.errs[index] != nil {
		return nil, fake.errs[index]
	}
	if index < len(fake.responses) {
		return fake.responses[index], nil
	}
	return nil, errors.New("the test scripted no answer for this call")
}

func answer(text string) *genai.GenerateContentResponse {
	return &genai.GenerateContentResponse{Candidates: []*genai.Candidate{{
		Content: genai.NewContentFromText(text, genai.RoleModel), FinishReason: genai.FinishReasonStop,
	}}}
}

func promotion() domain.Candidate {
	return domain.Candidate{
		Provider: "gmail", Kind: "message", Title: "50% off everything this weekend", MIME: transform.MIMEPlain,
		Metadata: map[string]string{"from": "Shop <deals@shop.example>", "labels": "INBOX, CATEGORY_UPDATES", "thread_id": "t-1"},
		Text:     "Shop now. Sale ends Sunday.", Size: 27,
	}
}

func TestJudgeReadsTheModelsVerdict(t *testing.T) {
	models := &fakeModels{responses: []*genai.GenerateContentResponse{answer(`{"reason":"promotional mail","category":"promotion","keep":false}`)}}
	judge := newGemini(models, "gemini-3.5-flash-lite")

	verdict, err := judge.Judge(context.Background(), promotion())
	if err != nil {
		t.Fatalf("Judge() error = %v", err)
	}
	if verdict.Keep || verdict.Reason != "promotional mail" || verdict.Category != "promotion" {
		t.Fatalf("verdict = %+v, want excluded as promotional mail", verdict)
	}

	asked := models.requests[0]
	if asked.model != "gemini-3.5-flash-lite" {
		t.Fatalf("model = %q", asked.model)
	}
	config := asked.config
	if config.ResponseMIMEType != "application/json" || config.ResponseSchema == nil ||
		config.ResponseSchema.Properties["keep"] == nil || config.ResponseSchema.Properties["reason"] == nil ||
		config.ResponseSchema.Properties["category"] == nil {
		t.Fatalf("the answer is not asked for as keep and reason: %+v", config.ResponseSchema)
	}
	if config.SystemInstruction == nil || !strings.Contains(config.SystemInstruction.Parts[0].Text, "Exclude noise") {
		t.Fatal("the model was not told what worth indexing means")
	}
	if config.ThinkingConfig == nil || config.ThinkingConfig.ThinkingLevel != genai.ThinkingLevelLow {
		t.Fatalf("thinking = %+v, want low", config.ThinkingConfig)
	}
	for _, setting := range config.SafetySettings {
		if setting.Threshold != genai.HarmBlockThresholdOff {
			t.Fatalf("safety %s = %s, want off: the member's own mail is being read, not generated", setting.Category, setting.Threshold)
		}
	}
	shown := asked.contents[0].Parts[0].Text
	for _, want := range []string{"Source: gmail (message)", "Title: 50% off everything this weekend", "from: Shop <deals@shop.example>", "labels: INBOX, CATEGORY_UPDATES", "Sale ends Sunday."} {
		if !strings.Contains(shown, want) {
			t.Fatalf("the model was not shown %q:\n%s", want, shown)
		}
	}
	if strings.Contains(shown, "thread_id") {
		t.Fatalf("an internal id was shown to the model:\n%s", shown)
	}
}

func TestGemini25OmitsUnsupportedThinkingLevel(t *testing.T) {
	t.Parallel()
	judge := newGemini(&fakeModels{}, "gemini-2.5-flash")
	if judge.config.ThinkingConfig != nil {
		t.Fatalf("Gemini 2.5 thinking config = %+v, want none", judge.config.ThinkingConfig)
	}
}

func TestJudgeReadsAPDFItselfAndFallsBackWhenTheModelWillNotTakeIt(t *testing.T) {
	models := &fakeModels{
		errs:      []error{genai.APIError{Code: http.StatusBadRequest, Message: "The document has no pages."}},
		responses: []*genai.GenerateContentResponse{nil, answer(`{"reason":"a real report","keep":true}`)},
	}
	judge := newGemini(models, "gemini-test")
	pdf := domain.Candidate{Provider: "googledrive", Kind: "file", Title: "Q3 report.pdf", MIME: transform.MIMEPDF,
		Original: []byte("%PDF-1.4 bytes"), Size: 14}

	verdict, err := judge.Judge(context.Background(), pdf)
	if err != nil {
		t.Fatalf("Judge() error = %v", err)
	}
	if !verdict.Keep {
		t.Fatalf("verdict = %+v, want kept", verdict)
	}
	if len(models.requests) != 2 {
		t.Fatalf("asked %d times, want the PDF then its details alone", len(models.requests))
	}
	first := models.requests[0].contents[0].Parts
	if len(first) != 2 || first[1].InlineData == nil || first[1].InlineData.MIMEType != transform.MIMEPDF {
		t.Fatal("the first ask did not carry the PDF itself")
	}
	second := models.requests[1].contents[0].Parts
	if len(second) != 1 || !strings.Contains(second[0].Text, "not shown") {
		t.Fatalf("the fallback did not judge by the details alone: %d parts", len(second))
	}
}

func TestJudgeDoesNotSendAPDFTooLargeToRead(t *testing.T) {
	models := &fakeModels{responses: []*genai.GenerateContentResponse{answer(`{"reason":"a scan","keep":true}`)}}
	judge := newGemini(models, "gemini-test")
	large := domain.Candidate{Provider: "googledrive", Title: "scan.pdf", MIME: transform.MIMEPDF,
		Original: make([]byte, maxInlinePDF+1), Size: maxInlinePDF + 1}

	if _, err := judge.Judge(context.Background(), large); err != nil {
		t.Fatalf("Judge() error = %v", err)
	}
	if parts := models.requests[0].contents[0].Parts; len(parts) != 1 {
		t.Fatalf("sent %d parts, want the details alone", len(parts))
	}
}

func TestJudgeReportsAModelItCannotReachAsUnavailable(t *testing.T) {
	for _, cause := range []error{
		genai.APIError{Code: http.StatusServiceUnavailable, Message: "overloaded"},
		genai.APIError{Code: http.StatusTooManyRequests, Message: "quota"},
		// A text-only request refused outright means the request itself is
		// wrong, for every document; the run stops rather than index
		// everything unjudged.
		genai.APIError{Code: http.StatusBadRequest, Message: "invalid thinking level"},
	} {
		judge := newGemini(&fakeModels{errs: []error{cause}}, "gemini-test")
		if _, err := judge.Judge(context.Background(), promotion()); !errors.Is(err, domain.ErrQualityCheckUnavailable) {
			t.Fatalf("Judge() error = %v for %v, want ErrQualityCheckUnavailable", err, cause)
		}
	}
}

func TestJudgeKeepsWhatItGetsNoClearAnswerFor(t *testing.T) {
	for _, response := range []*genai.GenerateContentResponse{
		answer("not json at all"),
		answer(`{"reason":"missing the decision"}`),
		{},
	} {
		judge := newGemini(&fakeModels{responses: []*genai.GenerateContentResponse{response}}, "gemini-test")
		verdict, err := judge.Judge(context.Background(), promotion())
		if err != nil {
			t.Fatalf("Judge() error = %v", err)
		}
		if !verdict.Keep {
			t.Fatalf("verdict = %+v, want kept: losing something that matters is worse", verdict)
		}
	}
}

func TestJudgeExcludesWhatTheModelRefusesToRead(t *testing.T) {
	blocked := &genai.GenerateContentResponse{PromptFeedback: &genai.GenerateContentResponsePromptFeedback{
		BlockReason: genai.BlockedReasonProhibitedContent,
	}}
	judge := newGemini(&fakeModels{responses: []*genai.GenerateContentResponse{blocked}}, "gemini-test")
	verdict, err := judge.Judge(context.Background(), promotion())
	if err != nil {
		t.Fatalf("Judge() error = %v", err)
	}
	if verdict.Keep || !strings.Contains(verdict.Reason, "prohibited_content") {
		t.Fatalf("verdict = %+v, want excluded with the model's reason", verdict)
	}
}

func TestDescribeCutsLongTextAndNamesWhatStandsInForContent(t *testing.T) {
	long := domain.Candidate{Provider: "googledrive", Title: "", MIME: transform.MIMEPlain, Text: strings.Repeat("a", textBudget+500)}
	shown := describe(long, false)
	if !strings.Contains(shown, "Title: (untitled)") {
		t.Fatalf("an untitled document was not called that:\n%.200s", shown)
	}
	if strings.Count(shown, "a") > textBudget+50 || !strings.Contains(shown, "[...]") {
		t.Fatal("the text was not cut to the budget")
	}
	office := describe(domain.Candidate{Provider: "googledrive", Title: "deck.pptx", MIME: "application/vnd.openxmlformats-officedocument.presentationml.presentation", Original: []byte("PK")}, false)
	if !strings.Contains(office, "judge it by its name and details") {
		t.Fatalf("an Office file's stand-in was not named:\n%s", office)
	}
}
