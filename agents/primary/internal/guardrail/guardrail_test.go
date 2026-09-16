package guardrail

import (
	"context"
	"errors"
	"io"
	"iter"
	"log/slog"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime/types"
	"google.golang.org/genai"

	"google.golang.org/adk/v2/model"
)

type fakeGuardrail struct {
	actions  map[types.GuardrailContentSource]types.GuardrailAction
	masked   map[types.GuardrailContentSource]string
	failWith error
	sources  []types.GuardrailContentSource
	inputs   []string
}

func (f *fakeGuardrail) ApplyGuardrail(_ context.Context, input *bedrockruntime.ApplyGuardrailInput, _ ...func(*bedrockruntime.Options)) (*bedrockruntime.ApplyGuardrailOutput, error) {
	f.sources = append(f.sources, input.Source)
	for _, block := range input.Content {
		if text, ok := block.(*types.GuardrailContentBlockMemberText); ok {
			f.inputs = append(f.inputs, aws.ToString(text.Value.Text))
		}
	}
	if f.failWith != nil {
		return nil, f.failWith
	}
	output := &bedrockruntime.ApplyGuardrailOutput{Action: f.actions[input.Source]}
	if masked := f.masked[input.Source]; masked != "" {
		output.Outputs = []types.GuardrailOutputContent{{Text: aws.String(masked)}}
	}
	return output, nil
}

type fakeModel struct {
	answer    string
	streamed  []bool
	callCount int
}

func (f *fakeModel) Name() string { return "fake-model" }

func (f *fakeModel) GenerateContent(_ context.Context, _ *model.LLMRequest, stream bool) iter.Seq2[*model.LLMResponse, error] {
	return func(yield func(*model.LLMResponse, error) bool) {
		f.callCount++
		f.streamed = append(f.streamed, stream)
		yield(&model.LLMResponse{Content: genai.NewContentFromText(f.answer, genai.RoleModel), TurnComplete: true}, nil)
	}
}

func userRequest(text string) *model.LLMRequest {
	return &model.LLMRequest{Contents: []*genai.Content{genai.NewContentFromText(text, genai.RoleUser)}}
}

func collect(t *testing.T, sequence iter.Seq2[*model.LLMResponse, error]) []*model.LLMResponse {
	t.Helper()
	var responses []*model.LLMResponse
	for response, err := range sequence {
		if err != nil {
			t.Fatalf("GenerateContent() error = %v", err)
		}
		responses = append(responses, response)
	}
	return responses
}

func responseText(t *testing.T, responses []*model.LLMResponse) string {
	t.Helper()
	if len(responses) == 0 {
		t.Fatal("no responses")
	}
	return responses[len(responses)-1].Content.Parts[0].Text
}

