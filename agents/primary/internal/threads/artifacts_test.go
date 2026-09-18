package threads

import (
	"context"
	"errors"
	"testing"
	"time"

	"google.golang.org/genai"

	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/agent/llmagent"
	"google.golang.org/adk/v2/artifact"
	"google.golang.org/adk/v2/model"
	"google.golang.org/adk/v2/session"
	"google.golang.org/adk/v2/tool"
	"google.golang.org/adk/v2/tool/functiontool"

	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/pricing"
)

func TestAFileARunKeepsShowsInTheThreadAndCanBeRead(t *testing.T) {
	screenshot, err := functiontool.New(functiontool.Config{Name: "take_screenshot", Description: "Takes a screenshot."},
		func(ctx agent.Context, _ struct{}) (map[string]any, error) {
			if _, err := ctx.Artifacts().Save(ctx, "shot.png", genai.NewPartFromBytes([]byte("png-bytes"), "image/png")); err != nil {
				return nil, err
			}
			return map[string]any{"saved": "shot.png"}, nil
		})
	if err != nil {
		t.Fatal(err)
	}
	llm := &scriptedModel{respond: func(request *model.LLMRequest) (*genai.Content, error) {
		if lastFunctionResponse(request) != nil {
			return genai.NewContentFromText("Here it is.", genai.RoleModel), nil
		}
		return &genai.Content{Role: genai.RoleModel, Parts: []*genai.Part{{FunctionCall: &genai.FunctionCall{ID: "call_1", Name: "take_screenshot", Args: map[string]any{}}}}}, nil
	}}
	krude, err := llmagent.New(llmagent.Config{Name: "krude", Model: llm, Instruction: "You are Krude.", Tools: []tool.Tool{screenshot}})
	if err != nil {
		t.Fatal(err)
	}
	prices, err := pricing.NewTable(nil)
	if err != nil {
		t.Fatal(err)
	}
	service, err := New(Config{
		AppName: "krude", Agent: krude, Sessions: session.InMemoryService(), Artifacts: artifact.InMemoryService(),
		Runs: newFakeRuns(), Prices: prices, RunTimeout: time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}
	h := &harness{t: t, service: service, model: llm}
	created, err := service.Create(context.Background(), asha)
	if err != nil {
		t.Fatal(err)
	}
	items, _ := h.run(asha, created.ID, RunInput{Message: "Take a screenshot"})
	kept := find(items, ItemArtifact)
	if len(kept) != 1 || kept[0].Artifact != "shot.png" || kept[0].Version < 0 {
		t.Fatalf("artifact items = %+v", kept)
	}
	detail, err := service.Get(context.Background(), asha, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if history := find(detail.Items, ItemArtifact); len(history) != 1 || history[0].Artifact != "shot.png" {
		t.Fatalf("history artifacts = %+v", history)
	}

	file, err := service.Artifact(context.Background(), asha, created.ID, "shot.png", 0)
	if err != nil || file.MIMEType != "image/png" || string(file.Data) != "png-bytes" {
		t.Fatalf("Artifact() = %+v, %v", file, err)
	}
	if _, err := service.Artifact(context.Background(), asha, created.ID, "missing.png", 0); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a missing artifact = %v, want ErrNotFound", err)
	}
	someoneElse := Member{UserID: "user_ravi", OrganizationID: asha.OrganizationID}
	if _, err := service.Artifact(context.Background(), someoneElse, created.ID, "shot.png", 0); !errors.Is(err, ErrNotFound) {
		t.Fatalf("another member reading it = %v, want ErrNotFound", err)
	}
}
