package agents

import (
	"context"
	"strings"
	"testing"

	"google.golang.org/adk/v2/model"
	"google.golang.org/genai"

	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/memberauth"
)

type fakeTeamspaceCollaboration struct {
	actor      TeamspaceActor
	token      string
	id         string
	input      SharedEntryInput
	spaces     []Teamspace
	entries    []SharedEntry
	listCalls  int
	entryCalls int
}

func (service *fakeTeamspaceCollaboration) CollaborationSpaces(_ context.Context, actor TeamspaceActor, token string) ([]Teamspace, error) {
	service.actor, service.token = actor, token
	service.listCalls++
	return service.spaces, nil
}

func (service *fakeTeamspaceCollaboration) CollaborationEntries(_ context.Context, actor TeamspaceActor, token, id string, _ int) ([]SharedEntry, error) {
	service.actor, service.token, service.id = actor, token, id
	service.entryCalls++
	return service.entries, nil
}

func teamspaceContextLLM() model.LLM {
	return newTreeModel(func(_ string, request *model.LLMRequest) *genai.Content {
		switch {
		case functionResponse(request, "teamspaces_context") != "":
			return genai.NewContentFromText("Queue capacity needs an owner.", genai.RoleModel)
		case functionResponse(request, "load_tools") != "":
			return functionCalls(&genai.FunctionCall{ID: "read_context", Name: "teamspaces_context", Args: map[string]any{"query": "Launch Room"}})
		default:
			return functionCalls(&genai.FunctionCall{ID: "load_teamspaces", Name: "load_tools", Args: map[string]any{"names": []any{"teamspaces"}}})
		}
	})
}

func TestTeamspaceContextFindsAndReadsNamedRoomInOneToolCall(t *testing.T) {
	service := &fakeTeamspaceCollaboration{
		spaces:  []Teamspace{{ID: "tsp_launch", Name: "Launch Room", TeamName: "Launch Council", Status: "active"}},
		entries: []SharedEntry{{ID: "tse_1", TeamspaceID: "tsp_launch", Summary: "Capacity remains open."}},
	}
	box, err := TeamspaceToolbox(TeamspaceConfig{Service: service})
	if err != nil {
		t.Fatal(err)
	}
	h := newHarness(t, teamspaceContextLLM(), box)
	ctx := memberauth.WithAccessToken(context.Background(), "member-token")
	answer := h.turnWith(ctx, "user_a", map[string]any{
		OrganizationIDKey: "org_1", AgentIDKey: "agt_a", AgentNameKey: "Carl", AgentKindKey: "primary",
	}, "Review the Launch Room")
	if !strings.Contains(answer, "capacity") || service.listCalls != 1 || service.entryCalls != 1 || service.id != "tsp_launch" {
		t.Fatalf("answer=%q lists=%d reads=%d id=%q", answer, service.listCalls, service.entryCalls, service.id)
	}
}

func TestSelectTeamspaceRequiresOneSemanticMatch(t *testing.T) {
	spaces := []Teamspace{{ID: "tsp_launch", Name: "Launch Room", TeamName: "Launch Council"}, {ID: "tsp_sales", Name: "Sales Room", TeamName: "Sales"}}
	if got, err := selectTeamspace(spaces, "launch"); err != nil || got.ID != "tsp_launch" {
		t.Fatalf("semantic match=%+v error=%v", got, err)
	}
	if _, err := selectTeamspace(spaces, "room"); err == nil || !strings.Contains(err.Error(), "ambiguous") {
		t.Fatalf("ambiguous error=%v", err)
	}
	if _, err := selectTeamspace(spaces, "missing"); err == nil || !strings.Contains(err.Error(), "no accessible") {
		t.Fatalf("missing error=%v", err)
	}
}

