package managedagents

import (
	"context"
	"errors"
	"iter"
	"strings"
	"testing"
	"time"

	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/agent/llmagent"
	"google.golang.org/adk/v2/model"
	"google.golang.org/adk/v2/session"
	"google.golang.org/adk/v2/tool"
	"google.golang.org/adk/v2/tool/toolconfirmation"
	"google.golang.org/genai"

	primaryagents "github.com/Shubham04-oss/oryxa-go/agents/primary/internal/agents"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/pricing"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/runstore"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/threads"
	agentsv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/agents/v1"
)

type primaryReportRuns struct {
	noRuns
	busy bool
	last runstore.Run
}

func (runs *primaryReportRuns) Start(_ context.Context, run runstore.Run, _ time.Duration) error {
	runs.last = run
	if runs.busy {
		return runstore.ErrRunInProgress
	}
	return nil
}

func reportInput() threads.RunInput {
	return threads.RunInput{
		Message: "Report the published answer", StartedBy: agentsv1.StartedByTrigger,
		Delivery: primaryagents.Delivery{RequestID: "irq_1", Purpose: primaryagents.DeliveryReports, PrimaryChat: true},
	}
}

func primaryReportWorld(t *testing.T) (*Service, *threads.Service, threads.Member, string, *primaryReportRuns) {
	t.Helper()
	answering := func(words string) agent.Agent {
		built, err := llmagent.New(llmagent.Config{Name: primaryagents.RootName, Model: saying(words), Instruction: "Answer."})
		if err != nil {
			t.Fatal(err)
		}
		return built
	}
	prices, err := pricing.NewTable(nil)
	if err != nil {
		t.Fatal(err)
	}
	sessions := session.InMemoryService()
	runs := &primaryReportRuns{}
	repo := newFakeRepository()
	service := &Service{
		repository: repo, factory: func(Definition) (agent.Agent, error) { return answering("member's agent"), nil },
		unattended: func(_ Definition, profile UnattendedProfile) (agent.Agent, error) {
			if profile != DeliveryReport {
				t.Fatalf("report used profile %q", profile)
			}
			return answering("The approved answer is 14 October."), nil
		},
		sessions: sessions, runs: runs, prices: prices,
	}
	primary, err := threads.New(threads.Config{
		AppName: primaryagents.RootName, Agent: answering("member's agent"),
		Sessions: sessions, Runs: runs, Prices: prices,
	})
	if err != nil {
		t.Fatal(err)
	}
	member := threads.Member{OrganizationID: "org_1", UserID: "user_1", AgentID: repo.primary.ID, AgentKind: "primary", AgentName: repo.primary.Name}
	created, err := primary.Create(context.Background(), member)
	if err != nil {
		t.Fatal(err)
	}
	return service, primary, member, created.ID, runs
}

func TestRequesterReportContinuesTheRealPrimaryChatNamespace(t *testing.T) {
	service, primary, member, threadID, runs := primaryReportWorld(t)
	started, err := service.StartUnattendedRun(context.Background(), member, member.AgentID, threadID, reportInput(), DeliveryReport)
	if err != nil || started.ThreadID != threadID {
		t.Fatalf("primary report=%+v err=%v", started, err)
	}
	started.Run.Execute(context.Background(), func(threads.Item) error { return nil })
	detail, err := primary.Get(context.Background(), member, threadID)
	if err != nil || len(detail.Items) != 2 || !strings.Contains(detail.Items[1].Text, "14 October") {
		t.Fatalf("original primary chat=%+v err=%v", detail, err)
	}
	if runs.last.SessionID != threadID || runs.last.OrganizationID != member.OrganizationID || runs.last.UserID != member.UserID {
		t.Fatalf("report held a different conversation: %+v", runs.last)
	}
	// No managed-agent replacement was allocated behind the member's chat.
	listed, err := service.sessions.List(context.Background(), &session.ListRequest{AppName: appName(member.AgentID), UserID: member.UserID})
	if err != nil || len(listed.Sessions) != 0 {
		t.Fatalf("managed replacements=%+v err=%v", listed, err)
	}
}

func TestPrimaryOriginReportKeepsBusyAndConfirmationChecks(t *testing.T) {
	service, primary, member, threadID, runs := primaryReportWorld(t)
	runs.busy = true
	_, err := service.StartUnattendedRun(context.Background(), member, member.AgentID, threadID, reportInput(), DeliveryReport)
	if !errors.Is(err, threads.ErrRunInProgress) || runs.last.SessionID != threadID {
		t.Fatalf("busy report err=%v run=%+v", err, runs.last)
	}
	runs.busy = false
	stored, err := service.sessions.Get(context.Background(), &session.GetRequest{AppName: primaryagents.RootName, UserID: member.UserID, SessionID: threadID})
	if err != nil {
		t.Fatal(err)
	}
	event := session.NewEvent(context.Background(), "waiting_run")
	event.Author = primaryagents.RootName
	event.Content = &genai.Content{Role: genai.RoleModel, Parts: []*genai.Part{{FunctionCall: &genai.FunctionCall{
		ID: "confirmation_1", Name: toolconfirmation.FunctionCallName,
		Args: map[string]any{"originalFunctionCall": &genai.FunctionCall{ID: "tool_1", Name: "delete_file", Args: map[string]any{"name": "plan.txt"}}},
	}}}}
	if err := service.sessions.AppendEvent(context.Background(), stored.Session, event); err != nil {
		t.Fatal(err)
	}
	_, err = service.StartUnattendedRun(context.Background(), member, member.AgentID, threadID, reportInput(), DeliveryReport)
	if !errors.Is(err, threads.ErrConfirmationPending) {
		t.Fatalf("confirmation-pending origin report err=%v", err)
	}
	detail, err := primary.Get(context.Background(), member, threadID)
	if err != nil || len(detail.Pending) != 1 || detail.Pending[0] != "confirmation_1" || len(detail.Items) != 1 {
		t.Fatalf("report disturbed the pending confirmation: %+v err=%v", detail, err)
	}
}

