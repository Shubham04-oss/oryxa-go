package agents

import (
	"context"
	"errors"
	"strings"
	"testing"

	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/session"
	"google.golang.org/adk/v2/tool"
)

func TestAgentSystemInstructionsRequireAdaptiveDiscoveryBeforeCreation(t *testing.T) {
	for _, guidance := range []string{
		"apps_connections",
		"Before the first member-facing discovery response",
		"three to six",
		"market and ICP",
		"Clay, Apollo or Luma",
		"`agent_system_create_agent`",
		"first response to a broad request",
		"never turn it into a snake_case",
		"working checklist",
	} {
		if !strings.Contains(agentSystemInstructions, guidance) {
			t.Fatalf("agent system creation guidance is missing %q", guidance)
		}
	}
}

func TestAgentSystemToolboxConfirmsEveryPersistentMutation(t *testing.T) {
	box, err := AgentSystemToolbox(NewAgentSystemBinding())
	if err != nil {
		t.Fatal(err)
	}
	sessions := session.InMemoryService()
	created, err := sessions.Create(context.Background(), &session.CreateRequest{
		AppName: "test", UserID: "user_1", State: map[string]any{OrganizationIDKey: "org_1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	invocation := &fakeInvocationContext{base: context.Background(), session: created.Session}
	available, err := box.Toolset.Tools(agent.NewContext(invocation))
	if err != nil {
		t.Fatal(err)
	}
	if len(available) != 11 {
		t.Fatalf("tools=%d, want 11", len(available))
	}
	toolContext := agent.NewToolContext(invocation, "call_1", &session.EventActions{StateDelta: map[string]any{}}, nil)
	mutations := map[string]map[string]any{
		"agent_system_create_agent":        {"name": "Sales", "description": "Own pipeline", "instructions": "Qualify opportunities."},
		"agent_system_update_agent":        {"agent_id": "agt_sales"},
		"agent_system_delete_agent":        {"id": "agt_sales"},
		"agent_system_create_team":         {"name": "Revenue", "description": "Move deals", "members": []any{map[string]any{"agent_id": "agt_sales", "role": "Qualify"}}},
		"agent_system_update_team":         {"team_id": "atm_revenue"},
		"agent_system_delete_team":         {"id": "atm_revenue"},
		"agent_system_continue_assignment": {"team_id": "atm_revenue", "agent_id": "agt_sales", "thread_id": "thread_1", "confirmations": []any{map[string]any{"id": "call_1", "confirmed": true}}},
	}
	for name, arguments := range mutations {
		found := findAppsTool(t, available, name)
		runnable, ok := found.(interface {
			Run(agent.Context, any) (map[string]any, error)
		})
		if !ok {
			t.Fatalf("tool %q cannot run", name)
		}
		if _, err := runnable.Run(toolContext, arguments); !errors.Is(err, tool.ErrConfirmationRequired) {
			t.Fatalf("%s error=%v, want confirmation", name, err)
		}
	}
}

func TestAgentSystemBindingCanOnlyBeBoundOnce(t *testing.T) {
	binding := NewAgentSystemBinding()
	service := &fakeAgentSystemService{}
	if err := binding.Bind(service); err != nil {
		t.Fatal(err)
	}
	if err := binding.Bind(service); err == nil {
		t.Fatal("binding accepted a replacement service")
	}
}

type fakeAgentSystemService struct{}

func (*fakeAgentSystemService) ListAgents(context.Context, AgentSystemActor) ([]SystemAgent, error) {
	return nil, nil
}
func (*fakeAgentSystemService) CreateAgent(context.Context, AgentSystemActor, SystemAgentCreateInput) (SystemAgent, error) {
	return SystemAgent{}, nil
}
func (*fakeAgentSystemService) UpdateAgent(context.Context, AgentSystemActor, SystemAgentUpdateInput) (SystemAgent, error) {
	return SystemAgent{}, nil
}
func (*fakeAgentSystemService) DeleteAgent(context.Context, AgentSystemActor, string) error {
	return nil
}
func (*fakeAgentSystemService) ListTeams(context.Context, AgentSystemActor) ([]SystemTeam, error) {
	return nil, nil
}
func (*fakeAgentSystemService) GetTeam(context.Context, AgentSystemActor, string) (SystemTeam, error) {
	return SystemTeam{}, nil
}
func (*fakeAgentSystemService) CreateTeam(context.Context, AgentSystemActor, SystemTeamCreateInput) (SystemTeam, error) {
	return SystemTeam{}, nil
}
func (*fakeAgentSystemService) UpdateTeam(context.Context, AgentSystemActor, SystemTeamUpdateInput) (SystemTeam, error) {
	return SystemTeam{}, nil
}
func (*fakeAgentSystemService) DeleteTeam(context.Context, AgentSystemActor, string) error {
	return nil
}
func (*fakeAgentSystemService) Delegate(context.Context, AgentSystemActor, SystemTeamDelegateInput) (SystemDelegation, error) {
	return SystemDelegation{}, nil
}
func (*fakeAgentSystemService) Continue(context.Context, AgentSystemActor, SystemTeamContinueInput) (SystemDelegationResult, error) {
	return SystemDelegationResult{}, nil
}
