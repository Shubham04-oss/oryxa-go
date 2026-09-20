package agents

import (
	"context"
	_ "embed"
	"errors"
	"strings"
	"sync"

	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/tool"
	"google.golang.org/adk/v2/tool/functiontool"

	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/memberauth"
)

//go:embed toolboxes/agent_system.md
var agentSystemInstructions string

type AgentSystemActor struct {
	OrganizationID string
	UserID         string
	AgentID        string
	AgentName      string
	AgentKind      string
}

type SystemAgent struct {
	ID           string `json:"id"`
	Kind         string `json:"kind"`
	Name         string `json:"name"`
	Description  string `json:"description"`
	Instructions string `json:"instructions"`
	Status       string `json:"status"`
	Revision     int64  `json:"revision"`
}

type SystemAgentCreateInput struct {
	Name         string `json:"name"`
	Description  string `json:"description"`
	Instructions string `json:"instructions"`
}

type SystemAgentUpdateInput struct {
	AgentID      string  `json:"agent_id"`
	Name         *string `json:"name,omitempty"`
	Description  *string `json:"description,omitempty"`
	Instructions *string `json:"instructions,omitempty"`
	Status       *string `json:"status,omitempty"`
}

type SystemTeamMember struct {
	AgentID     string `json:"agent_id"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Status      string `json:"status"`
	Role        string `json:"role"`
	Position    int    `json:"position"`
}

type SystemTeam struct {
	ID                 string             `json:"id"`
	Name               string             `json:"name"`
	Description        string             `json:"description"`
	CoordinatorAgentID string             `json:"coordinator_agent_id"`
	CoordinatorName    string             `json:"coordinator_name"`
	Status             string             `json:"status"`
	Revision           int64              `json:"revision"`
	Members            []SystemTeamMember `json:"members"`
}

type SystemTeamMemberInput struct {
	AgentID string `json:"agent_id"`
	Role    string `json:"role"`
}

type SystemTeamCreateInput struct {
	Name        string                  `json:"name"`
	Description string                  `json:"description"`
	Members     []SystemTeamMemberInput `json:"members"`
}

type SystemTeamUpdateInput struct {
	TeamID      string                   `json:"team_id"`
	Name        *string                  `json:"name,omitempty"`
	Description *string                  `json:"description,omitempty"`
	Status      *string                  `json:"status,omitempty"`
	Members     *[]SystemTeamMemberInput `json:"members,omitempty"`
}

type SystemDeleteInput struct {
	ID string `json:"id"`
}

type SystemGetTeamInput struct {
	TeamID string `json:"team_id"`
}

type SystemTeamAssignment struct {
	AgentID string `json:"agent_id"`
	Task    string `json:"task"`
	Context string `json:"context,omitempty"`
}

type SystemTeamDelegateInput struct {
	TeamID      string                 `json:"team_id"`
	Assignments []SystemTeamAssignment `json:"assignments"`
}

type SystemConfirmationAnswer struct {
	ID        string `json:"id"`
	Confirmed bool   `json:"confirmed"`
}

type SystemTeamContinueInput struct {
	TeamID        string                     `json:"team_id"`
	AgentID       string                     `json:"agent_id"`
	ThreadID      string                     `json:"thread_id"`
	Confirmations []SystemConfirmationAnswer `json:"confirmations"`
}

type SystemDelegationResult struct {
	AgentID       string   `json:"agent_id"`
	AgentName     string   `json:"agent_name"`
	ThreadID      string   `json:"thread_id,omitempty"`
	RunID         string   `json:"run_id,omitempty"`
	Status        string   `json:"status"`
	Output        string   `json:"output,omitempty"`
	Error         string   `json:"error,omitempty"`
	Confirmations []string `json:"pending_confirmations,omitempty"`
}

type SystemDelegation struct {
	TeamID  string                   `json:"team_id"`
	Results []SystemDelegationResult `json:"results"`
}

// AgentSystemService is the member-owned registry, team catalogue and
// coordinator execution path beneath the toolbox.
type AgentSystemService interface {
	ListAgents(context.Context, AgentSystemActor) ([]SystemAgent, error)
	CreateAgent(context.Context, AgentSystemActor, SystemAgentCreateInput) (SystemAgent, error)
	UpdateAgent(context.Context, AgentSystemActor, SystemAgentUpdateInput) (SystemAgent, error)
	DeleteAgent(context.Context, AgentSystemActor, string) error
	ListTeams(context.Context, AgentSystemActor) ([]SystemTeam, error)
	GetTeam(context.Context, AgentSystemActor, string) (SystemTeam, error)
	CreateTeam(context.Context, AgentSystemActor, SystemTeamCreateInput) (SystemTeam, error)
	UpdateTeam(context.Context, AgentSystemActor, SystemTeamUpdateInput) (SystemTeam, error)
	DeleteTeam(context.Context, AgentSystemActor, string) error
	Delegate(context.Context, AgentSystemActor, SystemTeamDelegateInput) (SystemDelegation, error)
	Continue(context.Context, AgentSystemActor, SystemTeamContinueInput) (SystemDelegationResult, error)
}

// AgentSystemBinding breaks assembly's construction cycle: the toolbox is
// built into the agents first, then the fully assembled runtime is bound
// before any request can run.
type AgentSystemBinding struct {
	mu      sync.RWMutex
	service AgentSystemService
}

func NewAgentSystemBinding() *AgentSystemBinding { return &AgentSystemBinding{} }

func (binding *AgentSystemBinding) Bind(service AgentSystemService) error {
	if service == nil {
		return errors.New("agent system: a service is required")
	}
	binding.mu.Lock()
	defer binding.mu.Unlock()
	if binding.service != nil {
		return errors.New("agent system: service is already bound")
	}
	binding.service = service
	return nil
}

func (binding *AgentSystemBinding) target() (AgentSystemService, error) {
	binding.mu.RLock()
	defer binding.mu.RUnlock()
	if binding.service == nil {
		return nil, errors.New("the member's persistent agent system is not ready")
	}
	return binding.service, nil
}

type agentSystemTools struct{ binding *AgentSystemBinding }

type noArgs struct{}

type systemAgentsResult struct {
	Agents []SystemAgent `json:"agents"`
}
type systemTeamsResult struct {
	Teams []SystemTeam `json:"teams"`
}
type systemDeletedResult struct {
	Deleted bool `json:"deleted"`
}

func AgentSystemToolbox(binding *AgentSystemBinding) (Toolbox, error) {
	if binding == nil {
		return Toolbox{}, errors.New("agents: AgentSystemToolbox requires a binding")
	}
	handlers := agentSystemTools{binding: binding}
	listAgents, err := functiontool.New(functiontool.Config{
		Name: "agent_system_list_agents", Description: "Lists the member's persistent primary and specialist agents with their current configuration.",
	}, handlers.listAgents)
	if err != nil {
		return Toolbox{}, err
	}
	createAgent, err := functiontool.New(functiontool.Config{
		Name: "agent_system_create_agent", Description: "Creates one persistent specialist agent after the member confirms its exact name, purpose and operating instructions.", RequireConfirmation: true,
	}, handlers.createAgent)
	if err != nil {
		return Toolbox{}, err
	}
	updateAgent, err := functiontool.New(functiontool.Config{
		Name: "agent_system_update_agent", Description: "Changes one of the member's persistent agents after confirmation.", RequireConfirmation: true,
	}, handlers.updateAgent)
	if err != nil {
		return Toolbox{}, err
	}
	deleteAgent, err := functiontool.New(functiontool.Config{
		Name: "agent_system_delete_agent", Description: "Deletes a specialist agent after confirmation. The primary agent cannot be deleted.", RequireConfirmation: true,
	}, handlers.deleteAgent)
	if err != nil {
		return Toolbox{}, err
	}
	listTeams, err := functiontool.New(functiontool.Config{
		Name: "agent_system_list_teams", Description: "Lists the member's persistent agent teams, their coordinator and specialist roles.",
	}, handlers.listTeams)
	if err != nil {
		return Toolbox{}, err
	}
	getTeam, err := functiontool.New(functiontool.Config{
		Name: "agent_system_get_team", Description: "Reads one persistent agent team by id.",
	}, handlers.getTeam)
	if err != nil {
		return Toolbox{}, err
	}
	createTeam, err := functiontool.New(functiontool.Config{
		Name: "agent_system_create_team", Description: "Composes existing specialist agents into a primary-coordinated team after confirmation.", RequireConfirmation: true,
	}, handlers.createTeam)
	if err != nil {
		return Toolbox{}, err
	}
	updateTeam, err := functiontool.New(functiontool.Config{
		Name: "agent_system_update_team", Description: "Changes a team's identity, status or complete member-role list after confirmation.", RequireConfirmation: true,
	}, handlers.updateTeam)
	if err != nil {
		return Toolbox{}, err
	}
	deleteTeam, err := functiontool.New(functiontool.Config{
		Name: "agent_system_delete_team", Description: "Deletes a persistent agent team after confirmation; its agents remain available.", RequireConfirmation: true,
	}, handlers.deleteTeam)
	if err != nil {
		return Toolbox{}, err
	}
	delegate, err := functiontool.New(functiontool.Config{
		Name: "agent_system_delegate", Description: "As the team's primary coordinator, assigns bounded work to one or more team specialists in parallel and returns each strict result.",
	}, handlers.delegate)
	if err != nil {
		return Toolbox{}, err
	}
	continueAssignment, err := functiontool.New(functiontool.Config{
		Name: "agent_system_continue_assignment", Description: "Resumes one confirmation-pending specialist assignment in its durable thread with the member's explicit answers.", RequireConfirmation: true,
	}, handlers.continueAssignment)
	if err != nil {
		return Toolbox{}, err
	}
	return Toolbox{
		Name: "agent_system", Description: "Create and configure persistent specialist agents, compose primary-coordinated teams, and delegate bounded assignments to team members.",
		Instructions: agentSystemInstructions,
		Toolset: fixedTools{name: "agent_system", tools: []tool.Tool{
			listAgents, createAgent, updateAgent, deleteAgent,
			listTeams, getTeam, createTeam, updateTeam, deleteTeam, delegate, continueAssignment,
		}},
	}, nil
}

func (tools agentSystemTools) actor(ctx agent.Context) (AgentSystemActor, AgentSystemService, error) {
	member, err := memberauth.MemberFromSession(ctx)
	if err != nil {
		return AgentSystemActor{}, nil, err
	}
	actor := AgentSystemActor{
		OrganizationID: member.OrganizationID, UserID: member.UserID,
		AgentID:   stateText(ctx.ReadonlyState(), AgentIDKey, ""),
		AgentName: stateText(ctx.ReadonlyState(), AgentNameKey, ""),
		AgentKind: stateText(ctx.ReadonlyState(), AgentKindKey, ""),
	}
	if actor.AgentID == "" {
		return AgentSystemActor{}, nil, errors.New("this conversation has no persistent agent identity")
	}
	target, err := tools.binding.target()
	return actor, target, err
}

func (tools agentSystemTools) listAgents(ctx agent.Context, _ noArgs) (systemAgentsResult, error) {
	actor, service, err := tools.actor(ctx)
	if err != nil {
		return systemAgentsResult{}, err
	}
	result, err := service.ListAgents(ctx, actor)
	return systemAgentsResult{Agents: result}, err
}
func (tools agentSystemTools) createAgent(ctx agent.Context, input SystemAgentCreateInput) (SystemAgent, error) {
	actor, service, err := tools.actor(ctx)
	if err != nil {
		return SystemAgent{}, err
	}
	return service.CreateAgent(ctx, actor, input)
}
func (tools agentSystemTools) updateAgent(ctx agent.Context, input SystemAgentUpdateInput) (SystemAgent, error) {
	actor, service, err := tools.actor(ctx)
	if err != nil {
		return SystemAgent{}, err
	}
	return service.UpdateAgent(ctx, actor, input)
}
func (tools agentSystemTools) deleteAgent(ctx agent.Context, input SystemDeleteInput) (systemDeletedResult, error) {
	actor, service, err := tools.actor(ctx)
	if err != nil {
		return systemDeletedResult{}, err
	}
	err = service.DeleteAgent(ctx, actor, strings.TrimSpace(input.ID))
	return systemDeletedResult{Deleted: err == nil}, err
}
func (tools agentSystemTools) listTeams(ctx agent.Context, _ noArgs) (systemTeamsResult, error) {
	actor, service, err := tools.actor(ctx)
	if err != nil {
		return systemTeamsResult{}, err
	}
	result, err := service.ListTeams(ctx, actor)
	return systemTeamsResult{Teams: result}, err
}
func (tools agentSystemTools) getTeam(ctx agent.Context, input SystemGetTeamInput) (SystemTeam, error) {
	actor, service, err := tools.actor(ctx)
	if err != nil {
		return SystemTeam{}, err
	}
	return service.GetTeam(ctx, actor, strings.TrimSpace(input.TeamID))
}
func (tools agentSystemTools) createTeam(ctx agent.Context, input SystemTeamCreateInput) (SystemTeam, error) {
	actor, service, err := tools.actor(ctx)
	if err != nil {
		return SystemTeam{}, err
	}
	return service.CreateTeam(ctx, actor, input)
}
func (tools agentSystemTools) updateTeam(ctx agent.Context, input SystemTeamUpdateInput) (SystemTeam, error) {
	actor, service, err := tools.actor(ctx)
	if err != nil {
		return SystemTeam{}, err
	}
	return service.UpdateTeam(ctx, actor, input)
}
func (tools agentSystemTools) deleteTeam(ctx agent.Context, input SystemDeleteInput) (systemDeletedResult, error) {
	actor, service, err := tools.actor(ctx)
	if err != nil {
		return systemDeletedResult{}, err
	}
	err = service.DeleteTeam(ctx, actor, strings.TrimSpace(input.ID))
	return systemDeletedResult{Deleted: err == nil}, err
}
func (tools agentSystemTools) delegate(ctx agent.Context, input SystemTeamDelegateInput) (SystemDelegation, error) {
	actor, service, err := tools.actor(ctx)
	if err != nil {
		return SystemDelegation{}, err
	}
	return service.Delegate(ctx, actor, input)
}

func (tools agentSystemTools) continueAssignment(ctx agent.Context, input SystemTeamContinueInput) (SystemDelegationResult, error) {
	actor, service, err := tools.actor(ctx)
	if err != nil {
		return SystemDelegationResult{}, err
	}
	return service.Continue(ctx, actor, input)
}
