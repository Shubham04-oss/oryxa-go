package threads

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"google.golang.org/genai"

	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/artifact"
	"google.golang.org/adk/v2/model"
	"google.golang.org/adk/v2/session"
	"google.golang.org/adk/v2/tool"
	"google.golang.org/adk/v2/tool/functiontool"

	primaryagents "github.com/Shubham04-oss/oryxa-go/agents/primary/internal/agents"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/modelrouter"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/pricing"
)

type toolList []tool.Tool

func (toolList) Name() string { return "list" }

func (tools toolList) Tools(agent.ReadonlyContext) ([]tool.Tool, error) { return tools, nil }

// TestARunStartsWithTheToolboxesItNamesLoaded: a run that names the
// toolboxes its work needs has their tools from its very first model call,
// with their skills in its instruction, so no call goes to listing or
// loading them; and the thread keeps them loaded, beside those it had.
func TestARunStartsWithTheToolboxesItNamesLoaded(t *testing.T) {
	ctx := context.Background()
	var requests []*model.LLMRequest
	llm := &scriptedModel{respond: func(request *model.LLMRequest) (*genai.Content, error) {
		requests = append(requests, request)
		return genai.NewContentFromText("Done.", genai.RoleModel), nil
	}}
	addNote, err := functiontool.New(functiontool.Config{Name: "notes_add", Description: "Adds a note."},
		func(_ agent.Context, args struct {
			Text string `json:"text"`
		}) (map[string]any, error) {
			return map[string]any{"added": args.Text}, nil
		})
	if err != nil {
		t.Fatal(err)
	}
	router, err := modelrouter.New(llm, llm)
	if err != nil {
		t.Fatal(err)
	}
	krude, err := primaryagents.UnattendedRoot(router, []primaryagents.Toolbox{
		{Name: "notes", Description: "Notes.", Instructions: "Use notes_add for every note.", Toolset: toolList{addNote}},
		{Name: "diary", Description: "A diary.", Toolset: toolList{}},
	})
	if err != nil {
		t.Fatal(err)
	}
	prices, err := pricing.NewTable(nil)
	if err != nil {
		t.Fatal(err)
	}
	sessions := session.InMemoryService()
	service, err := New(Config{
		AppName: "krude", Agent: krude, Sessions: sessions, Artifacts: artifact.InMemoryService(),
		Runs: newFakeRuns(), Prices: prices, RunTimeout: time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}
	thread, err := service.Create(ctx, asha)
	if err != nil {
		t.Fatal(err)
	}
	// The thread already has a toolbox loaded, which the run keeps.
	stored, err := sessions.Get(ctx, &session.GetRequest{AppName: "krude", UserID: asha.UserID, SessionID: thread.ID})
	if err != nil {
		t.Fatal(err)
	}
	if err := sessions.AppendEvent(ctx, stored.Session, &session.Event{
		Author: "user", Actions: session.EventActions{StateDelta: primaryagents.LoadToolboxes(stored.Session.State(), "diary")},
	}); err != nil {
		t.Fatal(err)
	}

	run, err := service.StartRun(ctx, asha, thread.ID, RunInput{Message: "Add a note.", Toolboxes: []string{"notes"}})
	if err != nil {
		t.Fatal(err)
	}
	run.Execute(ctx, func(Item) error { return nil })

	if len(requests) != 1 {
		t.Fatalf("the model was called %d times, want once", len(requests))
	}
	first := requests[0]
	var declared []string
	for _, declaration := range first.Config.Tools {
		for _, function := range declaration.FunctionDeclarations {
			declared = append(declared, function.Name)
		}
	}
	if !slices.Contains(declared, "notes_add") {
		t.Fatalf("the first model call declared %v, want notes_add among them", declared)
	}
	var instruction strings.Builder
	for _, part := range first.Config.SystemInstruction.Parts {
		instruction.WriteString(part.Text)
	}
	if !strings.Contains(instruction.String(), "Use notes_add for every note.") {
		t.Fatalf("the first model call's instruction lacks the notes skill:\n%s", instruction.String())
	}
	after, err := sessions.Get(ctx, &session.GetRequest{AppName: "krude", UserID: asha.UserID, SessionID: thread.ID})
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := after.Session.State().Get("loaded_tools")
	if err != nil {
		t.Fatal(err)
	}
	if names := storedNames(loaded); !slices.Equal(names, []string{"diary", "notes"}) {
		t.Fatalf("the thread has %v loaded, want diary and notes", loaded)
	}
}

// storedNames reads a stored list of names, whichever slice type it came
// back as.
func storedNames(value any) []string {
	switch list := value.(type) {
	case []string:
		return list
	case []any:
		var names []string
		for _, name := range list {
			if text, ok := name.(string); ok {
				names = append(names, text)
			}
		}
		return names
	}
	return nil
}

// TestARunDeliveringAnInteractionRecordsWhichOne: a run started to deliver
// one interaction records its request in the thread's state as it starts,
// which is where its interactions tools read what they may act on; a run
// that delivers nothing records nothing.
func TestARunDeliveringAnInteractionRecordsWhichOne(t *testing.T) {
	ctx := context.Background()
	llm := &scriptedModel{respond: func(*model.LLMRequest) (*genai.Content, error) {
		return genai.NewContentFromText("Done.", genai.RoleModel), nil
	}}
	router, err := modelrouter.New(llm, llm)
	if err != nil {
		t.Fatal(err)
	}
	krude, err := primaryagents.UnattendedRoot(router, nil)
	if err != nil {
		t.Fatal(err)
	}
	prices, err := pricing.NewTable(nil)
	if err != nil {
		t.Fatal(err)
	}
	sessions := session.InMemoryService()
	service, err := New(Config{
		AppName: "krude", Agent: krude, Sessions: sessions, Artifacts: artifact.InMemoryService(),
		Runs: newFakeRuns(), Prices: prices, RunTimeout: time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}
	delivery := primaryagents.Delivery{RequestID: "irq_1", Purpose: primaryagents.DeliveryDecides}
	for _, delivered := range []primaryagents.Delivery{delivery, {}} {
		thread, err := service.Create(ctx, asha)
		if err != nil {
			t.Fatal(err)
		}
		run, err := service.StartRun(ctx, asha, thread.ID, RunInput{Message: "Deliver it.", Delivery: delivered})
		if err != nil {
			t.Fatal(err)
		}
		run.Execute(ctx, func(Item) error { return nil })
		after, err := sessions.Get(ctx, &session.GetRequest{AppName: "krude", UserID: asha.UserID, SessionID: thread.ID})
		if err != nil {
			t.Fatal(err)
		}
		for key, want := range primaryagents.DeliveryState(delivery) {
			got, err := after.Session.State().Get(key)
			if delivered.RequestID == "" && err == nil {
				t.Fatalf("delivering nothing, the thread records %v under %s", got, key)
			}
			if delivered.RequestID != "" && got != want {
				t.Fatalf("delivering %+v, the thread records %v under %s, want %v", delivered, got, key, want)
			}
		}
	}
}
