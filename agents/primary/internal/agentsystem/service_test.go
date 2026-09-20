package agentsystem

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	primaryagents "github.com/Shubham04-oss/oryxa-go/agents/primary/internal/agents"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/agentteams"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/managedagents"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/memberauth"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/threads"
	agentsv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/agents/v1"
)

type registryCall struct {
	member   threads.Member
	agentID  string
	threadID string
	input    threads.RunInput
}

type fakeAgentRegistry struct {
	mu          sync.Mutex
	definitions []managedagents.Definition
	calls       []registryCall
}

func (registry *fakeAgentRegistry) List(context.Context, managedagents.Scope) ([]managedagents.Definition, error) {
	return registry.definitions, nil
}

func (registry *fakeAgentRegistry) Create(_ context.Context, scope managedagents.Scope, input managedagents.CreateInput) (managedagents.Definition, error) {
	return managedagents.Definition{ID: "agt_created", OrganizationID: scope.OrganizationID, OwnerUserID: scope.UserID, Kind: managedagents.KindSpecialist, Name: input.Name, Description: input.Description, Instructions: input.Instructions, Status: managedagents.StatusActive}, nil
}

func (registry *fakeAgentRegistry) Update(context.Context, managedagents.Scope, string, managedagents.UpdateInput) (managedagents.Definition, error) {
	return managedagents.Definition{}, nil
}

func (registry *fakeAgentRegistry) Delete(context.Context, managedagents.Scope, string) error {
	return nil
}

func (registry *fakeAgentRegistry) StartRun(_ context.Context, member threads.Member, agentID, threadID string, input threads.RunInput) (managedagents.StartedRun, error) {
	registry.mu.Lock()
	registry.calls = append(registry.calls, registryCall{member: member, agentID: agentID, threadID: threadID, input: input})
	registry.mu.Unlock()
	if threadID == "" {
		threadID = "thread_" + agentID
	}
	return managedagents.StartedRun{ThreadID: threadID, Run: &fakeRun{id: "run_" + agentID, output: "result from " + agentID}}, nil
}

type fakeRun struct {
	id     string
	output string
}

func (run *fakeRun) ID() string { return run.id }
func (run *fakeRun) Execute(_ context.Context, emit func(threads.Item) error) {
	_ = emit(threads.Item{Type: threads.ItemMessage, Author: "specialist", Text: run.output})
	_ = emit(threads.Item{Type: threads.ItemRunCompleted, RunID: run.id})
}
func (run *fakeRun) Discard(context.Context, string) {}

type fakeTeamRegistry struct{ team agentteams.Team }

func (registry *fakeTeamRegistry) List(context.Context, agentteams.Scope) ([]agentteams.Team, error) {
	return []agentteams.Team{registry.team}, nil
}
func (registry *fakeTeamRegistry) Get(_ context.Context, _ agentteams.Scope, id string) (agentteams.Team, error) {
	if id != registry.team.ID {
		return agentteams.Team{}, agentteams.ErrNotFound
	}
	return registry.team, nil
}
func (registry *fakeTeamRegistry) Create(context.Context, agentteams.Scope, agentteams.CreateInput) (agentteams.Team, error) {
	return registry.team, nil
}
func (registry *fakeTeamRegistry) Update(context.Context, agentteams.Scope, string, agentteams.UpdateInput) (agentteams.Team, error) {
	return registry.team, nil
}
func (registry *fakeTeamRegistry) Delete(context.Context, agentteams.Scope, string) error { return nil }

func primaryActor() primaryagents.AgentSystemActor {
	return primaryagents.AgentSystemActor{OrganizationID: "org_1", UserID: "user_1", AgentID: "agt_primary", AgentName: "Crew", AgentKind: string(managedagents.KindPrimary)}
}

func testTeam() agentteams.Team {
	return agentteams.Team{
		ID: "atm_revenue", Name: "Revenue", CoordinatorAgentID: "agt_primary", CoordinatorName: "Crew", Status: agentteams.StatusActive,
		Members: []agentteams.Member{
			{AgentID: "agt_sales", Name: "Sales", Role: "Qualify opportunities", Status: managedagents.StatusActive},
			{AgentID: "agt_engineering", Name: "Engineering", Role: "Answer feasibility questions", Status: managedagents.StatusActive},
		},
	}
}