func (service *fakeTeamspaceCollaboration) CollaborationPublish(_ context.Context, actor TeamspaceActor, token, id string, input SharedEntryInput) (SharedEntry, error) {
	service.actor, service.token, service.id, service.input = actor, token, id, input
	return SharedEntry{
		ID: "tse_1", TeamspaceID: id, AuthorUserID: actor.UserID,
		AuthorAgentID: actor.AgentID, AuthorAgentName: actor.AgentName,
		Kind: input.Kind, Title: input.Title, Summary: input.Summary,
	}, nil
}

func teamspaceLLM() model.LLM {
	return newTreeModel(func(_ string, request *model.LLMRequest) *genai.Content {
		switch {
		case functionResponse(request, "teamspaces_publish") != "":
			return genai.NewContentFromText("Published "+functionResponse(request, "teamspaces_publish"), genai.RoleModel)
		case functionResponse(request, "load_tools") != "":
			return functionCalls(&genai.FunctionCall{ID: "publish_result", Name: "teamspaces_publish", Args: map[string]any{
				"teamspace_id": "tsp_1", "kind": "result", "title": "Architecture review",
				"summary": "Ready to proceed.", "tools_used": []any{"github.search"},
			}})
		default:
			return functionCalls(&genai.FunctionCall{ID: "load_teamspaces", Name: "load_tools", Args: map[string]any{"names": []any{"teamspaces"}}})
		}
	})
}

func TestTeamspaceToolboxDerivesWhispererAuthorship(t *testing.T) {
	service := &fakeTeamspaceCollaboration{}
	box, err := TeamspaceToolbox(TeamspaceConfig{Service: service})
	if err != nil {
		t.Fatal(err)
	}
	h := newHarness(t, teamspaceLLM(), box)
	ctx := memberauth.WithAccessToken(context.Background(), "member-token")
	answer := h.turnWith(ctx, "user_a", map[string]any{
		OrganizationIDKey: "org_1", AgentIDKey: "agt_a", AgentNameKey: "Carl", AgentKindKey: "primary",
	}, "Publish the completed review")
	if !strings.Contains(answer, "tse_1") || service.actor.UserID != "user_a" || service.actor.AgentID != "agt_a" ||
		service.actor.AgentKind != "primary" || service.token != "member-token" || service.id != "tsp_1" ||
		service.input.Title != "Architecture review" {
		t.Fatalf("answer=%q actor=%+v token=%q id=%q input=%+v", answer, service.actor, service.token, service.id, service.input)
	}
}

func TestTeamspaceToolboxNeedsAService(t *testing.T) {
	if _, err := TeamspaceToolbox(TeamspaceConfig{}); err == nil {
		t.Fatal("TeamspaceToolbox() accepted no service")
	}
}

// pinnedTeamspaces is a collaboration service over two teamspaces that keeps
// which teamspace every read and every publication was asked for.
type pinnedTeamspaces struct {
	spaces    []Teamspace
	read      []string
	published []string
}

func newPinnedTeamspaces() *pinnedTeamspaces {
	return &pinnedTeamspaces{spaces: []Teamspace{
		{ID: "tsp_launch", Name: "Launch Room", TeamName: "Launch Council", Status: "active"},
		{ID: "tsp_sales", Name: "Sales Room", TeamName: "Revenue", Status: "active"},
	}}
}

func (service *pinnedTeamspaces) CollaborationSpaces(context.Context, TeamspaceActor, string) ([]Teamspace, error) {
	return service.spaces, nil
}

func (service *pinnedTeamspaces) CollaborationEntries(_ context.Context, _ TeamspaceActor, _, id string, _ int) ([]SharedEntry, error) {
	service.read = append(service.read, id)
	return []SharedEntry{{ID: "tse_" + id, TeamspaceID: id, Summary: "Something shared in " + id}}, nil
}

func (service *pinnedTeamspaces) CollaborationPublish(_ context.Context, actor TeamspaceActor, _, id string, input SharedEntryInput) (SharedEntry, error) {
	service.published = append(service.published, id)
	return SharedEntry{ID: "tse_new", TeamspaceID: id, AuthorAgentID: actor.AgentID, Kind: input.Kind, Title: input.Title, Summary: input.Summary}, nil
}

