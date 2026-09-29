package agents

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"

	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/artifact"
	"google.golang.org/adk/v2/model"
	"google.golang.org/adk/v2/runner"
	"google.golang.org/adk/v2/session"
	"google.golang.org/genai"

	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/modelrouter"
)

func TestScreenContextBoundsAndEncoding(t *testing.T) {
	screen := ScreenContext{
		View:         "team/teamspace",
		Selection:    &ScreenItem{Kind: "teamspace", ID: "space_1", Name: "Launch"},
		VisibleItems: []ScreenItem{{Kind: "person", ID: "user_1", Name: "Ada"}},
	}
	if !screen.Valid() {
		t.Fatal("a bounded Oryxa view should be valid")
	}
	var decoded ScreenContext
	if err := json.Unmarshal([]byte(EncodedScreenContext(&screen)), &decoded); err != nil || decoded.View != screen.View {
		t.Fatalf("encoded screen context did not round trip: %+v, %v", decoded, err)
	}
	if EncodedScreenContext(nil) != "" {
		t.Fatal("a run without screen context must clear the prior snapshot")
	}
	for _, invalid := range []ScreenContext{
		{View: "other/window"},
		{View: screen.View, VisibleItems: make([]ScreenItem, 13)},
		{View: screen.View, Selection: &ScreenItem{Kind: "person", ID: strings.Repeat("x", 121)}},
		{View: screen.View, VisibleItems: []ScreenItem{{Kind: "person", ID: "user_1", Name: "A\nB"}}},
	} {
		if invalid.Valid() {
			t.Fatalf("accepted invalid screen context: %+v", invalid)
		}
	}
}

// screenReader plays Krude for a run that has to read the member's screen: it
// calls get_current_screen for the member's message, and answers once the tool
// has answered. It looks at the last turn only, so a thread's earlier runs do
// not make it skip the tool.
func screenReader(final string) *treeModel {
	return newTreeModel(func(_ string, request *model.LLMRequest) *genai.Content {
		if last := request.Contents[len(request.Contents)-1]; len(last.Parts) > 0 && last.Parts[0].FunctionResponse != nil {
			return genai.NewContentFromText(final, genai.RoleModel)
		}
		return functionCalls(&genai.FunctionCall{ID: "call_1", Name: "get_current_screen", Args: map[string]any{}})
	})
}

// A snapshot describes what the member had open when they sent one message.
// It has to reach the tool during that run, and nowhere after: not in the
// session's state, and not in the actions of an event stored with the thread.
// ADK's in-memory session service keeps an event's actions whole, so the
// event half of that is checked against PostgreSQL, the service Oryxa runs on,
// in adksession's TestPostgresTemporaryStateIsNeverStored.
func TestTheScreenSnapshotReachesItsRunAndIsNotKept(t *testing.T) {
	if !strings.HasPrefix(ScreenStateKey, session.KeyPrefixTemp) {
		t.Fatalf("ScreenStateKey = %q, want ADK temporary state (%q prefix), which is never stored", ScreenStateKey, session.KeyPrefixTemp)
	}
	screen := ScreenContext{
		View:         "team/teamspace",
		Selection:    &ScreenItem{Kind: "teamspace", ID: "space_1", Name: "Launch"},
		VisibleItems: []ScreenItem{{Kind: "person", ID: "user_1", Name: "Ada"}},
	}
	llm := screenReader("You are looking at Launch.")
	h := newHarness(t, llm)
	ctx := context.Background()
	created, err := h.sessions.Create(ctx, &session.CreateRequest{AppName: RootName, UserID: "member_1"})
	if err != nil {
		t.Fatal(err)
	}
	delta := map[string]any{ScreenStateKey: EncodedScreenContext(&screen)}
	for _, err := range h.turns.Run(ctx, "member_1", created.Session.ID(), genai.NewContentFromText("What is this?", genai.RoleUser), agent.RunConfig{}, runner.WithStateDelta(delta)) {
		if err != nil {
			t.Fatalf("run: %v", err)
		}
	}

	requests := llm.requestsFor(RootName)
	if len(requests) != 2 {
		t.Fatalf("Krude made %d model calls, want the tool call and the answer", len(requests))
	}
	seen := functionResponse(requests[1], "get_current_screen")
	for _, want := range []string{"available:true", "team/teamspace", "Launch", "Ada"} {
		if !strings.Contains(seen, want) {
			t.Fatalf("the tool answered %q during the run, want it to carry %q", seen, want)
		}
	}

	reloaded, err := h.sessions.Get(ctx, &session.GetRequest{AppName: RootName, UserID: "member_1", SessionID: created.Session.ID()})
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{ScreenStateKey, "current_screen_context"} {
		if value, err := reloaded.Session.State().Get(key); !errors.Is(err, session.ErrStateKeyNotExist) {
			t.Fatalf("state key %q survived the run with %v (%v)", key, value, err)
		}
	}
}