func TestOnlyThePrimaryAgentCanReadOrChangeFullAgentConfiguration(t *testing.T) {
	agents := &fakeAgentRegistry{definitions: []managedagents.Definition{{ID: "agt_primary", Kind: managedagents.KindPrimary, Name: "Crew"}}}
	service, err := New(agents, &fakeTeamRegistry{team: testTeam()})
	if err != nil {
		t.Fatal(err)
	}
	specialist := primaryActor()
	specialist.AgentID, specialist.AgentKind = "agt_sales", string(managedagents.KindSpecialist)
	if _, err := service.ListAgents(context.Background(), specialist); !errors.Is(err, agentteams.ErrForbidden) {
		t.Fatalf("specialist list error=%v", err)
	}
	if _, err := service.CreateAgent(context.Background(), specialist, primaryagents.SystemAgentCreateInput{Name: "Legal", Instructions: "Review."}); !errors.Is(err, agentteams.ErrForbidden) {
		t.Fatalf("specialist create error=%v", err)
	}
	listed, err := service.ListAgents(context.Background(), primaryActor())
	if err != nil || len(listed) != 1 || listed[0].ID != "agt_primary" {
		t.Fatalf("listed=%+v error=%v", listed, err)
	}
}

func TestThePrimaryCoordinatorDelegatesIndependentAssignments(t *testing.T) {
	agents := &fakeAgentRegistry{}
	service, err := New(agents, &fakeTeamRegistry{team: testTeam()})
	if err != nil {
		t.Fatal(err)
	}
	ctx := memberauth.WithAccessToken(context.Background(), "member-token")
	ctx = memberauth.WithRunOrigin(ctx, agentsv1.StartedByMember)
	delegation, err := service.Delegate(ctx, primaryActor(), primaryagents.SystemTeamDelegateInput{
		TeamID: "atm_revenue",
		Assignments: []primaryagents.SystemTeamAssignment{
			{AgentID: "agt_sales", Task: "Qualify Acme", Context: "New inbound opportunity"},
			{AgentID: "agt_engineering", Task: "Assess SSO support"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(delegation.Results) != 2 || delegation.Results[0].Status != "completed" || delegation.Results[0].Output != "result from agt_sales" || delegation.Results[1].Status != "completed" {
		t.Fatalf("delegation=%+v", delegation)
	}
	agents.mu.Lock()
	defer agents.mu.Unlock()
	if len(agents.calls) != 2 {
		t.Fatalf("calls=%+v", agents.calls)
	}
	for _, call := range agents.calls {
		if call.member.UserID != "user_1" || call.member.OrganizationID != "org_1" || call.member.AccessToken != "member-token" || call.input.StartedBy != agentsv1.StartedByTeam {
			t.Fatalf("call=%+v", call)
		}
		if !strings.Contains(call.input.Message, "This delegation does not expand your configured purpose") {
			t.Fatalf("delegation message=%q", call.input.Message)
		}
	}
}

func TestDelegationRequiresAMemberPresentAndActualTeamMembers(t *testing.T) {
	service, _ := New(&fakeAgentRegistry{}, &fakeTeamRegistry{team: testTeam()})
	input := primaryagents.SystemTeamDelegateInput{TeamID: "atm_revenue", Assignments: []primaryagents.SystemTeamAssignment{{AgentID: "agt_legal", Task: "Review terms"}}}
	if _, err := service.Delegate(context.Background(), primaryActor(), input); err == nil {
		t.Fatal("delegation without a member-present origin succeeded")
	}
	ctx := memberauth.WithRunOrigin(context.Background(), agentsv1.StartedByMember)
	if _, err := service.Delegate(ctx, primaryActor(), input); !errors.Is(err, agentteams.ErrInvalid) {
		t.Fatalf("nonmember assignment error=%v", err)
	}
}

func TestThePrimaryCanResumeAConfirmationPendingAssignment(t *testing.T) {
	agents := &fakeAgentRegistry{}
	service, _ := New(agents, &fakeTeamRegistry{team: testTeam()})
	ctx := memberauth.WithAccessToken(context.Background(), "member-token")
	ctx = memberauth.WithRunOrigin(ctx, agentsv1.StartedByMember)
	result, err := service.Continue(ctx, primaryActor(), primaryagents.SystemTeamContinueInput{
		TeamID: "atm_revenue", AgentID: "agt_sales", ThreadID: "thread_sales",
		Confirmations: []primaryagents.SystemConfirmationAnswer{{ID: "call_write_crm", Confirmed: true}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "completed" || result.ThreadID != "thread_sales" {
		t.Fatalf("result=%+v", result)
	}
	agents.mu.Lock()
	defer agents.mu.Unlock()
	if len(agents.calls) != 1 || agents.calls[0].threadID != "thread_sales" || agents.calls[0].input.StartedBy != agentsv1.StartedByTeam ||
		len(agents.calls[0].input.Confirmations) != 1 || agents.calls[0].input.Confirmations[0].ID != "call_write_crm" || !agents.calls[0].input.Confirmations[0].Confirmed {
		t.Fatalf("calls=%+v", agents.calls)
	}
}