// usingTeamspaceTool has the agent call one teamspaces tool with args, in a run
// on ctx, and returns what the tool answered.
func usingTeamspaceTool(t *testing.T, ctx context.Context, service TeamspaceService, toolName string, args map[string]any) string {
	t.Helper()
	box, err := TeamspaceToolbox(TeamspaceConfig{Service: service})
	if err != nil {
		t.Fatal(err)
	}
	return newHarness(t, callingOnce("teamspaces", toolName, args), box).turnWith(
		memberauth.WithAccessToken(ctx, "member-token"), "user_a", map[string]any{
			OrganizationIDKey: "org_1", AgentIDKey: "agt_a", AgentNameKey: "Carl", AgentKindKey: "primary",
		}, "Use the shared teamspace")
}

// TestAPinnedRunReadsAndPublishesInItsTeamspaceOrNotAtAll: teamspaces_entries
// and teamspaces_publish work in the pinned teamspace whether the model names
// it, pads it, or leaves it out, and refuse any other one without touching
// the service or telling the model which one the run is pinned to. An unpinned
// run works in what it names, as it always did.
func TestAPinnedRunReadsAndPublishesInItsTeamspaceOrNotAtAll(t *testing.T) {
	pinned := WithPinnedTeamspace(context.Background(), "tsp_launch")
	publication := func(named map[string]any) map[string]any {
		args := map[string]any{"kind": "result", "title": "Review", "summary": "One bounded finding."}
		for name, value := range named {
			args[name] = value
		}
		return args
	}

	// Both tools declare teamspace_id as required, so "none named" is the
	// model sending it blank.
	for name, named := range map[string]map[string]any{
		"none named":               {"teamspace_id": ""},
		"the pinned named":         {"teamspace_id": "tsp_launch"},
		"the pinned named, padded": {"teamspace_id": " tsp_launch "},
	} {
		service := newPinnedTeamspaces()
		usingTeamspaceTool(t, pinned, service, "teamspaces_entries", named)
		if len(service.read) != 1 || service.read[0] != "tsp_launch" {
			t.Errorf("teamspaces_entries, %s: read %v, want tsp_launch alone", name, service.read)
		}
		service = newPinnedTeamspaces()
		usingTeamspaceTool(t, pinned, service, "teamspaces_publish", publication(named))
		if len(service.published) != 1 || service.published[0] != "tsp_launch" {
			t.Errorf("teamspaces_publish, %s: published in %v, want tsp_launch alone", name, service.published)
		}
	}

	for toolName, args := range map[string]map[string]any{
		"teamspaces_entries": {"teamspace_id": "tsp_sales"},
		"teamspaces_publish": publication(map[string]any{"teamspace_id": "tsp_sales"}),
	} {
		service := newPinnedTeamspaces()
		answer := usingTeamspaceTool(t, pinned, service, toolName, args)
		if len(service.read) != 0 || len(service.published) != 0 {
			t.Errorf("%s named another teamspace and read %v, published in %v", toolName, service.read, service.published)
		}
		if !strings.Contains(answer, "error") || !strings.Contains(answer, "one teamspace") || strings.Contains(answer, "tsp_launch") {
			t.Errorf("%s told the model %q, want a refusal that says the conversation is about one teamspace and not which", toolName, answer)
		}
	}

	service := newPinnedTeamspaces()
	usingTeamspaceTool(t, context.Background(), service, "teamspaces_entries", map[string]any{"teamspace_id": " tsp_sales "})
	usingTeamspaceTool(t, context.Background(), service, "teamspaces_publish", publication(map[string]any{"teamspace_id": " tsp_sales "}))
	if len(service.read) != 1 || service.read[0] != "tsp_sales" || len(service.published) != 1 || service.published[0] != "tsp_sales" {
		t.Errorf("an unpinned run read %v and published in %v, want what it named", service.read, service.published)
	}
}

