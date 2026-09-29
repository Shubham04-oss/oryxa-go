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
	if err != nil || file.MIMEType != "image/png" || string(file.Data) != "png-bytes" || file.Version != 1 {
		t.Fatalf("Artifact() = %+v, %v", file, err)
	}
	if _, err := service.Artifact(context.Background(), asha, created.ID, "missing.png", 0); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a missing artifact = %v, want ErrNotFound", err)
	}
	if _, err := service.Artifact(context.Background(), asha, created.ID, "../shot.png", 0); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a name with a path in it = %v, want ErrNotFound", err)
	}
	someoneElse := Member{UserID: "user_ravi", OrganizationID: asha.OrganizationID}
	if _, err := service.Artifact(context.Background(), someoneElse, created.ID, "shot.png", 0); !errors.Is(err, ErrNotFound) {
		t.Fatalf("another member reading it = %v, want ErrNotFound", err)
	}
	version, err := service.SaveArtifact(context.Background(), asha, created.ID, "shot.png", 1, []byte("edited-png"))
	if err != nil || version != 2 {
		t.Fatalf("edited artifact version = %d, %v", version, err)
	}
	latest, err := service.Artifact(context.Background(), asha, created.ID, "shot.png", 0)
	if err != nil || latest.Version != 2 || string(latest.Data) != "edited-png" {
		t.Fatalf("latest edited artifact = %+v, %v", latest, err)
	}
	old, err := service.Artifact(context.Background(), asha, created.ID, "shot.png", 1)
	if err != nil || string(old.Data) != "png-bytes" {
		t.Fatalf("previous version = %+v, %v", old, err)
	}
	if _, err := service.SaveArtifact(context.Background(), asha, created.ID, "shot.png", 1, []byte("different")); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale edit = %v, want conflict", err)
	}
	if version, err := service.SaveArtifact(context.Background(), asha, created.ID, "shot.png", 1, []byte("edited-png")); err != nil || version != 2 {
		t.Fatalf("retry of saved edit = %d, %v", version, err)
	}
	if _, err := service.SaveArtifact(context.Background(), someoneElse, created.ID, "shot.png", 2, []byte("stolen")); !errors.Is(err, ErrNotFound) {
		t.Fatalf("another member editing it = %v, want not found", err)
	}
}

// An edit is saved as the file it replaces, whatever type that file carries,
// not as what its extension says.
func TestAnEditedFileKeepsTheTypeOfTheVersionItReplaces(t *testing.T) {
	llm := &scriptedModel{respond: func(*model.LLMRequest) (*genai.Content, error) {
		return genai.NewContentFromText("Done.", genai.RoleModel), nil
	}}
	krude, err := llmagent.New(llmagent.Config{Name: "krude", Model: llm, Instruction: "You are Krude."})
	if err != nil {
		t.Fatal(err)
	}
	prices, err := pricing.NewTable(nil)
	if err != nil {
		t.Fatal(err)
	}
	artifacts := artifact.InMemoryService()
	service, err := New(Config{
		AppName: "krude", Agent: krude, Sessions: session.InMemoryService(), Artifacts: artifacts,
		Runs: newFakeRuns(), Prices: prices, RunTimeout: time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	created, err := service.Create(ctx, asha)
	if err != nil {
		t.Fatal(err)
	}
	// An agent kept the file with a type of its own choosing: not the one
	// its extension would give it.
	const kept = "application/octet-stream"
	if _, err := artifacts.Save(ctx, &artifact.SaveRequest{
		AppName: "krude", UserID: asha.UserID, SessionID: created.ID, FileName: "plan.docx",
		Part: genai.NewPartFromBytes([]byte("docx-v1"), kept),
	}); err != nil {
		t.Fatal(err)
	}
	saved, err := service.SaveArtifact(ctx, asha, created.ID, "plan.docx", 1, []byte("docx-v2"))
	if err != nil || saved != 2 {
		t.Fatalf("SaveArtifact() = %d, %v; want version 2", saved, err)
	}
	edited, err := service.Artifact(ctx, asha, created.ID, "plan.docx", 0)
	if err != nil || edited.Version != 2 || string(edited.Data) != "docx-v2" || edited.MIMEType != kept {
		t.Fatalf("the edited file = %+v, %v; want version 2 still typed %q", edited, err, kept)
	}
}
