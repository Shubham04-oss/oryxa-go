package agents

import (
	"context"
	"encoding/json"
	"io"
	"iter"
	"net/http"
	"net/http/httptest"
	"slices"
	"sync"
	"testing"

	"google.golang.org/genai"

	"google.golang.org/adk/v2/model"
	"google.golang.org/adk/v2/model/gemini"
)

// A streamed Gemini 3 answer as ADK stores it: the text, then the empty last
// chunk's signature as a part of its own.
func streamedAnswer(text, signature string) *genai.Content {
	return &genai.Content{Role: genai.RoleModel, Parts: []*genai.Part{
		{Text: text},
		{ThoughtSignature: []byte(signature)},
	}}
}

func TestFoldSignaturesPutsAStreamedAnswersSignatureOnItsText(t *testing.T) {
	answer := streamedAnswer("Hi Sam!", "sig-1")
	contents := []*genai.Content{
		genai.NewContentFromText("hi", genai.RoleUser),
		answer,
		genai.NewContentFromText("what can you do?", genai.RoleUser),
	}

	folded := foldSignatures(contents)

	if len(folded) != 3 || folded[0] != contents[0] || folded[2] != contents[2] {
		t.Fatalf("folded = %v, want the turns around the answer as they were", folded)
	}
	parts := folded[1].Parts
	if len(parts) != 1 || parts[0].Text != "Hi Sam!" || string(parts[0].ThoughtSignature) != "sig-1" || folded[1].Role != genai.RoleModel {
		t.Fatalf("answer = %+v, want its text carrying its signature", folded[1])
	}
	if len(answer.Parts) != 2 || answer.Parts[0].ThoughtSignature != nil {
		t.Fatalf("the stored answer changed: %+v", answer.Parts)
	}
}

func TestFoldSignaturesKeepsAFunctionCallsOwnSignature(t *testing.T) {
	call := &genai.Content{Role: genai.RoleModel, Parts: []*genai.Part{
		{FunctionCall: &genai.FunctionCall{Name: "list_tools"}, ThoughtSignature: []byte("call-sig")},
		{ThoughtSignature: []byte("trailing")},
	}}

	folded := foldSignatures([]*genai.Content{call})

	if len(folded) != 1 || len(folded[0].Parts) != 1 || string(folded[0].Parts[0].ThoughtSignature) != "call-sig" {
		t.Fatalf("folded = %+v, want the call alone with its own signature", folded[0].Parts)
	}
}

func TestFoldSignaturesDropsATurnOfNothingButASignature(t *testing.T) {
	contents := []*genai.Content{
		genai.NewContentFromText("hi", genai.RoleUser),
		{Role: genai.RoleModel, Parts: []*genai.Part{{Thought: true, ThoughtSignature: []byte("sig")}}},
		genai.NewContentFromText("again", genai.RoleUser),
	}

	folded := foldSignatures(contents)

	if len(folded) != 2 || folded[0] != contents[0] || folded[1] != contents[2] {
		t.Fatalf("folded = %v, want the empty turn gone", folded)
	}
}

func TestFoldSignaturesLeavesACleanHistoryAlone(t *testing.T) {
	contents := []*genai.Content{
		genai.NewContentFromText("hi", genai.RoleUser),
		{Role: genai.RoleModel, Parts: []*genai.Part{{Text: "Hello.", ThoughtSignature: []byte("sig")}}},
	}

	if folded := foldSignatures(contents); &folded[0] != &contents[0] {
		t.Fatal("a history with nothing to fold was copied")
	}
}

type requestRecorder struct {
	requests []*model.LLMRequest
}

func (r *requestRecorder) Name() string { return "recorder" }

func (r *requestRecorder) GenerateContent(_ context.Context, req *model.LLMRequest, _ bool) iter.Seq2[*model.LLMResponse, error] {
	r.requests = append(r.requests, req)
	return func(yield func(*model.LLMResponse, error) bool) {}
}