func quietConfig() Config {
	return Config{Identifier: "gr-1", Version: "1", Logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
}

func TestAllowedInputReachesTheModel(t *testing.T) {
	inner := &fakeModel{answer: "All good."}
	client := &fakeGuardrail{actions: map[types.GuardrailContentSource]types.GuardrailAction{}}
	guarded := Model(inner, client, quietConfig())

	responses := collect(t, guarded.GenerateContent(context.Background(), userRequest("hello"), true))

	if inner.callCount != 1 {
		t.Fatalf("inner model calls = %d, want 1", inner.callCount)
	}
	if len(client.inputs) != 2 || client.inputs[0] != "hello" || client.inputs[1] != "All good." {
		t.Fatalf("screened inputs = %v", client.inputs)
	}
	if responseText(t, responses) != "All good." {
		t.Fatalf("answer = %q", responseText(t, responses))
	}
}

func TestGuardrailsTurnStreamingOff(t *testing.T) {
	inner := &fakeModel{answer: "All good."}
	guarded := Model(inner, &fakeGuardrail{actions: map[types.GuardrailContentSource]types.GuardrailAction{}}, quietConfig())

	collect(t, guarded.GenerateContent(context.Background(), userRequest("hello"), true))

	if len(inner.streamed) != 1 || inner.streamed[0] {
		t.Fatalf("the inner model streamed = %v, want false", inner.streamed)
	}
}

func TestBlockedInputNeverReachesTheModel(t *testing.T) {
	inner := &fakeModel{answer: "All good."}
	client := &fakeGuardrail{actions: map[types.GuardrailContentSource]types.GuardrailAction{
		types.GuardrailContentSourceInput: types.GuardrailActionGuardrailIntervened,
	}}
	guarded := Model(inner, client, quietConfig())

	responses := collect(t, guarded.GenerateContent(context.Background(), userRequest("do something bad"), false))

	if inner.callCount != 0 {
		t.Fatalf("the model ran %d times for blocked input", inner.callCount)
	}
	if responseText(t, responses) != "I can't help with that." {
		t.Fatalf("answer = %q", responseText(t, responses))
	}
}

func TestBlockedOutputBecomesTheBlockedMessage(t *testing.T) {
	inner := &fakeModel{answer: "Here is something bad."}
	client := &fakeGuardrail{actions: map[types.GuardrailContentSource]types.GuardrailAction{
		types.GuardrailContentSourceOutput: types.GuardrailActionGuardrailIntervened,
	}}
	guarded := Model(inner, client, quietConfig())

	responses := collect(t, guarded.GenerateContent(context.Background(), userRequest("hello"), false))

	if responseText(t, responses) != "I can't help with that." {
		t.Fatalf("answer = %q", responseText(t, responses))
	}
}

func TestMaskedOutputReplacesTheAnswer(t *testing.T) {
	inner := &fakeModel{answer: "Call 4111 1111 1111 1111 now."}
	client := &fakeGuardrail{
		actions: map[types.GuardrailContentSource]types.GuardrailAction{
			types.GuardrailContentSourceOutput: types.GuardrailActionGuardrailIntervened,
		},
		masked: map[types.GuardrailContentSource]string{
			types.GuardrailContentSourceOutput: "Call {CREDIT_DEBIT_CARD_NUMBER} now.",
		},
	}
	guarded := Model(inner, client, quietConfig())

	responses := collect(t, guarded.GenerateContent(context.Background(), userRequest("hello"), false))

	if responseText(t, responses) != "Call {CREDIT_DEBIT_CARD_NUMBER} now." {
		t.Fatalf("answer = %q", responseText(t, responses))
	}
}

func TestToolResultsAreScreened(t *testing.T) {
	inner := &fakeModel{answer: "Thanks."}
	client := &fakeGuardrail{actions: map[types.GuardrailContentSource]types.GuardrailAction{}}
	guarded := Model(inner, client, quietConfig())
	request := &model.LLMRequest{Contents: []*genai.Content{
		genai.NewContentFromText("look it up", genai.RoleUser),
		{Role: genai.RoleUser, Parts: []*genai.Part{{FunctionResponse: &genai.FunctionResponse{Name: "search", Response: map[string]any{"result": "secret"}}}}},
	}}

	collect(t, guarded.GenerateContent(context.Background(), request, false))

	if len(client.inputs) < 1 || client.inputs[0] != `{"result":"secret"}` {
		t.Fatalf("screened inputs = %v, want the tool result screened", client.inputs)
	}
}

func TestFailedScreenFollowsFailMode(t *testing.T) {
	inner := &fakeModel{answer: "All good."}
	client := &fakeGuardrail{failWith: errors.New("guardrail unavailable")}

	open := quietConfig()
	responses := collect(t, Model(inner, client, open).GenerateContent(context.Background(), userRequest("hello"), false))
	if responseText(t, responses) != "All good." {
		t.Fatalf("fail-open answer = %q", responseText(t, responses))
	}

	closed := quietConfig()
	closed.FailClosed = true
	responses = collect(t, Model(inner, client, closed).GenerateContent(context.Background(), userRequest("hello"), false))
	if responseText(t, responses) != "I can't help with that." {
		t.Fatalf("fail-closed answer = %q", responseText(t, responses))
	}
}

func TestWithoutAnIdentifierTheModelIsUnchanged(t *testing.T) {
	inner := &fakeModel{answer: "All good."}
	if got := Model(inner, &fakeGuardrail{}, Config{}); got != model.LLM(inner) {
		t.Fatalf("Model() = %T, want the inner model", got)
	}
}