func TestPrimaryOriginReportCannotCrossOwnershipOrChangeWorkProfile(t *testing.T) {
	service, _, member, threadID, _ := primaryReportWorld(t)
	for _, other := range []threads.Member{
		{OrganizationID: "org_2", UserID: member.UserID},
		{OrganizationID: member.OrganizationID, UserID: "user_2"},
	} {
		if _, err := service.StartUnattendedRun(context.Background(), other, member.AgentID, threadID, reportInput(), DeliveryReport); !errors.Is(err, ErrNotFound) {
			t.Fatalf("cross-ownership origin report err=%v", err)
		}
	}
	input := reportInput()
	input.Delivery.Purpose = primaryagents.DeliveryWorks
	if _, err := service.StartUnattendedRun(context.Background(), member, member.AgentID, threadID, input, DeliveryQuiet); !errors.Is(err, threads.ErrInvalid) {
		t.Fatalf("approved work was allowed into a primary origin: %v", err)
	}
}

type followupRequestLedger struct {
	primaryagents.InteractionService
	actor primaryagents.InteractionActor
	state map[string]any
}

func (ledger *followupRequestLedger) Create(ctx context.Context, actor primaryagents.InteractionActor, _, _ string, input primaryagents.InteractionCreateInput) (primaryagents.Interaction, error) {
	ledger.actor, ledger.state = actor, map[string]any{}
	toolContext := ctx.(agent.Context)
	for _, key := range []string{"delivered_interaction", "delivery_purpose", "delivery_sources"} {
		ledger.state[key], _ = toolContext.ReadonlyState().Get(key)
	}
	return primaryagents.Interaction{ID: "irq_followup", Status: "pending", Task: input.Task}, nil
}

type followupRequestModel struct{}

func (followupRequestModel) Name() string { return "followup" }

func (followupRequestModel) GenerateContent(_ context.Context, request *model.LLMRequest, _ bool) iter.Seq2[*model.LLMResponse, error] {
	return func(yield func(*model.LLMResponse, error) bool) {
		for _, content := range request.Contents {
			for _, part := range content.Parts {
				if part.FunctionResponse != nil && part.FunctionResponse.Name == "interactions_create" {
					yield(&model.LLMResponse{Content: genai.NewContentFromText("I filed the next request.", genai.RoleModel), TurnComplete: true}, nil)
					return
				}
			}
		}
		yield(&model.LLMResponse{Content: &genai.Content{Role: genai.RoleModel, Parts: []*genai.Part{{FunctionCall: &genai.FunctionCall{
			ID: "new_request", Name: "interactions_create", Args: map[string]any{"target_agent_id": "agt_other", "purpose": "Follow-up", "task": "What changed?"},
		}}}}}, nil)
	}
}

func TestOrdinaryFollowupCanFileANewRequestAfterAPrimaryReport(t *testing.T) {
	service, _, member, threadID, _ := primaryReportWorld(t)
	input := reportInput()
	input.Delivery.Sources = []string{}
	started, err := service.StartUnattendedRun(context.Background(), member, member.AgentID, threadID, input, DeliveryReport)
	if err != nil {
		t.Fatal(err)
	}
	started.Run.Execute(context.Background(), func(threads.Item) error { return nil })
	ledger := &followupRequestLedger{}
	box, err := primaryagents.InteractionToolbox(primaryagents.InteractionConfig{Service: ledger})
	if err != nil {
		t.Fatal(err)
	}
	normal, err := llmagent.New(llmagent.Config{
		Name: primaryagents.RootName, Model: followupRequestModel{}, Instruction: "File the member's new request.",
		Toolsets: []tool.Toolset{box.Toolset},
	})
	if err != nil {
		t.Fatal(err)
	}
	service.factory = func(Definition) (agent.Agent, error) { return normal, nil }
	service.unattended = nil
	engine, err := service.engineIn(service.repository.(*fakeRepository).primary, UnattendedReads, primaryagents.RootName)
	if err != nil {
		t.Fatal(err)
	}
	member.AccessToken = "member-token"
	followup, err := engine.StartRun(context.Background(), member, threadID, threads.RunInput{Message: "Ask the next question."})
	if err != nil {
		t.Fatal(err)
	}
	var items []threads.Item
	followup.Execute(context.Background(), func(item threads.Item) error { items = append(items, item); return nil })
	if ledger.actor.ThreadID != threadID || ledger.actor.ThreadAppName != primaryagents.RootName {
		t.Fatalf("ordinary request never filed in the origin: actor=%+v items=%+v", ledger.actor, items)
	}
	if ledger.state["delivered_interaction"] != "" || ledger.state["delivery_purpose"] != "" || ledger.state["delivery_sources"] != nil {
		t.Fatalf("ordinary turn inherited its delivery's state: %v", ledger.state)
	}
}
