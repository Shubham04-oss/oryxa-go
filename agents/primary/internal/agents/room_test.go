package agents

import (
	"context"
	"strings"
	"testing"

	"google.golang.org/adk/v2/artifact"
	"google.golang.org/adk/v2/model"
	"google.golang.org/adk/v2/runner"
	"google.golang.org/adk/v2/session"
	"google.golang.org/genai"

	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/memberauth"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/modelrouter"
)

// roomHarness runs Krude as a turn in a room runs it.
func roomHarness(t *testing.T, llm model.LLM, toolboxes ...Toolbox) *harness {
	t.Helper()
	router, err := modelrouter.New(llm, llm)
	if err != nil {
		t.Fatal(err)
	}
	root, err := RoomRoot(router, toolboxes)
	if err != nil {
		t.Fatal(err)
	}
	sessions := session.InMemoryService()
	turns, err := runner.New(runner.Config{AppName: RootName, Agent: root, SessionService: sessions, ArtifactService: artifact.InMemoryService()})
	if err != nil {
		t.Fatal(err)
	}
	return &harness{t: t, sessions: sessions, turns: turns}
}

// askingOwner files a question with the member once, then says what it got.
func askingOwner() *treeModel {
	return newTreeModel(func(_ string, request *model.LLMRequest) *genai.Content {
		if hasFunctionResponse(request) {
			return genai.NewContentFromText("Filed: "+functionResponse(request, "room_ask_owner"), genai.RoleModel)
		}
		return functionCalls(&genai.FunctionCall{ID: "ask", Name: "room_ask_owner", Args: map[string]any{
			"purpose": "Bo asked about Northwind's renewal in the room.", "task": "What does your CRM say about Northwind's renewal?",
			"requested_tools": []any{"apps"},
		}})
	})
}

// adasRoomTurn is the session of Ada's agent taking a turn in a room.
func adasRoomTurn() map[string]any {
	return map[string]any{OrganizationIDKey: "org_1", AgentIDKey: "agt_ada", AgentKindKey: "primary"}
}

// TestAQuestionTheRoomCannotAnswerIsFiledWithItsOwner: a turn in a room
// files what it cannot answer with the member whose agent is taking it, as a
// request from the member whose message started the exchange — through their
// own agent, with the token of the session they posted in, made in the room
// — which the model never names. A run no room started files nothing, nor
// does one its own member started, or one from another organization's room.
func TestAQuestionTheRoomCannotAnswerIsFiledWithItsOwner(t *testing.T) {
	bo := RoomAsker{RoomID: "tsp_room", OrganizationID: "org_1", UserID: "user_bo", AgentID: "agt_bo", AgentName: "Cass", AgentKind: "primary"}
	service := &fakeInteractions{}
	room, err := RoomToolbox(RoomConfig{Service: service})
	if err != nil {
		t.Fatal(err)
	}
	llm := askingOwner()
	// Ada's own token would be on a run she started; the request is Bo's.
	ctx := WithRoomAsker(memberauth.WithAccessToken(context.Background(), "ada-token"), bo, "bo-token")
	said := roomHarness(t, llm, room).turnWith(ctx, "user_ada", adasRoomTurn(), "@mira what does your CRM say about Northwind?")
	if !strings.Contains(said, "irq_1") {
		t.Fatalf("the turn said %q, want the request it filed", said)
	}
	if service.actor != (InteractionActor{OrganizationID: "org_1", UserID: "user_bo", AgentID: "agt_bo", AgentName: "Cass", AgentKind: "primary", Session: SessionAgent}) ||
		service.token != "bo-token" || service.key == "" {
		t.Fatalf("filed as %+v with token %q and key %q, want Bo's agent in Bo's session", service.actor, service.token, service.key)
	}
	if service.input.TargetAgentID != "agt_ada" || service.input.TeamspaceID != "tsp_room" ||
		service.input.Task != "What does your CRM say about Northwind's renewal?" || len(service.input.RequestedTools) != 1 {
		t.Fatalf("filed %+v, want a request to Ada's agent in the room", service.input)
	}

	for name, ctx := range map[string]context.Context{
		"a run no room started":          context.Background(),
		"a turn its own member began":    WithRoomAsker(context.Background(), RoomAsker{RoomID: "tsp_room", OrganizationID: "org_1", UserID: "user_ada", AgentID: "agt_ada", AgentName: "Mira", AgentKind: "primary"}, "ada-token"),
		"a room in another organization": WithRoomAsker(context.Background(), RoomAsker{RoomID: "tsp_other", OrganizationID: "org_2", UserID: "user_bo", AgentID: "agt_bo", AgentName: "Cass", AgentKind: "primary"}, "bo-token"),
	} {
		refused := &fakeInteractions{}
		room, err := RoomToolbox(RoomConfig{Service: refused})
		if err != nil {
			t.Fatal(err)
		}
		roomHarness(t, askingOwner(), room).turnWith(ctx, "user_ada", adasRoomTurn(), "@mira?")
		if refused.key != "" {
			t.Fatalf("%s filed a request as %+v", name, refused.actor)
		}
	}
}

func TestRoomToolboxNeedsAService(t *testing.T) {
	if _, err := RoomToolbox(RoomConfig{}); err == nil {
		t.Fatal("RoomToolbox() accepted no service")
	}
}
