package assembly

import (
	"context"
	"iter"
	"slices"
	"sync"
	"testing"

	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/artifact"
	"google.golang.org/adk/v2/model"
	"google.golang.org/adk/v2/runner"
	"google.golang.org/adk/v2/session"
	"google.golang.org/adk/v2/tool"
	"google.golang.org/adk/v2/tool/functiontool"
	"google.golang.org/genai"

	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/agents"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/modelrouter"
)

// recordingModel answers every call with a finished specialist result, which
// Krude reads as plain text, and keeps every request it was sent.
type recordingModel struct {
	mu       sync.Mutex
	requests []*model.LLMRequest
}

func (*recordingModel) Name() string { return "recording" }

func (m *recordingModel) GenerateContent(_ context.Context, request *model.LLMRequest, _ bool) iter.Seq2[*model.LLMResponse, error] {
	m.mu.Lock()
	m.requests = append(m.requests, request)
	m.mu.Unlock()
	return func(yield func(*model.LLMResponse, error) bool) {
		yield(&model.LLMResponse{Content: genai.NewContentFromText(
			`{"status":"completed","summary":"Done.","tools_used":[],"artifacts":[],"handoffs":[]}`, genai.RoleModel,
		), TurnComplete: true}, nil)
	}
}

// firstCall runs one turn of built in a new session and returns the first
// request it sent the model.
func (m *recordingModel) firstCall(t *testing.T, built agent.Agent) *model.LLMRequest {
	t.Helper()
	m.mu.Lock()
	sent := len(m.requests)
	m.mu.Unlock()
	sessions := session.InMemoryService()
	turns, err := runner.New(runner.Config{AppName: "app", Agent: built, SessionService: sessions, ArtifactService: artifact.InMemoryService()})
	if err != nil {
		t.Fatal(err)
	}
	created, err := sessions.Create(context.Background(), &session.CreateRequest{AppName: "app", UserID: "user_b", State: map[string]any{
		agents.OrganizationIDKey: "org_1", agents.AgentIDKey: "agt_b", agents.AgentNameKey: "Carl", agents.AgentKindKey: "primary",
	}})
	if err != nil {
		t.Fatal(err)
	}
	for _, err := range turns.Run(context.Background(), "user_b", created.Session.ID(), genai.NewContentFromText("Deliver irq_1.", genai.RoleUser), agent.RunConfig{}) {
		if err != nil {
			t.Fatalf("run: %v", err)
		}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.requests) == sent {
		t.Fatal("the run called no model")
	}
	return m.requests[sent]
}

func declared(request *model.LLMRequest) []string {
	var names []string
	if request.Config == nil {
		return names
	}
	for _, tools := range request.Config.Tools {
		for _, function := range tools.FunctionDeclarations {
			names = append(names, function.Name)
		}
	}
	return names
}