// ADK v2.6 still stores and sends a streamed Gemini 3 answer's signature as
// a part of its own, so foldSignatures still has that part to fold.
func TestADKStillSendsAStreamedAnswersSignatureAsAPartOfItsOwn(t *testing.T) {
	tap, tree := hereModel()
	answer := answerAfter(t, tap, []storedTurn{
		{"user", genai.NewContentFromText("hi", genai.RoleUser)},
		{RootName, streamedAnswer("Hi Sam!", "sig-1")},
	}, "Are you there?")

	if answer != "Yes, I am here." {
		t.Fatalf("answer = %q", answer)
	}
	fromADK := tap.requests[0]
	if len(fromADK) != 3 || !slices.ContainsFunc(fromADK[1].Parts, signatureOnly) {
		t.Fatalf("ADK sent %+v, want the answer with its signature-only part", fromADK)
	}
	sent := tree.requestsFor(RootName)[0].Contents
	if len(sent) != 3 || len(sent[1].Parts) != 1 || string(sent[1].Parts[0].ThoughtSignature) != "sig-1" {
		t.Fatalf("the model was sent %+v, want the signature folded onto the answer's text", sent)
	}
}

// What ADK v2.6's Gemini model puts on the wire for a signature-only part
// (adk-go #1639, working around googleapis/go-genai#931): an empty text
// beside the signature, the shape Gemini 3 streamed it in, rather than the
// bare {"thoughtSignature": ...} that Vertex sometimes refused and that
// foldSignatures exists to avoid.
func TestADKsGeminiModelSendsASignatureOnlyPartWithAnEmptyText(t *testing.T) {
	var (
		mu     sync.Mutex
		bodies []map[string]any
	)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode request: %v", err)
		}
		mu.Lock()
		bodies = append(bodies, body)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"candidates":[{"content":{"role":"model","parts":[{"text":"ok"}]},"finishReason":"STOP"}]}`)
	}))
	t.Cleanup(server.Close)
	llm, err := gemini.NewModel(context.Background(), "gemini-3-flash", &genai.ClientConfig{
		Backend:     genai.BackendGeminiAPI,
		APIKey:      "test-key",
		HTTPOptions: genai.HTTPOptions{BaseURL: server.URL},
	})
	if err != nil {
		t.Fatal(err)
	}
	contents := []*genai.Content{
		genai.NewContentFromText("hi", genai.RoleUser),
		streamedAnswer("Hi Sam!", "sig-1"),
		genai.NewContentFromText("more", genai.RoleUser),
	}

	for _, sending := range []struct {
		name string
		llm  model.LLM
	}{{"ADK alone", llm}, {"repaired", repairedHistory{llm}}} {
		for _, err := range sending.llm.GenerateContent(context.Background(), &model.LLMRequest{Contents: slices.Clone(contents)}, false) {
			if err != nil {
				t.Fatalf("%s: GenerateContent() = %v", sending.name, err)
			}
		}
	}

	if len(bodies) != 2 {
		t.Fatalf("the server got %d requests, want 2", len(bodies))
	}
	answerParts := func(body map[string]any) []any {
		return body["contents"].([]any)[1].(map[string]any)["parts"].([]any)
	}
	// ADK alone: the text, then the signature with an empty text of its own.
	alone := answerParts(bodies[0])
	if len(alone) != 2 {
		t.Fatalf("ADK alone sent parts %v, want two", alone)
	}
	if signed := alone[1].(map[string]any); signed["thoughtSignature"] == nil || signed["text"] != "" {
		t.Fatalf("ADK alone sent the signature part as %v, want an empty text beside it", signed)
	}
	// Repaired: one part, the text carrying the signature.
	repaired := answerParts(bodies[1])
	if len(repaired) != 1 {
		t.Fatalf("the repaired request sent parts %v, want one", repaired)
	}
	if signed := repaired[0].(map[string]any); signed["thoughtSignature"] == nil || signed["text"] != "Hi Sam!" {
		t.Fatalf("the repaired request sent %v, want the text carrying the signature", signed)
	}
}

func TestRepairedHistorySendsNoSignatureOnlyPart(t *testing.T) {
	recorder := &requestRecorder{}
	llm := repairedHistory{recorder}
	req := &model.LLMRequest{Contents: []*genai.Content{
		genai.NewContentFromText("hi", genai.RoleUser),
		streamedAnswer("Hi.", "sig"),
		genai.NewContentFromText("more", genai.RoleUser),
	}}

	for range llm.GenerateContent(context.Background(), req, true) {
	}

	for _, content := range recorder.requests[0].Contents {
		for _, part := range content.Parts {
			if signatureOnly(part) {
				t.Fatalf("sent a signature-only part in %+v", content)
			}
		}
	}
}
