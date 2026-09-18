package threads

import (
	"context"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime"
	guardrailtypes "github.com/aws/aws-sdk-go-v2/service/bedrockruntime/types"
	"google.golang.org/genai"

	"google.golang.org/adk/v2/agent/llmagent"
	"google.golang.org/adk/v2/artifact"
	"google.golang.org/adk/v2/model"
	"google.golang.org/adk/v2/session"

	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/guardrail"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/pricing"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/runstore"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/usage"
)

// blockingGuardrail intervenes on every answer and passes every request.
type blockingGuardrail struct{}

func (blockingGuardrail) ApplyGuardrail(_ context.Context, input *bedrockruntime.ApplyGuardrailInput, _ ...func(*bedrockruntime.Options)) (*bedrockruntime.ApplyGuardrailOutput, error) {
	if input.Source == guardrailtypes.GuardrailContentSourceOutput {
		return &bedrockruntime.ApplyGuardrailOutput{Action: guardrailtypes.GuardrailActionGuardrailIntervened}, nil
	}
	return &bedrockruntime.ApplyGuardrailOutput{Action: guardrailtypes.GuardrailActionNone}, nil
}

// The assembly wraps the conversation's model the same way: counted inside,
// screened outside.
func TestAGuardedAnswerDoesNotStreamAndStillCostsWhatItSpent(t *testing.T) {
	llm := &scriptedModel{respond: func(*model.LLMRequest) (*genai.Content, error) {
		return genai.NewContentFromText("Something the guardrail stops.", genai.RoleModel), nil
	}}
	guarded := guardrail.Model(usage.Counted(llm), blockingGuardrail{}, guardrail.Config{Identifier: "gr-1", Version: "1", BlockedMessage: "I can't help with that."})
	krude, err := llmagent.New(llmagent.Config{Name: "krude", Model: guarded, Instruction: "You are Krude."})
	if err != nil {
		t.Fatal(err)
	}
	prices, err := pricing.NewTable(nil)
	if err != nil {
		t.Fatal(err)
	}
	runs := newFakeRuns()
	service, err := New(Config{
		AppName: "krude", Agent: krude, Sessions: session.InMemoryService(), Artifacts: artifact.InMemoryService(),
		Runs: runs, Prices: prices, RunTimeout: time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}
	h := &harness{t: t, service: service, runs: runs, model: llm}
	created, err := service.Create(context.Background(), asha)
	if err != nil {
		t.Fatal(err)
	}
	items, run := h.run(asha, created.ID, RunInput{Message: "hi"})
	if deltas := find(items, ItemDelta); len(deltas) != 0 {
		t.Fatalf("a guarded answer streamed %d deltas before it was screened", len(deltas))
	}
	messages := find(items, ItemMessage)
	if answer := messages[len(messages)-1]; answer.Author != "krude" || answer.Text != "I can't help with that." {
		t.Fatalf("answer = %+v, want the blocked message", answer)
	}
	if outcome := runs.finished[run.ID()]; outcome.Status != runstore.StatusCompleted || outcome.Usage.TotalTokens != 110 {
		t.Fatalf("outcome = %+v, want the blocked answer's tokens still counted", outcome)
	}
}