// fakeToolbox is a toolbox of one tool that echoes its input.
func fakeToolbox(t *testing.T, name, toolName string) agents.Toolbox {
	t.Helper()
	echo, err := functiontool.New(functiontool.Config{Name: toolName, Description: "Echoes."}, func(_ agent.Context, args struct {
		Text string `json:"text"`
	}) (map[string]any, error) {
		return map[string]any{"text": args.Text}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return agents.Toolbox{Name: name, Description: name, Instructions: name + " skill", Toolset: tools{echo}}
}

type tools []tool.Tool

func (tools) Name() string { return "tools" }

func (list tools) Tools(agent.ReadonlyContext) ([]tool.Tool, error) { return list, nil }

// noInteractions is a ledger nothing is asked of.
type noInteractions struct{}

func (noInteractions) Create(context.Context, agents.InteractionActor, string, string, agents.InteractionCreateInput) (agents.Interaction, error) {
	return agents.Interaction{}, nil
}
func (noInteractions) Inbox(context.Context, agents.InteractionActor, int) ([]agents.Interaction, error) {
	return nil, nil
}
func (noInteractions) Sent(context.Context, agents.InteractionActor, int) ([]agents.Interaction, error) {
	return nil, nil
}
func (noInteractions) Get(context.Context, agents.InteractionActor, string) (agents.Interaction, error) {
	return agents.Interaction{}, nil
}
func (noInteractions) Decide(context.Context, agents.InteractionActor, string, agents.InteractionDecisionInput) (agents.Interaction, error) {
	return agents.Interaction{}, nil
}
func (noInteractions) Respond(context.Context, agents.InteractionActor, string, agents.InteractionResponseInput) (agents.Interaction, error) {
	return agents.Interaction{}, nil
}

// assembledKrude is Krude built on llm the way Build builds it, with the
// member's apps and knowledge and the interactions toolboxes.
func assembledKrude(t *testing.T, llm model.LLM) *Krude {
	t.Helper()
	router, err := modelrouter.New(llm, llm)
	if err != nil {
		t.Fatal(err)
	}
	interactions := func(config agents.InteractionConfig) agents.Toolbox {
		config.Service = noInteractions{}
		box, err := agents.InteractionToolbox(config)
		if err != nil {
			t.Fatal(err)
		}
		return box
	}
	apps, knowledge := fakeToolbox(t, "apps", "apps_run_read_tool"), fakeToolbox(t, "knowledge", "knowledge_search")
	answering, delivering := interactions(agents.InteractionConfig{Answering: true}), interactions(agents.InteractionConfig{Delivering: true})
	unattended := []agents.Toolbox{apps, knowledge, answering}
	unattendedQuiet := []agents.Toolbox{knowledge, answering}
	krude := &Krude{
		router:            router,
		toolboxes:         []agents.Toolbox{apps, knowledge, interactions(agents.InteractionConfig{})},
		unattended:        unattended,
		unattendedQuiet:   unattendedQuiet,
		deliveryToolboxes: deliveryToolboxes(unattended, unattendedQuiet, &delivering),
	}
	if err := krude.buildAgents(); err != nil {
		t.Fatal(err)
	}
	return krude
}

// TestEachDeliveryStartsWithItsWholeReach: approved work starts with the
// member's apps and knowledge, read-only, and its request; deciding a new
// request with the knowledge and the request; a report with the request
// alone. Krude and a specialist alike, and none of them can list or load a
// toolbox, so none spends a model call finding one.
func TestEachDeliveryStartsWithItsWholeReach(t *testing.T) {
	llm := &recordingModel{}
	krude := assembledKrude(t, llm)
	request := []string{"interactions_get", "interactions_decide", "interactions_respond"}
	for name, reach := range map[string]struct {
		reach   DeliveryReach
		offered []string
		absent  []string
	}{
		"approved work": {DeliveryReads, append([]string{"apps_run_read_tool", "knowledge_search"}, request...), nil},
		"a new request": {DeliveryQuiet, append([]string{"knowledge_search"}, request...), []string{"apps_run_read_tool"}},
		"a report":      {DeliveryReport, request, []string{"apps_run_read_tool", "knowledge_search"}},
	} {
		specialist, err := krude.ManagedDelivering(agents.ManagedSpec{ID: "agt_sales", Name: "Sales", Instructions: "Answer requests."}, reach.reach)
		if err != nil {
			t.Fatal(err)
		}
		for runner, built := range map[string]agent.Agent{"Krude": krude.Delivering(reach.reach), "a specialist": specialist} {
			offered := declared(llm.firstCall(t, built))
			for _, want := range reach.offered {
				if !slices.Contains(offered, want) {
					t.Errorf("%s, for %s: the first call declared %v, want %s", name, runner, offered, want)
				}
			}
			for _, unwanted := range append([]string{"list_tools", "load_tools", "interactions_inbox", "interactions_create"}, reach.absent...) {
				if slices.Contains(offered, unwanted) {
					t.Errorf("%s, for %s: the first call declared %s: %v", name, runner, unwanted, offered)
				}
			}
		}
	}
}

// TestOnlyDeliveriesStartLoaded: the member's own chat, an app wake-up and a
// room's stand-in still find and load their toolboxes as they did.
func TestOnlyDeliveriesStartLoaded(t *testing.T) {
	llm := &recordingModel{}
	krude := assembledKrude(t, llm)
	stood, err := krude.ManagedUnattended(agents.ManagedSpec{ID: "agt_sales", Name: "Sales", Instructions: "Answer requests."}, false)
	if err != nil {
		t.Fatal(err)
	}
	for name, built := range map[string]agent.Agent{
		"a member's chat": krude.Root, "a wake-up": krude.Unattended, "a room's stand-in": krude.UnattendedQuiet, "a specialist standing in": stood,
	} {
		offered := declared(llm.firstCall(t, built))
		if !slices.Contains(offered, "list_tools") || !slices.Contains(offered, "load_tools") {
			t.Errorf("%s lost list_tools or load_tools: %v", name, offered)
		}
		if slices.Contains(offered, "knowledge_search") || slices.Contains(offered, "interactions_get") {
			t.Errorf("%s started with a toolbox loaded: %v", name, offered)
		}
	}
}

// TestDeliveryToolboxesGiveEachReachItsOwn: a delivery's toolboxes are the
// unattended run's, or those without the apps, with the interactions toolbox
// swapped for the delivering one, and a report's are the delivering one
// alone; with no interactions configured, a report has none.
func TestDeliveryToolboxesGiveEachReachItsOwn(t *testing.T) {
	unattended := []agents.Toolbox{{Name: "apps", Description: "reads"}, {Name: "knowledge", Description: "reads"}, {Name: "interactions", Description: "answering"}}
	quiet := []agents.Toolbox{{Name: "knowledge", Description: "reads"}, {Name: "interactions", Description: "answering"}}
	delivering := agents.Toolbox{Name: "interactions", Description: "delivering"}
	described := func(toolboxes []agents.Toolbox) []string {
		descriptions := []string{}
		for _, box := range toolboxes {
			descriptions = append(descriptions, box.Name+":"+box.Description)
		}
		return descriptions
	}
	reaches := deliveryToolboxes(unattended, quiet, &delivering)
	for reach, want := range map[DeliveryReach][]string{
		DeliveryReads:  {"apps:reads", "knowledge:reads", "interactions:delivering"},
		DeliveryQuiet:  {"knowledge:reads", "interactions:delivering"},
		DeliveryReport: {"interactions:delivering"},
	} {
		if got := described(reaches[reach]); !slices.Equal(got, want) {
			t.Errorf("reach %d = %v, want %v", reach, got, want)
		}
	}
	if got := described(deliveryToolboxes(unattended, quiet, nil)[DeliveryReport]); len(got) != 0 {
		t.Errorf("a report with no interactions configured has %v", got)
	}
	if got := described(unattended); !slices.Equal(got, []string{"apps:reads", "knowledge:reads", "interactions:answering"}) {
		t.Errorf("the unattended list changed: %v", got)
	}
}

// TestAReachNobodyKnowsGetsTheLeast: a reach this code does not know starts
// with what a report has, never more.
func TestAReachNobodyKnowsGetsTheLeast(t *testing.T) {
	llm := &recordingModel{}
	krude := assembledKrude(t, llm)
	offered := declared(llm.firstCall(t, krude.Delivering(DeliveryReach(99))))
	if slices.Contains(offered, "apps_run_read_tool") || slices.Contains(offered, "knowledge_search") || !slices.Contains(offered, "interactions_get") {
		t.Fatalf("an unknown reach offered %v, want a report's", offered)
	}
	if got := krude.toolboxesFor(DeliveryReach(99)); len(got) != 1 || got[0].Name != "interactions" {
		t.Fatalf("an unknown reach's toolboxes = %v, want a report's", got)
	}
}