// TestAPinnedRunSeesOnlyItsTeamspaceInTheList: teamspaces_list shows a pinned
// run the one teamspace it is pinned to and none other, an empty list when the
// agent cannot work in that one, and an unpinned run every teamspace it may
// work in.
func TestAPinnedRunSeesOnlyItsTeamspaceInTheList(t *testing.T) {
	answer := usingTeamspaceTool(t, context.Background(), newPinnedTeamspaces(), "teamspaces_list", map[string]any{})
	if !strings.Contains(answer, "tsp_launch") || !strings.Contains(answer, "tsp_sales") {
		t.Fatalf("an unpinned run was listed %q, want both teamspaces", answer)
	}
	answer = usingTeamspaceTool(t, WithPinnedTeamspace(context.Background(), "tsp_sales"), newPinnedTeamspaces(), "teamspaces_list", map[string]any{})
	if !strings.Contains(answer, "tsp_sales") || strings.Contains(answer, "tsp_launch") {
		t.Fatalf("a run pinned to tsp_sales was listed %q, want that one alone", answer)
	}
	answer = usingTeamspaceTool(t, WithPinnedTeamspace(context.Background(), "tsp_elsewhere"), newPinnedTeamspaces(), "teamspaces_list", map[string]any{})
	if strings.Contains(answer, "tsp_") || !strings.Contains(answer, "teamspaces:[]") {
		t.Fatalf("a run pinned to a teamspace the agent cannot work in was listed %q, want an empty list", answer)
	}
}

// TestAPinnedRunReadsTheContextOfItsTeamspaceAlone: teamspaces_context finds a
// teamspace among the pinned one alone, so a query that names none finds the
// pinned one even though the agent may work in two, and one that names any other
// — by its id, its name or its team's — finds nothing and reads nothing.
func TestAPinnedRunReadsTheContextOfItsTeamspaceAlone(t *testing.T) {
	pinned := WithPinnedTeamspace(context.Background(), "tsp_launch")
	for name, query := range map[string]string{
		"no query":              "",
		"its name":              "Launch Room",
		"its team":              "launch",
		"its id, in any case":   "TSP_LAUNCH",
		"a query that is blank": "   ",
	} {
		service := newPinnedTeamspaces()
		answer := usingTeamspaceTool(t, pinned, service, "teamspaces_context", map[string]any{"query": query})
		if len(service.read) != 1 || service.read[0] != "tsp_launch" || !strings.Contains(answer, "Something shared in tsp_launch") {
			t.Errorf("%s: read %v and answered %q, want the pinned teamspace", name, service.read, answer)
		}
	}

	for name, query := range map[string]string{"its id": "tsp_sales", "its name": "Sales Room", "its team": "Revenue"} {
		service := newPinnedTeamspaces()
		answer := usingTeamspaceTool(t, pinned, service, "teamspaces_context", map[string]any{"query": query})
		if len(service.read) != 0 || !strings.Contains(answer, "error") || !strings.Contains(answer, "one teamspace only") ||
			strings.Contains(answer, "tsp_launch") || strings.Contains(answer, "Something shared") {
			t.Errorf("another teamspace by %s: read %v and answered %q, want a refusal that reads nothing", name, service.read, answer)
		}
	}

	service := newPinnedTeamspaces()
	answer := usingTeamspaceTool(t, WithPinnedTeamspace(context.Background(), "tsp_elsewhere"), service, "teamspaces_context", map[string]any{"query": ""})
	if len(service.read) != 0 || !strings.Contains(answer, "not one this agent can work in") {
		t.Errorf("a run pinned to a teamspace the agent cannot work in read %v and answered %q", service.read, answer)
	}

	// Unpinned, two teamspaces and no query is still the model's to settle.
	service = newPinnedTeamspaces()
	answer = usingTeamspaceTool(t, context.Background(), service, "teamspaces_context", map[string]any{"query": ""})
	if len(service.read) != 0 || !strings.Contains(answer, "name the teamspace or human team") {
		t.Errorf("an unpinned run with two teamspaces and no query read %v and answered %q, want it told to name one", service.read, answer)
	}
}
