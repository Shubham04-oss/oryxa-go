package threads

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/agent/llmagent"
	"google.golang.org/adk/v2/model"
	"google.golang.org/adk/v2/session"
	"google.golang.org/adk/v2/tool"
	"google.golang.org/adk/v2/tool/functiontool"
	"google.golang.org/genai"

	primaryagents "github.com/Shubham04-oss/oryxa-go/agents/primary/internal/agents"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/pricing"
)

// pinHarness is a service whose agent has one tool, which keeps the teamspace
// its run is pinned to as a tool of the agents package would see it, and
// answers with nothing about it: what the thread holds afterwards is only what
// the framework itself kept.
type pinHarness struct {
	*harness
	mu   sync.Mutex
	seen []string
}

// pinned is the teamspace each run's tool saw, in the order the runs called
// it, "" for a run with none.
func (h *pinHarness) pinned() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]string(nil), h.seen...)
}

func newPinHarness(t *testing.T) *pinHarness {
	t.Helper()
	h := &pinHarness{}
	which, err := functiontool.New(functiontool.Config{Name: "which_teamspace", Description: "Looks at the teamspace this run works in."},
		func(ctx agent.Context, _ struct{}) (map[string]any, error) {
			id, _ := primaryagents.PinnedTeamspace(ctx)
			h.mu.Lock()
			h.seen = append(h.seen, id)
			h.mu.Unlock()
			return map[string]any{"looked": true}, nil
		})
	if err != nil {
		t.Fatal(err)
	}
	llm := &scriptedModel{respond: func(request *model.LLMRequest) (*genai.Content, error) {
		if lastFunctionResponse(request) != nil {
			return genai.NewContentFromText("Done.", genai.RoleModel), nil
		}
		return &genai.Content{Role: genai.RoleModel, Parts: []*genai.Part{{FunctionCall: &genai.FunctionCall{ID: "call_1", Name: "which_teamspace"}}}}, nil
	}}
	krude, err := llmagent.New(llmagent.Config{Name: "krude", Model: llm, Instruction: "You are Krude.", Tools: []tool.Tool{which}})
	if err != nil {
		t.Fatal(err)
	}
	prices, err := pricing.NewTable(nil)
	if err != nil {
		t.Fatal(err)
	}
	runs := newFakeRuns()
	sessions := session.InMemoryService()
	service, err := New(Config{AppName: "krude", Agent: krude, Sessions: sessions, Runs: runs, Prices: prices})
	if err != nil {
		t.Fatal(err)
	}
	h.harness = &harness{t: t, service: service, runs: runs, model: llm, sessions: sessions}
	return h
}

// rendered is whatever a value holds, as text, for looking for a string in it.
func rendered(value any) string {
	if encoded, err := json.Marshal(value); err == nil {
		return string(encoded)
	}
	return fmt.Sprintf("%+v", value)
}

// TestARunPinnedToATeamspaceCarriesItOnItsContextAlone: a run started with a
// teamspace's id has it on the context its tools run on, trimmed, and it is
// that run's alone: it is in no session state, no event of the thread, no record
// of the run and no event announcing it, and the next run of the same thread,
// started without one, has no pin. Pinning the next run is its caller's to do
// again.
func TestARunPinnedToATeamspaceCarriesItOnItsContextAlone(t *testing.T) {
	h := newPinHarness(t)
	created, err := h.service.Create(context.Background(), asha)
	if err != nil {
		t.Fatal(err)
	}

	items, run := h.run(asha, created.ID, RunInput{Message: "Ask the launch room", TeamspaceID: "  tsp_launch "})
	if got := h.pinned(); len(got) != 1 || got[0] != "tsp_launch" {
		t.Fatalf("the first run's tool saw the pin %q, want tsp_launch", got)
	}
	if results := find(items, ItemToolResult); len(results) != 1 || results[0].Failed {
		t.Fatalf("the tool's result = %+v", results)
	}

	stored, err := h.sessions.Get(context.Background(), &session.GetRequest{AppName: "krude", UserID: asha.UserID, SessionID: created.ID})
	if err != nil {
		t.Fatal(err)
	}
	for key, value := range stored.Session.State().All() {
		if strings.Contains(strings.ToLower(key), "teamspace") || strings.Contains(rendered(value), "tsp_launch") {
			t.Errorf("the thread's state holds %q = %v", key, value)
		}
	}
	for event := range stored.Session.Events().All() {
		if strings.Contains(rendered(event), "tsp_launch") {
			t.Errorf("an event of the thread holds the pin: %s", rendered(event))
		}
	}
	for what, held := range map[string]any{
		"the run's record": h.runs.started[0], "the run's outcome": h.runs.finished[run.ID()], "the run's event": string(h.runs.events[run.ID()].Data),
		"what the member was streamed": items,
	} {
		if strings.Contains(rendered(held), "tsp_launch") {
			t.Errorf("%s holds the pin: %s", what, rendered(held))
		}
	}

	// The next run, started without one, has none; a run started with another
	// has that one.
	h.run(asha, created.ID, RunInput{Message: "And now nothing in particular"})
	h.run(asha, created.ID, RunInput{Message: "The sales room", TeamspaceID: "tsp_sales"})
	if got := h.pinned(); len(got) != 3 || got[0] != "tsp_launch" || got[1] != "" || got[2] != "tsp_sales" {
		t.Fatalf("the runs' tools saw the pins %q, want tsp_launch, none, tsp_sales", got)
	}
}

// TestARunPinnedToNothingIsUnpinned: no id means no pin.
func TestARunPinnedToNothingIsUnpinned(t *testing.T) {
	h := newPinHarness(t)
	created, err := h.service.Create(context.Background(), asha)
	if err != nil {
		t.Fatal(err)
	}
	h.run(asha, created.ID, RunInput{Message: "Ask the launch room", TeamspaceID: ""})
	if got := h.pinned(); len(got) != 1 || got[0] != "" {
		t.Fatalf("a run started without a pin saw %q, want none", got)
	}
}

// TestARunWhosePinIsNotATeamspaceIdNeverStarts: a pin of only spaces, one that
// holds whitespace, and one too long are the caller's mistake, refused as
// invalid before the run takes its thread, so it is free for the next one.
func TestARunWhosePinIsNotATeamspaceIdNeverStarts(t *testing.T) {
	h := newPinHarness(t)
	created, err := h.service.Create(context.Background(), asha)
	if err != nil {
		t.Fatal(err)
	}
	for name, id := range map[string]string{
		"only spaces":         "   ",
		"a space inside":      "tsp launch",
		"a newline inside":    "tsp_launch\ntsp_other",
		"129 characters long": strings.Repeat("t", 129),
	} {
		if _, err := h.service.StartRun(context.Background(), asha, created.ID, RunInput{Message: "hi", TeamspaceID: id}); !errors.Is(err, ErrInvalid) {
			t.Errorf("a pin with %s = %v, want ErrInvalid", name, err)
		}
	}
	if len(h.runs.started) != 0 || len(h.pinned()) != 0 {
		t.Fatalf("a run started for an invalid pin: started=%d, tool saw %q", len(h.runs.started), h.pinned())
	}
	h.run(asha, created.ID, RunInput{Message: "hi", TeamspaceID: strings.Repeat("t", 128)})
	if got := h.pinned(); len(got) != 1 || len(got[0]) != 128 {
		t.Fatalf("a pin of 128 characters was not carried: %q", got)
	}
}