// A run's snapshot is only what its own message brought: the next run of the
// same thread finds none unless it brings one.
func TestARunWithoutASnapshotFindsNoScreen(t *testing.T) {
	llm := screenReader("I can't see a page.")
	h := newHarness(t, llm)
	ctx := context.Background()
	created, err := h.sessions.Create(ctx, &session.CreateRequest{AppName: RootName, UserID: "member_1"})
	if err != nil {
		t.Fatal(err)
	}
	screen := ScreenContext{View: "home/overview", VisibleItems: []ScreenItem{}}
	turn := func(message string, delta map[string]any) {
		t.Helper()
		for _, err := range h.turns.Run(ctx, "member_1", created.Session.ID(), genai.NewContentFromText(message, genai.RoleUser), agent.RunConfig{}, runner.WithStateDelta(delta)) {
			if err != nil {
				t.Fatalf("run: %v", err)
			}
		}
	}
	turn("What is this?", map[string]any{ScreenStateKey: EncodedScreenContext(&screen)})
	turn("And now?", map[string]any{ScreenStateKey: EncodedScreenContext(nil)})

	requests := llm.requestsFor(RootName)
	if len(requests) != 4 {
		t.Fatalf("Krude made %d model calls, want two for each of two runs", len(requests))
	}
	if first := functionResponse(requests[1], "get_current_screen"); !strings.Contains(first, "available:true") {
		t.Fatalf("the first run's tool answered %q, want its snapshot", first)
	}
	// Both runs' tool answers are in the last request's history; the second
	// one is the last of them.
	answers := strings.Split(functionResponse(requests[3], "get_current_screen"), " | ")
	if len(answers) != 2 || strings.Contains(answers[1], "available:true") {
		t.Fatalf("tool answers over the thread = %q, want the second run's to say nothing is available", answers)
	}
}

// What steers an unattended, room or delivery run is what other people wrote,
// and its answer may reach them. The member's screen is not theirs to read: the
// tool, and the words that point to it, stay in the member's own runs.
func TestOnlyTheMembersOwnRunsCanReadTheirScreen(t *testing.T) {
	spec := ManagedSpec{ID: "agt_x", Name: "X", Description: "Answers.", Instructions: "Answer the member."}
	for _, test := range []struct {
		name  string
		build func(*modelrouter.Router) (agent.Agent, error)
		reads bool
	}{
		{"Krude", func(router *modelrouter.Router) (agent.Agent, error) { return Root(router, nil, false) }, true},
		{"a specialist", func(router *modelrouter.Router) (agent.Agent, error) { return Managed(router, nil, false, spec) }, true},
		{"unattended Krude", func(router *modelrouter.Router) (agent.Agent, error) { return UnattendedRoot(router, nil) }, false},
		{"Krude in a room", func(router *modelrouter.Router) (agent.Agent, error) { return RoomRoot(router, nil) }, false},
		{"Krude delivering a request", func(router *modelrouter.Router) (agent.Agent, error) {
			return DeliveringRoot(router, nil, "")
		}, false},
		{"a specialist in a room", func(router *modelrouter.Router) (agent.Agent, error) { return ManagedRoom(router, nil, spec) }, false},
		{"a specialist delivering a request", func(router *modelrouter.Router) (agent.Agent, error) {
			return ManagedDelivering(router, nil, spec, "")
		}, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			// A specialist must answer in its result envelope; Krude takes any text.
			llm := newTreeModel(func(string, *model.LLMRequest) *genai.Content {
				return genai.NewContentFromText(`{"status":"completed","summary":"ok","tools_used":[],"artifacts":[],"handoffs":[]}`, genai.RoleModel)
			})
			router, err := modelrouter.New(llm, llm)
			if err != nil {
				t.Fatal(err)
			}
			root, err := test.build(router)
			if err != nil {
				t.Fatal(err)
			}
			sessions := session.InMemoryService()
			turns, err := runner.New(runner.Config{AppName: root.Name(), Agent: root, SessionService: sessions, ArtifactService: artifact.InMemoryService()})
			if err != nil {
				t.Fatal(err)
			}
			created, err := sessions.Create(context.Background(), &session.CreateRequest{AppName: root.Name(), UserID: "member_1"})
			if err != nil {
				t.Fatal(err)
			}
			for _, err := range turns.Run(context.Background(), "member_1", created.Session.ID(), genai.NewContentFromText("hi", genai.RoleUser), agent.RunConfig{}) {
				if err != nil {
					t.Fatal(err)
				}
			}
			var declared []string
			var instruction strings.Builder
			for _, requests := range llm.requests {
				for _, request := range requests {
					declared = append(declared, declaredFunctions(request)...)
					instruction.WriteString(text(request.Config.SystemInstruction))
				}
			}
			if len(declared) == 0 && test.reads {
				t.Fatal("the run offered the model no tools, so nothing was checked")
			}
			if got := slices.Contains(declared, "get_current_screen"); got != test.reads {
				t.Fatalf("get_current_screen offered = %v, want %v (tools %v)", got, test.reads, declared)
			}
			if got := strings.Contains(instruction.String(), "get_current_screen"); got != test.reads {
				t.Fatalf("instruction mentions get_current_screen = %v, want %v", got, test.reads)
			}
		})
	}
}
