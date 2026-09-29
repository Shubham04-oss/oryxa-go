package agents

import (
	"context"
	"iter"
	"testing"

	"google.golang.org/genai"

	"google.golang.org/adk/v2/model"
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
