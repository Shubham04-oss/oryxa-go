package httpserver

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"time"

	primaryagents "github.com/Shubham04-oss/oryxa-go/agents/primary/internal/agents"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/agentteams"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/agentworkflows"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/authclient"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/managedagents"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/organizationagents"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/teamspaces"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/threads"
)

type fakeDirectory struct {
	membership authclient.Membership
	err        error
	seen       []authclient.Credentials
}

func (directory *fakeDirectory) Membership(_ context.Context, organizationID string, credentials authclient.Credentials) (authclient.Membership, error) {
	directory.seen = append(directory.seen, credentials)
	if directory.err != nil {
		return authclient.Membership{}, directory.err
	}
	membership := directory.membership
	membership.OrganizationID = organizationID
	return membership, nil
}

type fakeThreads struct {
	members  []threads.Member
	inputs   []threads.RunInput
	detail   threads.Detail
	err      error
	startErr error
	run      *fakeRun
}

func (service *fakeThreads) Create(_ context.Context, member threads.Member) (threads.Thread, error) {
	service.members = append(service.members, member)
	return threads.Thread{ID: "thread_1", UpdatedAt: time.Unix(0, 0).UTC()}, service.err
}

func (service *fakeThreads) List(_ context.Context, member threads.Member) ([]threads.Thread, error) {
	service.members = append(service.members, member)
	return []threads.Thread{{ID: "thread_1", Title: "Hello"}}, service.err
}

func (service *fakeThreads) Get(_ context.Context, member threads.Member, threadID string) (threads.Detail, error) {
	service.members = append(service.members, member)
	if threadID != "thread_1" {
		return threads.Detail{}, threads.ErrNotFound
	}
	return service.detail, service.err
}

func (service *fakeThreads) Delete(_ context.Context, member threads.Member, threadID string) error {
	service.members = append(service.members, member)
	if threadID != "thread_1" {
		return threads.ErrNotFound
	}
	return service.err
}

func (service *fakeThreads) StartRun(_ context.Context, member threads.Member, _ string, input threads.RunInput) (Run, error) {
	service.members = append(service.members, member)
	service.inputs = append(service.inputs, input)
	if service.startErr != nil {
		return nil, service.startErr
	}
	return service.run, nil
}

func (service *fakeThreads) Artifact(_ context.Context, member threads.Member, threadID, name string, version int64) (threads.File, error) {
	service.members = append(service.members, member)
	switch {
	case threadID != "thread_1":
		return threads.File{}, threads.ErrNotFound
	case name == "screenshot.png":
		return threads.File{Name: name, MIMEType: "image/png", Data: []byte("png-bytes")}, nil
	case name == "report.html" && version == 2:
		return threads.File{Name: name, MIMEType: "text/html", Data: []byte("<script>alert(1)</script>")}, nil
	case name == "user:plan.txt":
		return threads.File{Name: name, MIMEType: "text/plain; charset=utf-8", Data: []byte("plan")}, nil
	}
	return threads.File{}, threads.ErrNotFound
}

type fakeRun struct {
	items     []threads.Item
	executed  bool
	discarded string
}

func (run *fakeRun) ID() string { return "run_1" }

func (run *fakeRun) Execute(_ context.Context, emit func(threads.Item) error) {
	run.executed = true
	for _, item := range run.items {
		if emit(item) != nil {
			return
		}
	}
}

func (run *fakeRun) Discard(_ context.Context, reason string) { run.discarded = reason }

type fakeManagedAgents struct {
	definitions []managedagents.Definition
	run         *fakeRun
	lastInput   threads.RunInput
	threadID    string
	// primaryErr, when set, is what Primary returns instead of looking
	// through definitions - simulating the agent registry itself failing
	// (a database blip), as opposed to it working but having no primary
	// agent yet.
	primaryErr error
	// scopes and members record, in call order, exactly what every method
	// was invoked with - so an authorization test can assert the handler
	// always passed the authenticated caller's own identity, never one
	// taken from the request path or body.
	scopes  []managedagents.Scope
	members []threads.Member
}

type fakeAgentTeams struct {
	teams      []agentteams.Team
	seenScopes []agentteams.Scope
	// deleteErr, when set, is what Delete answers: the store refusing to
	// remove a team something still points at.
	deleteErr error
}

type fakeAgentWorkflows struct {
	workflow  agentworkflows.Workflow
	execution agentworkflows.Execution
	scopes    []agentworkflows.Scope
}

type fakeTeamspaces struct {
	space             teamspaces.Space
	organizationAgent teamspaces.OrganizationAgent
	entries           []teamspaces.Entry
	actors            []teamspaces.Actor
}

// fakeOrganizationContext records, in call order, the organization id every
// method was invoked with. The interface takes no user identity at all: the
// organization-agent's synthesis and follow-ups are shared, organization-wide
// context by design (like a teamspace), not a member-owned resource, so the
// only boundary this layer can and must hold is the organization one.
type fakeOrganizationContext struct {
	organizationIDs []string
}

func (service *fakeOrganizationContext) Synthesis(_ context.Context, organizationID string) (organizationagents.Synthesis, error) {
	service.organizationIDs = append(service.organizationIDs, organizationID)
	return organizationagents.Synthesis{
		OrganizationID: organizationID, AgentID: "oag_oryxa", Revision: 2,
		Summary: "Launch is on track.", Decisions: []string{"Use the reviewed design."},
		Risks: []string{}, OpenQuestions: []string{"Confirm rollout date."}, SourceEntryID: "tse_review",
	}, nil
}

func (service *fakeOrganizationContext) ListFollowUps(_ context.Context, organizationID string, _ int) ([]organizationagents.FollowUp, error) {
	service.organizationIDs = append(service.organizationIDs, organizationID)
	return []organizationagents.FollowUp{{
		ID: "ofu_1", OrganizationID: organizationID, SynthesisRevision: 2,
		SourceEntryID: "tse_review", TeamspaceID: "tsp_engineering",
		TargetAgentID: "agt_primary", Task: "Confirm rollout date", Rationale: "It remains open", Status: organizationagents.FollowUpProposed,
	}}, nil
}

func (service *fakeTeamspaces) OrganizationAgent(_ context.Context, actor teamspaces.Actor) (teamspaces.OrganizationAgent, error) {
	service.actors = append(service.actors, actor)
	if service.organizationAgent.ID == "" {
		service.organizationAgent = teamspaces.OrganizationAgent{
			ID: "oag_oryxa", OrganizationID: actor.OrganizationID, Name: "Oryxa Agent",
			Instructions: "Coordinate shared context.", Status: teamspaces.StatusActive, Revision: 1, CanManage: true,
		}
	}
	return service.organizationAgent, nil
}

func (service *fakeTeamspaces) UpdateOrganizationAgent(_ context.Context, actor teamspaces.Actor, input teamspaces.OrganizationAgentUpdate) (teamspaces.OrganizationAgent, error) {
	agent, err := service.OrganizationAgent(context.Background(), actor)
	if err != nil {
		return teamspaces.OrganizationAgent{}, err
	}
	if input.Name != nil {
		agent.Name = *input.Name
	}
	if input.Description != nil {
		agent.Description = *input.Description
	}
	if input.Instructions != nil {
		agent.Instructions = *input.Instructions
	}
	if input.Status != nil {
		agent.Status = *input.Status
	}
	agent.Revision++
	service.organizationAgent = agent
	return agent, nil
}

func (service *fakeTeamspaces) List(_ context.Context, actor teamspaces.Actor) ([]teamspaces.Space, error) {
	service.actors = append(service.actors, actor)
	return []teamspaces.Space{service.space}, nil
}

func (service *fakeTeamspaces) Get(_ context.Context, actor teamspaces.Actor, id string) (teamspaces.Space, error) {
	service.actors = append(service.actors, actor)
	if service.space.ID != id {
		return teamspaces.Space{}, teamspaces.ErrNotFound
	}
	return service.space, nil
}

func (service *fakeTeamspaces) Create(_ context.Context, actor teamspaces.Actor, input teamspaces.CreateInput) (teamspaces.Space, error) {
	service.actors = append(service.actors, actor)
	organizationAgent, _ := service.OrganizationAgent(context.Background(), actor)
	service.space = teamspaces.Space{
		ID: "tsp_engineering", OrganizationID: actor.OrganizationID, TeamID: input.TeamID,
		Name: input.Name, Description: input.Description, CreatedByUserID: actor.UserID,
		OrganizationAgentID: organizationAgent.ID, OrganizationAgent: organizationAgent,
		Whisperers: []teamspaces.Whisperer{{UserID: actor.UserID, AgentID: "agt_primary", AgentName: "Carl", Mode: "whisperer"}},
		Status:     teamspaces.StatusActive, Revision: 1, Contributions: []teamspaces.Contribution{}, CanManage: true, IsMember: true,
	}
	return service.space, nil
}

func (service *fakeTeamspaces) Update(_ context.Context, actor teamspaces.Actor, id string, input teamspaces.UpdateInput) (teamspaces.Space, error) {
	service.actors = append(service.actors, actor)
	if service.space.ID != id {
		return teamspaces.Space{}, teamspaces.ErrNotFound
	}
	if input.Name != nil {
		service.space.Name = *input.Name
	}
	service.space.Revision++
	return service.space, nil
}

func (service *fakeTeamspaces) Delete(_ context.Context, actor teamspaces.Actor, id string) error {
	service.actors = append(service.actors, actor)
	if service.space.ID != id {
		return teamspaces.ErrNotFound
	}
	return nil
}

func (service *fakeTeamspaces) AddContribution(_ context.Context, actor teamspaces.Actor, id string, input teamspaces.ContributionInput) (teamspaces.Space, error) {
	service.actors = append(service.actors, actor)
	if service.space.ID != id {
		return teamspaces.Space{}, teamspaces.ErrNotFound
	}
	service.space.Contributions = append(service.space.Contributions, teamspaces.Contribution{
		AgentID: input.AgentID, OwnerUserID: actor.UserID, Name: "Reviewer", Role: input.Role, DelegationMode: input.DelegationMode,
	})
	return service.space, nil
}

func (service *fakeTeamspaces) UpdateContribution(_ context.Context, actor teamspaces.Actor, id, agentID string, input teamspaces.ContributionUpdate) (teamspaces.Space, error) {
	service.actors = append(service.actors, actor)
	if service.space.ID != id || len(service.space.Contributions) == 0 || service.space.Contributions[0].AgentID != agentID {
		return teamspaces.Space{}, teamspaces.ErrNotFound
	}
	if input.Role != nil {
		service.space.Contributions[0].Role = *input.Role
	}
	if input.DelegationMode != nil {
		service.space.Contributions[0].DelegationMode = *input.DelegationMode
	}
	return service.space, nil
}

func (service *fakeTeamspaces) DeleteContribution(_ context.Context, actor teamspaces.Actor, id, agentID string) (teamspaces.Space, error) {
	service.actors = append(service.actors, actor)
	if service.space.ID != id || len(service.space.Contributions) == 0 || service.space.Contributions[0].AgentID != agentID {
		return teamspaces.Space{}, teamspaces.ErrNotFound
	}
	service.space.Contributions = []teamspaces.Contribution{}
	return service.space, nil
}

func (service *fakeTeamspaces) PublishEntry(_ context.Context, actor teamspaces.Actor, id string, input teamspaces.PublishEntryInput) (teamspaces.Entry, error) {
	service.actors = append(service.actors, actor)
	if service.space.ID != id {
		return teamspaces.Entry{}, teamspaces.ErrNotFound
	}
	entry := teamspaces.Entry{
		ID: "tse_review", TeamspaceID: id, OrganizationID: actor.OrganizationID,
		AuthorUserID: actor.UserID, AuthorAgentID: input.AuthorAgentID, AuthorAgentName: "Carl",
		Kind: input.Kind, Title: input.Title, Summary: input.Summary,
		ToolsUsed: input.ToolsUsed, Artifacts: input.Artifacts, CreatedAt: time.Unix(2, 0).UTC(),
	}
	service.entries = append(service.entries, entry)
	return entry, nil
}

func (service *fakeTeamspaces) ListEntries(_ context.Context, actor teamspaces.Actor, id string, _ int) ([]teamspaces.Entry, error) {
	service.actors = append(service.actors, actor)
	if service.space.ID != id {
		return nil, teamspaces.ErrNotFound
	}
	return service.entries, nil
}

func (service *fakeAgentWorkflows) List(_ context.Context, scope agentworkflows.Scope) ([]agentworkflows.Workflow, error) {
	service.scopes = append(service.scopes, scope)
	return []agentworkflows.Workflow{service.workflow}, nil
}
func (service *fakeAgentWorkflows) Get(_ context.Context, scope agentworkflows.Scope, id string) (agentworkflows.Workflow, error) {
	service.scopes = append(service.scopes, scope)
	if service.workflow.ID != id {
		return agentworkflows.Workflow{}, agentworkflows.ErrNotFound
	}
	return service.workflow, nil
}
func (service *fakeAgentWorkflows) Create(_ context.Context, scope agentworkflows.Scope, input agentworkflows.CreateInput) (agentworkflows.Workflow, error) {
	service.scopes = append(service.scopes, scope)
	service.workflow = agentworkflows.Workflow{ID: "awf_pipeline", OrganizationID: scope.OrganizationID, OwnerUserID: scope.UserID, Name: input.Name, Description: input.Description, TeamID: input.TeamID, Status: agentworkflows.StatusActive, Assignments: input.Assignments, Schedule: input.Schedule, Revision: 1}
	return service.workflow, nil
}
func (service *fakeAgentWorkflows) Update(_ context.Context, scope agentworkflows.Scope, id string, input agentworkflows.UpdateInput) (agentworkflows.Workflow, error) {
	service.scopes = append(service.scopes, scope)
	if service.workflow.ID != id {
		return agentworkflows.Workflow{}, agentworkflows.ErrNotFound
	}
	if input.Name != nil {
		service.workflow.Name = *input.Name
	}
	service.workflow.Revision++
	return service.workflow, nil
}
func (service *fakeAgentWorkflows) Delete(_ context.Context, scope agentworkflows.Scope, id string) error {
	service.scopes = append(service.scopes, scope)
	if service.workflow.ID != id {
		return agentworkflows.ErrNotFound
	}
	return nil
}
func (service *fakeAgentWorkflows) Start(_ context.Context, scope agentworkflows.Scope, id string) (agentworkflows.Execution, error) {
	service.scopes = append(service.scopes, scope)
	if service.workflow.ID != id {
		return agentworkflows.Execution{}, agentworkflows.ErrNotFound
	}
	service.execution = agentworkflows.Execution{ID: "awx_1", WorkflowID: id, OrganizationID: scope.OrganizationID, OwnerUserID: scope.UserID, StartedBy: agentworkflows.StartedByAPI, Status: agentworkflows.ExecutionQueued, ExternalID: "arn:execution:1"}
	return service.execution, nil
}
func (service *fakeAgentWorkflows) ListExecutions(_ context.Context, scope agentworkflows.Scope, workflowID string, _ int) ([]agentworkflows.Execution, error) {
	service.scopes = append(service.scopes, scope)
	if service.workflow.ID != workflowID {
		return nil, agentworkflows.ErrNotFound
	}
	return []agentworkflows.Execution{service.execution}, nil
}
func (service *fakeAgentWorkflows) GetExecution(_ context.Context, scope agentworkflows.Scope, id string) (agentworkflows.Execution, error) {
	service.scopes = append(service.scopes, scope)
	if service.execution.ID != id {
		return agentworkflows.Execution{}, agentworkflows.ErrNotFound
	}
	return service.execution, nil
}

func (teams *fakeAgentTeams) List(_ context.Context, scope agentteams.Scope) ([]agentteams.Team, error) {
	teams.seenScopes = append(teams.seenScopes, scope)
	return teams.teams, nil
}

func (teams *fakeAgentTeams) Get(_ context.Context, scope agentteams.Scope, id string) (agentteams.Team, error) {
	teams.seenScopes = append(teams.seenScopes, scope)
	for _, team := range teams.teams {
		if team.ID == id {
			return team, nil
		}
	}
	return agentteams.Team{}, agentteams.ErrNotFound
}

func (teams *fakeAgentTeams) Create(_ context.Context, scope agentteams.Scope, input agentteams.CreateInput) (agentteams.Team, error) {
	teams.seenScopes = append(teams.seenScopes, scope)
	created := agentteams.Team{
		ID: "atm_revenue", OrganizationID: scope.OrganizationID, OwnerUserID: scope.UserID,
		Name: input.Name, Description: input.Description, CoordinatorAgentID: "agt_primary",
		CoordinatorName: "Carl", Status: agentteams.StatusActive, Revision: 1,
	}
	for position, member := range input.Members {
		created.Members = append(created.Members, agentteams.Member{
			AgentID: member.AgentID, Role: member.Role, Position: position, Status: managedagents.StatusActive,
		})
	}
	teams.teams = append(teams.teams, created)
	return created, nil
}

func (teams *fakeAgentTeams) Update(_ context.Context, scope agentteams.Scope, id string, input agentteams.UpdateInput) (agentteams.Team, error) {
	teams.seenScopes = append(teams.seenScopes, scope)
	var found agentteams.Team
	for _, team := range teams.teams {
		if team.ID == id {
			found = team
			break
		}
	}
	if found.ID == "" {
		return agentteams.Team{}, agentteams.ErrNotFound
	}
	if input.Name != nil {
		found.Name = *input.Name
	}
	found.Revision++
	return found, nil
}

func (teams *fakeAgentTeams) Delete(_ context.Context, scope agentteams.Scope, id string) error {
	teams.seenScopes = append(teams.seenScopes, scope)
	if teams.deleteErr != nil {
		return teams.deleteErr
	}
	if id != "atm_revenue" {
		return agentteams.ErrNotFound
	}
	return nil
}

type fakeInteractions struct {
	request primaryagents.Interaction
	actor   primaryagents.InteractionActor
	token   string
	key     string
	// actors records, in call order, the actor every method was invoked
	// with - so an authorization test can assert each route always acted as
	// the authenticated caller's own persistent agent identity.
	actors []primaryagents.InteractionActor
}

func (service *fakeInteractions) Create(_ context.Context, actor primaryagents.InteractionActor, token, key string, input primaryagents.InteractionCreateInput) (primaryagents.Interaction, error) {
	service.actors = append(service.actors, actor)
	service.actor, service.token, service.key = actor, token, key
	service.request = primaryagents.Interaction{ID: "irq_1", OrganizationID: actor.OrganizationID, Requester: primaryagents.InteractionParty{UserID: actor.UserID, AgentID: actor.AgentID}, Target: primaryagents.InteractionParty{AgentID: input.TargetAgentID}, Purpose: input.Purpose, Task: input.Task, Status: "pending"}
	return service.request, nil
}
func (service *fakeInteractions) Inbox(_ context.Context, actor primaryagents.InteractionActor, _ int) ([]primaryagents.Interaction, error) {
	service.actors = append(service.actors, actor)
	return []primaryagents.Interaction{service.request}, nil
}
func (service *fakeInteractions) Sent(_ context.Context, actor primaryagents.InteractionActor, _ int) ([]primaryagents.Interaction, error) {
	service.actors = append(service.actors, actor)
	return []primaryagents.Interaction{service.request}, nil
}
func (service *fakeInteractions) Get(_ context.Context, actor primaryagents.InteractionActor, _ string) (primaryagents.Interaction, error) {
	service.actors = append(service.actors, actor)
	return service.request, nil
}
func (service *fakeInteractions) Decide(_ context.Context, actor primaryagents.InteractionActor, _ string, input primaryagents.InteractionDecisionInput) (primaryagents.Interaction, error) {
	service.actors = append(service.actors, actor)
	service.request.Status = input.Decision
	return service.request, nil
}
func (service *fakeInteractions) Respond(_ context.Context, actor primaryagents.InteractionActor, _ string, input primaryagents.InteractionResponseInput) (primaryagents.Interaction, error) {
	service.actors = append(service.actors, actor)
	service.request.Status = input.Status
	return service.request, nil
}

func (agents *fakeManagedAgents) Primary(_ context.Context, scope managedagents.Scope) (managedagents.Definition, error) {
	agents.scopes = append(agents.scopes, scope)
	if agents.primaryErr != nil {
		return managedagents.Definition{}, agents.primaryErr
	}
	for _, definition := range agents.definitions {
		if definition.Kind == managedagents.KindPrimary {
			definition.OrganizationID = scope.OrganizationID
			definition.OwnerUserID = scope.UserID
			return definition, nil
		}
	}
	return managedagents.Definition{}, managedagents.ErrNotFound
}

func (agents *fakeManagedAgents) List(_ context.Context, scope managedagents.Scope) ([]managedagents.Definition, error) {
	agents.scopes = append(agents.scopes, scope)
	return agents.definitions, nil
}
func (agents *fakeManagedAgents) Get(_ context.Context, scope managedagents.Scope, id string) (managedagents.Definition, error) {
	agents.scopes = append(agents.scopes, scope)
	for _, definition := range agents.definitions {
		if definition.ID == id {
			return definition, nil
		}
	}
	return managedagents.Definition{}, managedagents.ErrNotFound
}
func (agents *fakeManagedAgents) Create(_ context.Context, scope managedagents.Scope, input managedagents.CreateInput) (managedagents.Definition, error) {
	agents.scopes = append(agents.scopes, scope)
	created := managedagents.Definition{ID: "agt_sales", OrganizationID: scope.OrganizationID, OwnerUserID: scope.UserID, Kind: managedagents.KindSpecialist, Name: input.Name, Description: input.Description, Instructions: input.Instructions, Status: managedagents.StatusActive}
	agents.definitions = append(agents.definitions, created)
	return created, nil
}
func (agents *fakeManagedAgents) Update(_ context.Context, scope managedagents.Scope, id string, input managedagents.UpdateInput) (managedagents.Definition, error) {
	agents.scopes = append(agents.scopes, scope)
	definition, err := agents.Get(context.Background(), scope, id)
	if err != nil {
		return managedagents.Definition{}, err
	}
	if input.Name != nil {
		definition.Name = *input.Name
	}
	return definition, nil
}
func (agents *fakeManagedAgents) Delete(_ context.Context, scope managedagents.Scope, id string) error {
	agents.scopes = append(agents.scopes, scope)
	if id == "agt_primary" {
		return managedagents.ErrInvalid
	}
	return nil
}
func (agents *fakeManagedAgents) StartRun(_ context.Context, member threads.Member, _ string, threadID string, input threads.RunInput) (managedagents.StartedRun, error) {
	agents.members = append(agents.members, member)
	agents.lastInput = input
	if threadID == "" {
		threadID = "agent_thread_1"
	}
	agents.threadID = threadID
	return managedagents.StartedRun{ThreadID: threadID, Run: agents.run}, nil
}

func newTestServer(directory *fakeDirectory, service *fakeThreads) *Server {
	managed := &fakeManagedAgents{definitions: []managedagents.Definition{{ID: "agt_primary", Kind: managedagents.KindPrimary, Name: "Carl", Status: managedagents.StatusActive}}, run: &fakeRun{}}
	return New(directory, service, Config{AllowedOrigins: []string{"https://app.oryxa.test"}, RunTimeout: time.Minute, Agents: managed}, nil)
}

func member() *fakeDirectory {
	return &fakeDirectory{membership: authclient.Membership{UserID: "user_1", Role: "member"}}
}

func serve(server *Server, method, path, body string, headers map[string]string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	for name, value := range headers {
		if name == "Cookie" {
			request.AddCookie(&http.Cookie{Name: authclient.AccessCookieName, Value: value})
			continue
		}
		request.Header.Set(name, value)
	}
	recorder := httptest.NewRecorder()
	server.ServeHTTP(recorder, request)
	return recorder
}

func errorCode(t *testing.T, recorder *httptest.ResponseRecorder) string {
	t.Helper()
	var body struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatalf("error body %q: %v", recorder.Body.String(), err)
	}
	return body.Error.Code
}

var bearer = map[string]string{"Authorization": "Bearer member-token"}

func TestPatternsNameEveryRoute(t *testing.T) {
	server := newTestServer(member(), &fakeThreads{})
	patterns := server.Patterns()
	if len(patterns) != 48 {
		t.Fatalf("patterns = %v", patterns)
	}
	wildcards := regexp.MustCompile(`\{[^}]+\}`)
	for _, pattern := range patterns {
		method, path, _ := strings.Cut(pattern, " ")
		request := httptest.NewRequest(method, wildcards.ReplaceAllString(path, "x"), nil)
		if _, matched := server.mux.Handler(request); matched != pattern {
			t.Errorf("%s routes to %q", pattern, matched)
		}
	}
}

func TestAgentTeamsCanBeComposedAndManagedInsideTheMemberBoundary(t *testing.T) {
	teams := &fakeAgentTeams{}
	server := New(member(), &fakeThreads{}, Config{
		AllowedOrigins: []string{"https://app.oryxa.test"}, RunTimeout: time.Minute,
		Agents: &fakeManagedAgents{definitions: []managedagents.Definition{{
			ID: "agt_primary", Kind: managedagents.KindPrimary, Name: "Carl", Status: managedagents.StatusActive,
		}}},
		Teams: teams,
	}, nil)

	created := serve(server, http.MethodPost, "/v1/organizations/org_1/agent-teams", `{"name":"Revenue","description":"Qualify and close","members":[{"agent_id":"agt_sales","role":"Qualify opportunities"}]}`, bearer)
	if created.Code != http.StatusCreated || !strings.Contains(created.Body.String(), `"id":"atm_revenue"`) || !strings.Contains(created.Body.String(), `"coordinator_agent_id":"agt_primary"`) {
		t.Fatalf("create=%d %s", created.Code, created.Body.String())
	}
	listed := serve(server, http.MethodGet, "/v1/organizations/org_1/agent-teams", "", bearer)
	if listed.Code != http.StatusOK || !strings.Contains(listed.Body.String(), `"name":"Revenue"`) {
		t.Fatalf("list=%d %s", listed.Code, listed.Body.String())
	}
	updated := serve(server, http.MethodPatch, "/v1/organizations/org_1/agent-teams/atm_revenue", `{"name":"Go to market"}`, bearer)
	if updated.Code != http.StatusOK || !strings.Contains(updated.Body.String(), `"name":"Go to market"`) {
		t.Fatalf("update=%d %s", updated.Code, updated.Body.String())
	}
	deleted := serve(server, http.MethodDelete, "/v1/organizations/org_1/agent-teams/atm_revenue", "", bearer)
	if deleted.Code != http.StatusNoContent {
		t.Fatalf("delete=%d %s", deleted.Code, deleted.Body.String())
	}
	for _, scope := range teams.seenScopes {
		if scope.OrganizationID != "org_1" || scope.UserID != "user_1" {
			t.Fatalf("scope=%+v", scope)
		}
	}
	bad := serve(server, http.MethodPost, "/v1/organizations/org_1/agent-teams", `{"name":"Revenue","members":[],"unknown":true}`, bearer)
	if bad.Code != http.StatusBadRequest {
		t.Fatalf("unknown field=%d %s", bad.Code, bad.Body.String())
	}
}

func TestTeamspacesExposeOrganizationAgentWhisperersAndExplicitAgentContributions(t *testing.T) {
	spaces := &fakeTeamspaces{}
	server := New(member(), &fakeThreads{}, Config{
		AllowedOrigins: []string{"https://app.oryxa.test"}, RunTimeout: time.Minute,
		Agents: &fakeManagedAgents{definitions: []managedagents.Definition{{
			ID: "agt_primary", Kind: managedagents.KindPrimary, Name: "Carl", Status: managedagents.StatusActive,
		}}},
		Teamspaces: spaces, OrganizationContext: &fakeOrganizationContext{},
	}, nil)

	created := serve(server, http.MethodPost, "/v1/organizations/org_1/teamspaces",
		`{"team_id":"team_engineering","name":"Engineering Space"}`, bearer)
	if created.Code != http.StatusCreated || !strings.Contains(created.Body.String(), `"id":"tsp_engineering"`) || !strings.Contains(created.Body.String(), `"id":"oag_oryxa"`) || !strings.Contains(created.Body.String(), `"mode":"whisperer"`) {
		t.Fatalf("create=%d %s", created.Code, created.Body.String())
	}

	organizationAgent := serve(server, http.MethodGet, "/v1/organizations/org_1/organization-agent", "", bearer)
	if organizationAgent.Code != http.StatusOK || !strings.Contains(organizationAgent.Body.String(), `"id":"oag_oryxa"`) {
		t.Fatalf("organization agent=%d %s", organizationAgent.Code, organizationAgent.Body.String())
	}
	updatedAgent := serve(server, http.MethodPatch, "/v1/organizations/org_1/organization-agent", `{"name":"Atlas"}`, bearer)
	if updatedAgent.Code != http.StatusOK || !strings.Contains(updatedAgent.Body.String(), `"name":"Atlas"`) {
		t.Fatalf("update organization agent=%d %s", updatedAgent.Code, updatedAgent.Body.String())
	}
	contextView := serve(server, http.MethodGet, "/v1/organizations/org_1/organization-agent/context", "", bearer)
	if contextView.Code != http.StatusOK || !strings.Contains(contextView.Body.String(), `"revision":2`) || !strings.Contains(contextView.Body.String(), `"source_entry_id":"tse_review"`) {
		t.Fatalf("organization context=%d %s", contextView.Code, contextView.Body.String())
	}
	followUps := serve(server, http.MethodGet, "/v1/organizations/org_1/organization-agent/follow-ups?limit=20", "", bearer)
	if followUps.Code != http.StatusOK || !strings.Contains(followUps.Body.String(), `"id":"ofu_1"`) || !strings.Contains(followUps.Body.String(), `"status":"proposed"`) {
		t.Fatalf("organization follow-ups=%d %s", followUps.Code, followUps.Body.String())
	}

	contributed := serve(server, http.MethodPost, "/v1/organizations/org_1/teamspaces/tsp_engineering/agents",
		`{"agent_id":"agt_reviewer","role":"Review architecture","delegation_mode":"automatic"}`, bearer)
	if contributed.Code != http.StatusCreated || !strings.Contains(contributed.Body.String(), `"delegation_mode":"automatic"`) {
		t.Fatalf("contribute=%d %s", contributed.Code, contributed.Body.String())
	}

	mode := serve(server, http.MethodPatch, "/v1/organizations/org_1/teamspaces/tsp_engineering/agents/agt_reviewer",
		`{"delegation_mode":"request"}`, bearer)
	if mode.Code != http.StatusOK || !strings.Contains(mode.Body.String(), `"delegation_mode":"request"`) {
		t.Fatalf("change contribution=%d %s", mode.Code, mode.Body.String())
	}

	listed := serve(server, http.MethodGet, "/v1/organizations/org_1/teamspaces", "", bearer)
	if listed.Code != http.StatusOK || !strings.Contains(listed.Body.String(), `"teamspaces"`) {
		t.Fatalf("list=%d %s", listed.Code, listed.Body.String())
	}
	published := serve(server, http.MethodPost, "/v1/organizations/org_1/teamspaces/tsp_engineering/entries",
		`{"author_agent_id":"agt_primary","kind":"result","title":"Architecture review","summary":"Ready to proceed.","tools_used":["github.search"]}`, bearer)
	if published.Code != http.StatusCreated || !strings.Contains(published.Body.String(), `"id":"tse_review"`) || !strings.Contains(published.Body.String(), `"author_agent_name":"Carl"`) {
		t.Fatalf("publish=%d %s", published.Code, published.Body.String())
	}
	entries := serve(server, http.MethodGet, "/v1/organizations/org_1/teamspaces/tsp_engineering/entries?limit=20", "", bearer)
	if entries.Code != http.StatusOK || !strings.Contains(entries.Body.String(), `"entries"`) || !strings.Contains(entries.Body.String(), `"tse_review"`) {
		t.Fatalf("entries=%d %s", entries.Code, entries.Body.String())
	}
	for _, actor := range spaces.actors {
		if actor.OrganizationID != "org_1" || actor.UserID != "user_1" || actor.AccessToken != "member-token" {
			t.Fatalf("actor escaped member identity: %+v", actor)
		}
	}
}

func TestAgentWorkflowsCanBeScheduledStartedAndObservedThroughTheAPI(t *testing.T) {
	workflows := &fakeAgentWorkflows{}
	server := New(member(), &fakeThreads{}, Config{
		AllowedOrigins: []string{"https://app.oryxa.test"}, RunTimeout: time.Minute,
		Agents: &fakeManagedAgents{definitions: []managedagents.Definition{{
			ID: "agt_primary", Kind: managedagents.KindPrimary, Name: "Carl", Status: managedagents.StatusActive,
		}}},
		Workflows: workflows,
	}, nil)

	created := serve(server, http.MethodPost, "/v1/organizations/org_1/agent-workflows",
		`{"name":"Weekly pipeline","team_id":"atm_revenue","assignments":[{"agent_id":"agt_sales","task":"Review pipeline"}],"schedule":{"expression":"cron(0 9 ? * MON *)","timezone":"Asia/Kolkata","state":"enabled"}}`, bearer)
	if created.Code != http.StatusCreated || workflows.workflow.ID != "awf_pipeline" || workflows.workflow.Schedule.Expression == "" {
		t.Fatalf("create = %d %s workflow=%+v", created.Code, created.Body.String(), workflows.workflow)
	}

	started := serve(server, http.MethodPost, "/v1/organizations/org_1/agent-workflows/awf_pipeline/executions", "{}", bearer)
	if started.Code != http.StatusAccepted || workflows.execution.Status != agentworkflows.ExecutionQueued {
		t.Fatalf("start = %d %s execution=%+v", started.Code, started.Body.String(), workflows.execution)
	}

	listed := serve(server, http.MethodGet, "/v1/organizations/org_1/agent-workflows/awf_pipeline/executions", "", bearer)
	if listed.Code != http.StatusOK || !strings.Contains(listed.Body.String(), `"awx_1"`) {
		t.Fatalf("list executions = %d %s", listed.Code, listed.Body.String())
	}
	got := serve(server, http.MethodGet, "/v1/organizations/org_1/agent-workflow-executions/awx_1", "", bearer)
	if got.Code != http.StatusOK || !strings.Contains(got.Body.String(), `"step_functions_execution_arn":"arn:execution:1"`) {
		t.Fatalf("get execution = %d %s", got.Code, got.Body.String())
	}

	for _, scope := range workflows.scopes {
		if scope.OrganizationID != "org_1" || scope.UserID != "user_1" {
			t.Fatalf("workflow scope escaped member boundary: %+v", scope)
		}
	}
}

func TestAgentRequestsCarryThePrimaryIdentityAndIdempotencyKey(t *testing.T) {
	managed := &fakeManagedAgents{definitions: []managedagents.Definition{{ID: "agt_primary", Kind: managedagents.KindPrimary, Name: "Carl", Status: managedagents.StatusActive}}}
	interactions := &fakeInteractions{}
	server := New(member(), &fakeThreads{}, Config{
		AllowedOrigins: []string{"https://app.oryxa.test"}, RunTimeout: time.Minute,
		Agents: managed, Interactions: interactions,
	}, nil)
	created := serve(server, http.MethodPost, "/v1/organizations/org_1/agent-requests",
		`{"target_agent_id":"agt_engineering","purpose":"Architecture","task":"Explain the boundary."}`,
		map[string]string{"Authorization": "Bearer member-token", "Idempotency-Key": "request-1"})
	if created.Code != http.StatusCreated || !strings.Contains(created.Body.String(), `"id":"irq_1"`) {
		t.Fatalf("create=%d %s", created.Code, created.Body.String())
	}
	if interactions.actor.AgentID != "agt_primary" || interactions.actor.AgentName != "Carl" || interactions.token != "member-token" || interactions.key != "request-1" {
		t.Fatalf("actor=%+v token=%q key=%q", interactions.actor, interactions.token, interactions.key)
	}
	inbox := serve(server, http.MethodGet, "/v1/organizations/org_1/agent-requests/inbox", "", bearer)
	if inbox.Code != http.StatusOK || !strings.Contains(inbox.Body.String(), `"requests":[{"id":"irq_1"`) {
		t.Fatalf("inbox=%d %s", inbox.Code, inbox.Body.String())
	}
}

func TestHealthz(t *testing.T) {
	response := serve(newTestServer(member(), &fakeThreads{}), http.MethodGet, "/healthz", "", nil)
	if response.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusNoContent)
	}
}

func TestEveryThreadRouteWantsAMember(t *testing.T) {
	server := newTestServer(member(), &fakeThreads{})
	for _, route := range []struct{ method, path string }{
		{http.MethodGet, "/v1/organizations/org_1/threads"},
		{http.MethodPost, "/v1/organizations/org_1/threads"},
		{http.MethodGet, "/v1/organizations/org_1/threads/thread_1"},
		{http.MethodDelete, "/v1/organizations/org_1/threads/thread_1"},
		{http.MethodPost, "/v1/organizations/org_1/threads/thread_1/runs"},
	} {
		response := serve(server, route.method, route.path, "", nil)
		if response.Code != http.StatusUnauthorized || errorCode(t, response) != "unauthorized" {
			t.Errorf("%s %s = %d %s, want 401", route.method, route.path, response.Code, response.Body.String())
		}
	}
}

func TestACookieChangesNothingFromAnotherSite(t *testing.T) {
	service := &fakeThreads{}
	server := newTestServer(member(), service)
	refused := serve(server, http.MethodPost, "/v1/organizations/org_1/threads", "", map[string]string{
		"Cookie": "cookie-token", "Origin": "https://evil.test",
	})
	if refused.Code != http.StatusForbidden {
		t.Fatalf("cross-site create = %d, want 403", refused.Code)
	}
	allowed := serve(server, http.MethodPost, "/v1/organizations/org_1/threads", "", map[string]string{
		"Cookie": "cookie-token", "Origin": "https://app.oryxa.test",
	})
	if allowed.Code != http.StatusCreated {
		t.Fatalf("same-site create = %d %s, want 201", allowed.Code, allowed.Body.String())
	}
	// Reading with a cookie needs no origin.
	if read := serve(server, http.MethodGet, "/v1/organizations/org_1/threads", "", map[string]string{"Cookie": "cookie-token"}); read.Code != http.StatusOK {
		t.Fatalf("cookie read = %d, want 200", read.Code)
	}
	if len(service.members) != 2 || service.members[0].AccessToken != "cookie-token" || service.members[0].AgentName != "Carl" {
		t.Fatalf("members = %+v, want the cookie's token carried as the member's own", service.members)
	}
}

func TestAnAgentCannotHoldAMembersConversation(t *testing.T) {
	directory := member()
	directory.membership.Agent = true
	response := serve(newTestServer(directory, &fakeThreads{}), http.MethodGet, "/v1/organizations/org_1/threads", "", bearer)
	if response.Code != http.StatusForbidden || errorCode(t, response) != "forbidden" {
		t.Fatalf("agent = %d %s, want 403", response.Code, response.Body.String())
	}
}

func TestAuthsAnswersBecomeErrorCodes(t *testing.T) {
	cases := []struct {
		err    error
		status int
		code   string
	}{
		{authclient.ErrUnauthorized, http.StatusUnauthorized, "unauthorized"},
		{authclient.ErrForbidden, http.StatusForbidden, "forbidden"},
		{authclient.ErrRateLimited, http.StatusTooManyRequests, "rate_limited"},
		{authclient.ErrUnavailable, http.StatusServiceUnavailable, "unavailable"},
	}
	for _, testCase := range cases {
		response := serve(newTestServer(&fakeDirectory{err: testCase.err}, &fakeThreads{}), http.MethodGet, "/v1/organizations/org_1/threads", "", bearer)
		if response.Code != testCase.status || errorCode(t, response) != testCase.code {
			t.Errorf("%v = %d %s, want %d %s", testCase.err, response.Code, response.Body.String(), testCase.status, testCase.code)
		}
	}
}

func TestAnUnavailableServiceSaysWhy(t *testing.T) {
	server := New(nil, nil, Config{Unavailable: "GOOGLE_CLOUD_PROJECT is not set"}, nil)
	response := serve(server, http.MethodGet, "/v1/organizations/org_1/threads", "", bearer)
	if response.Code != http.StatusServiceUnavailable || errorCode(t, response) != "agents_unavailable" ||
		!strings.Contains(response.Body.String(), "GOOGLE_CLOUD_PROJECT") {
		t.Fatalf("unavailable = %d %s", response.Code, response.Body.String())
	}
	if health := serve(server, http.MethodGet, "/healthz", "", nil); health.Code != http.StatusNoContent {
		t.Fatalf("health = %d, want it still to pass", health.Code)
	}
}

func TestManagedAgentsCanBeCreatedRenamedAndRun(t *testing.T) {
	managed := &fakeManagedAgents{
		definitions: []managedagents.Definition{{ID: "agt_primary", Kind: managedagents.KindPrimary, Name: "Carl", Status: managedagents.StatusActive}},
		run:         &fakeRun{items: []threads.Item{{Type: threads.ItemRunCompleted, RunID: "run_1"}}},
	}
	server := New(member(), &fakeThreads{}, Config{
		AllowedOrigins: []string{"https://app.oryxa.test"}, RunTimeout: time.Minute, Agents: managed,
	}, nil)

	listed := serve(server, http.MethodGet, "/v1/organizations/org_1/agents", "", bearer)
	if listed.Code != http.StatusOK || !strings.Contains(listed.Body.String(), `"name":"Carl"`) {
		t.Fatalf("list = %d %s", listed.Code, listed.Body.String())
	}
	created := serve(server, http.MethodPost, "/v1/organizations/org_1/agents", `{"name":"Sales","description":"Own pipeline","instructions":"Review CRM."}`, bearer)
	if created.Code != http.StatusCreated || !strings.Contains(created.Body.String(), `"id":"agt_sales"`) {
		t.Fatalf("create = %d %s", created.Code, created.Body.String())
	}
	name := "Crew"
	updated := serve(server, http.MethodPatch, "/v1/organizations/org_1/agents/agt_primary", `{"name":"`+name+`"}`, bearer)
	if updated.Code != http.StatusOK || !strings.Contains(updated.Body.String(), `"name":"Crew"`) {
		t.Fatalf("update = %d %s", updated.Code, updated.Body.String())
	}

	run := serve(server, http.MethodPost, "/v1/organizations/org_1/agents/agt_sales/runs", `{"message":"Review this quarter"}`, bearer)
	if run.Code != http.StatusOK || run.Header().Get("X-Oryxa-Thread") != "agent_thread_1" || !managed.run.executed {
		t.Fatalf("run = %d headers=%v body=%s executed=%v", run.Code, run.Header(), run.Body.String(), managed.run.executed)
	}
	if managed.lastInput.Message != "Review this quarter" {
		t.Fatalf("run input = %+v", managed.lastInput)
	}
}

// fakePreferences keeps choices by organization/member; a member with none
// has updates on.
type fakePreferences struct {
	off map[string]bool
	err error
	// calls records, in call order, the (organizationID, memberID) pair
	// every method was invoked with - so an authorization test can assert
	// the handler always read or wrote the authenticated caller's own
	// preferences, never an identity taken from the request path or body.
	calls [][2]string
}

func (preferences *fakePreferences) AppUpdates(_ context.Context, organizationID, memberID string) (bool, error) {
	preferences.calls = append(preferences.calls, [2]string{organizationID, memberID})
	if preferences.err != nil {
		return false, preferences.err
	}
	return !preferences.off[organizationID+"/"+memberID], nil
}

func (preferences *fakePreferences) SetAppUpdates(_ context.Context, organizationID, memberID string, on bool) error {
	preferences.calls = append(preferences.calls, [2]string{organizationID, memberID})
	if preferences.err != nil {
		return preferences.err
	}
	if preferences.off == nil {
		preferences.off = map[string]bool{}
	}
	preferences.off[organizationID+"/"+memberID] = !on
	return nil
}

func TestAMemberTurnsTheirOwnAppUpdatesOffAndOn(t *testing.T) {
	preferences := &fakePreferences{}
	server := New(member(), &fakeThreads{}, Config{AllowedOrigins: []string{"https://app.oryxa.test"}, Preferences: preferences}, nil)
	read := func() string {
		t.Helper()
		response := serve(server, http.MethodGet, "/v1/organizations/org_1/preferences", "", bearer)
		if response.Code != http.StatusOK {
			t.Fatalf("read = %d %s", response.Code, response.Body.String())
		}
		return strings.TrimSpace(response.Body.String())
	}
	if got := read(); got != `{"app_updates":true}` {
		t.Fatalf("before any choice = %s, want updates on", got)
	}
	turned := serve(server, http.MethodPut, "/v1/organizations/org_1/preferences", `{"app_updates":false}`, bearer)
	if turned.Code != http.StatusOK || strings.TrimSpace(turned.Body.String()) != `{"app_updates":false}` {
		t.Fatalf("turn off = %d %s", turned.Code, turned.Body.String())
	}
	if got := read(); got != `{"app_updates":false}` {
		t.Fatalf("after turning off = %s", got)
	}
	if !preferences.off["org_1/user_1"] || len(preferences.off) != 1 {
		t.Fatalf("recorded %v, want only user_1's choice in org_1", preferences.off)
	}

	for _, body := range []string{``, `{}`, `{"app_updates":"no"}`, `{"app_updates":true,"briefs":false}`} {
		if response := serve(server, http.MethodPut, "/v1/organizations/org_1/preferences", body, bearer); response.Code != http.StatusBadRequest || errorCode(t, response) != "invalid_request" {
			t.Errorf("PUT %q = %d %s, want 400", body, response.Code, response.Body.String())
		}
	}
	if got := read(); got != `{"app_updates":false}` {
		t.Fatalf("a refused change changed it: %s", got)
	}

	crossSite := serve(server, http.MethodPut, "/v1/organizations/org_1/preferences", `{"app_updates":true}`, map[string]string{
		"Cookie": "cookie-token", "Origin": "https://evil.test",
	})
	if crossSite.Code != http.StatusForbidden || !preferences.off["org_1/user_1"] {
		t.Fatalf("cross-site change = %d, off = %v; want 403 and nothing changed", crossSite.Code, preferences.off)
	}
	if response := serve(server, http.MethodGet, "/v1/organizations/org_1/preferences", "", nil); response.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous read = %d, want 401", response.Code)
	}
}

func TestAnAgentCannotChooseAMembersPreferences(t *testing.T) {
	directory := member()
	directory.membership.Agent = true
	preferences := &fakePreferences{}
	server := New(directory, &fakeThreads{}, Config{Preferences: preferences}, nil)
	for _, method := range []string{http.MethodGet, http.MethodPut} {
		response := serve(server, method, "/v1/organizations/org_1/preferences", `{"app_updates":false}`, bearer)
		if response.Code != http.StatusForbidden || errorCode(t, response) != "forbidden" {
			t.Errorf("agent %s = %d %s, want 403", method, response.Code, response.Body.String())
		}
	}
	if len(preferences.off) != 0 {
		t.Fatalf("an agent changed a member's preferences: %v", preferences.off)
	}
}

func TestPreferencesSayWhenTheyAreNotKept(t *testing.T) {
	response := serve(newTestServer(member(), &fakeThreads{}), http.MethodGet, "/v1/organizations/org_1/preferences", "", bearer)
	if response.Code != http.StatusServiceUnavailable || errorCode(t, response) != "preferences_unavailable" {
		t.Fatalf("no preferences = %d %s, want 503", response.Code, response.Body.String())
	}
	failing := New(member(), &fakeThreads{}, Config{Preferences: &fakePreferences{err: errors.New("database down")}}, nil)
	if response := serve(failing, http.MethodGet, "/v1/organizations/org_1/preferences", "", bearer); response.Code != http.StatusInternalServerError {
		t.Fatalf("failing store = %d %s, want 500", response.Code, response.Body.String())
	}
}

func TestThreadsAreCreatedListedReadAndDeleted(t *testing.T) {
	service := &fakeThreads{detail: threads.Detail{
		Thread: threads.Thread{ID: "thread_1"},
		Items:  []threads.Item{{Type: threads.ItemMessage, Author: threads.AuthorMember, Text: "hi"}},
	}}
	server := newTestServer(member(), service)

	created := serve(server, http.MethodPost, "/v1/organizations/org_1/threads", `{}`, bearer)
	if created.Code != http.StatusCreated || !strings.Contains(created.Body.String(), `"id":"thread_1"`) {
		t.Fatalf("create = %d %s", created.Code, created.Body.String())
	}
	if refused := serve(server, http.MethodPost, "/v1/organizations/org_1/threads", `{"title":"x"}`, bearer); refused.Code != http.StatusBadRequest {
		t.Fatalf("create with an unknown field = %d, want 400", refused.Code)
	}
	listed := serve(server, http.MethodGet, "/v1/organizations/org_1/threads", "", bearer)
	if listed.Code != http.StatusOK || !strings.Contains(listed.Body.String(), `"threads":[{"id":"thread_1","title":"Hello"`) {
		t.Fatalf("list = %d %s", listed.Code, listed.Body.String())
	}
	got := serve(server, http.MethodGet, "/v1/organizations/org_1/threads/thread_1", "", bearer)
	if got.Code != http.StatusOK || !strings.Contains(got.Body.String(), `"items":[{"type":"message","author":"member","text":"hi"}]`) {
		t.Fatalf("get = %d %s", got.Code, got.Body.String())
	}
	if missing := serve(server, http.MethodGet, "/v1/organizations/org_1/threads/other", "", bearer); missing.Code != http.StatusNotFound {
		t.Fatalf("get another = %d, want 404", missing.Code)
	}
	if deleted := serve(server, http.MethodDelete, "/v1/organizations/org_1/threads/thread_1", "", bearer); deleted.Code != http.StatusNoContent {
		t.Fatalf("delete = %d", deleted.Code)
	}
	for _, seen := range service.members {
		if seen.UserID != "user_1" || seen.OrganizationID != "org_1" || seen.AccessToken != "member-token" {
			t.Fatalf("member = %+v", seen)
		}
	}
}

func TestARunIsRefusedBeforeItsStreamOpens(t *testing.T) {
	cases := []struct {
		body   string
		err    error
		status int
		code   string
	}{
		{`{"message":"hi","extra":1}`, nil, http.StatusBadRequest, "invalid_request"},
		{`{"confirmations":[{"id":"c1"}]}`, nil, http.StatusBadRequest, "invalid_request"},
		{`{"message":"hi"}`, threads.ErrRunInProgress, http.StatusConflict, "run_in_progress"},
		{`{"message":"hi"}`, threads.ErrConfirmationPending, http.StatusConflict, "confirmation_pending"},
		{`{"message":"hi"}`, threads.ErrNotFound, http.StatusNotFound, "not_found"},
		{`{"message":"hi"}`, fmt.Errorf("%w: send a message", threads.ErrInvalid), http.StatusBadRequest, "invalid_request"},
		{`{"message":"hi"}`, errors.New("database down"), http.StatusInternalServerError, "internal"},
	}
	for _, testCase := range cases {
		service := &fakeThreads{startErr: testCase.err, run: &fakeRun{}}
		response := serve(newTestServer(member(), service), http.MethodPost, "/v1/organizations/org_1/threads/thread_1/runs", testCase.body, bearer)
		if response.Code != testCase.status || errorCode(t, response) != testCase.code {
			t.Errorf("%s (%v) = %d %s, want %d %s", testCase.body, testCase.err, response.Code, response.Body.String(), testCase.status, testCase.code)
		}
		if strings.Contains(response.Body.String(), "database down") {
			t.Errorf("an internal error reached the caller: %s", response.Body.String())
		}
	}
	// The sentinel's own words are dropped from what the caller is told.
	service := &fakeThreads{startErr: fmt.Errorf("%w: send a message", threads.ErrInvalid)}
	response := serve(newTestServer(member(), service), http.MethodPost, "/v1/organizations/org_1/threads/thread_1/runs", `{"message":"hi"}`, bearer)
	if !strings.Contains(response.Body.String(), `"message":"send a message"`) {
		t.Fatalf("invalid message = %s", response.Body.String())
	}
}

func TestARunStreamsItsItemsAsServerSentEvents(t *testing.T) {
	run := &fakeRun{items: []threads.Item{
		{Type: threads.ItemRunStarted, RunID: "run_1"},
		{Type: threads.ItemDelta, Author: "krude", Text: "Hel"},
		{Type: threads.ItemMessage, ID: "e1", Author: "krude", Text: "Hello"},
		{Type: threads.ItemRunCompleted, RunID: "run_1"},
	}}
	service := &fakeThreads{run: run}
	response := serve(newTestServer(member(), service), http.MethodPost, "/v1/organizations/org_1/threads/thread_1/runs",
		`{"confirmations":[{"id":"c1","confirmed":true},{"id":"c2","confirmed":false}]}`, bearer)
	if response.Code != http.StatusOK || response.Header().Get("Content-Type") != "text/event-stream" || response.Header().Get("X-Oryxa-Run") != "run_1" {
		t.Fatalf("stream = %d %v", response.Code, response.Header())
	}
	if !run.executed || run.discarded != "" {
		t.Fatalf("run executed %t, discarded %q", run.executed, run.discarded)
	}
	want := []threads.RunInput{{Confirmations: []threads.Answer{{ID: "c1", Confirmed: true}, {ID: "c2", Confirmed: false}}}}
	if fmt.Sprint(service.inputs) != fmt.Sprint(want) {
		t.Fatalf("inputs = %+v, want %+v", service.inputs, want)
	}

	var names []string
	var messages []threads.Item
	scanner := bufio.NewScanner(strings.NewReader(response.Body.String()))
	for scanner.Scan() {
		line := scanner.Text()
		if name, found := strings.CutPrefix(line, "event: "); found {
			names = append(names, name)
		}
		if data, found := strings.CutPrefix(line, "data: "); found {
			var item threads.Item
			if err := json.Unmarshal([]byte(data), &item); err != nil {
				t.Fatalf("data %q: %v", data, err)
			}
			messages = append(messages, item)
		}
	}
	if strings.Join(names, ",") != "run_started,delta,message,run_completed" {
		t.Fatalf("events = %v", names)
	}
	if messages[2].Text != "Hello" || messages[2].ID != "e1" {
		t.Fatalf("message item = %+v", messages[2])
	}
}

func TestArtifactsAreServedWithoutEverRunningAsAPage(t *testing.T) {
	server := newTestServer(member(), &fakeThreads{})
	image := serve(server, http.MethodGet, "/v1/organizations/org_1/threads/thread_1/artifacts/screenshot.png", "", bearer)
	if image.Code != http.StatusOK || image.Body.String() != "png-bytes" || image.Header().Get("Content-Type") != "image/png" ||
		!strings.HasPrefix(image.Header().Get("Content-Disposition"), "inline") {
		t.Fatalf("image = %d %v", image.Code, image.Header())
	}
	page := serve(server, http.MethodGet, "/v1/organizations/org_1/threads/thread_1/artifacts/report.html?version=2", "", bearer)
	if page.Code != http.StatusOK || !strings.HasPrefix(page.Header().Get("Content-Disposition"), "attachment") ||
		!strings.Contains(page.Header().Get("Content-Security-Policy"), "sandbox") || page.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("an HTML artifact could render as a page: %d %v", page.Code, page.Header())
	}
	members := serve(server, http.MethodGet, "/v1/organizations/org_1/threads/thread_1/artifacts/user:plan.txt", "", bearer)
	if members.Code != http.StatusOK || members.Header().Get("Content-Type") != "text/plain; charset=utf-8" ||
		!strings.Contains(members.Header().Get("Content-Disposition"), `filename=plan.txt`) {
		t.Fatalf("the member's own file = %d %v", members.Code, members.Header())
	}
	if nested := serve(server, http.MethodGet, "/v1/organizations/org_1/threads/thread_1/artifacts/a/b.txt", "", bearer); nested.Code != http.StatusNotFound {
		t.Fatalf("a name with a path in it = %d, want 404", nested.Code)
	}
	if missing := serve(server, http.MethodGet, "/v1/organizations/org_1/threads/thread_2/artifacts/screenshot.png", "", bearer); missing.Code != http.StatusNotFound {
		t.Fatalf("another thread's artifact = %d, want 404", missing.Code)
	}
	if bad := serve(server, http.MethodGet, "/v1/organizations/org_1/threads/thread_1/artifacts/screenshot.png?version=0", "", bearer); bad.Code != http.StatusBadRequest {
		t.Fatalf("version 0 = %d, want 400", bad.Code)
	}
	if anonymous := serve(server, http.MethodGet, "/v1/organizations/org_1/threads/thread_1/artifacts/screenshot.png", "", nil); anonymous.Code != http.StatusUnauthorized {
		t.Fatalf("without a member = %d, want 401", anonymous.Code)
	}
}

// TestABrokenAgentLookupOnlyFailsRoutesThatNeedAnAgentIdentity is the
// regression test for member() resolving the primary agent on every
// authenticated route: a thread is the member's own conversation and never
// needed an agent identity, so a database blip resolving one must not turn
// listing threads into a 500. An agent request, in contrast, is between two
// agents: it must still fail loudly rather than act with an empty identity.
func TestABrokenAgentLookupOnlyFailsRoutesThatNeedAnAgentIdentity(t *testing.T) {
	managed := &fakeManagedAgents{primaryErr: errors.New("agent registry is down")}
	server := New(member(), &fakeThreads{}, Config{
		AllowedOrigins: []string{"https://app.oryxa.test"}, RunTimeout: time.Minute,
		Agents: managed,
	}, nil)

	threadsResponse := serve(server, http.MethodGet, "/v1/organizations/org_1/threads", "", bearer)
	if threadsResponse.Code != http.StatusOK {
		t.Fatalf("threads = %d %s, want 200: a broken agent lookup must not fail a route that never needed an agent identity", threadsResponse.Code, threadsResponse.Body.String())
	}

	inbox := serve(server, http.MethodGet, "/v1/organizations/org_1/agent-requests/inbox", "", bearer)
	if inbox.Code != http.StatusInternalServerError || errorCode(t, inbox) != "internal" {
		t.Fatalf("inbox = %d %s, want 500 internal: it acts as the member's own agent and cannot proceed with no identity", inbox.Code, inbox.Body.String())
	}
}

// chunkedBody is an io.Reader whose concrete type is none of the ones
// net/http special-cases when it builds a request (*bytes.Buffer,
// *bytes.Reader, *strings.Reader), so it leaves ContentLength at -1 even
// when nothing is left to read - the same as a real request sent chunked,
// with no Content-Length header at all.
type chunkedBody struct{ io.Reader }

// TestStartingAWorkflowWithNoBodyAndAnUnknownLengthStillStarts is the
// regression test for startAgentWorkflow treating request.ContentLength !=
// 0 as "there is a body to decode": ContentLength is -1, not 0, when the
// length is unknown (chunked transfer encoding), so a perfectly valid
// bodyless start was answered 400 and the workflow never started.
func TestStartingAWorkflowWithNoBodyAndAnUnknownLengthStillStarts(t *testing.T) {
	workflows := &fakeAgentWorkflows{workflow: agentworkflows.Workflow{ID: "awf_pipeline"}}
	server := New(member(), &fakeThreads{}, Config{
		AllowedOrigins: []string{"https://app.oryxa.test"}, RunTimeout: time.Minute,
		Workflows: workflows,
	}, nil)

	request := httptest.NewRequest(http.MethodPost, "/v1/organizations/org_1/agent-workflows/awf_pipeline/executions", chunkedBody{strings.NewReader("")})
	if request.ContentLength != -1 {
		t.Fatalf("ContentLength = %d, want -1 for an unknown-length body, or this test is not exercising the bug", request.ContentLength)
	}
	request.Header.Set("Authorization", "Bearer member-token")
	recorder := httptest.NewRecorder()
	server.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusAccepted || workflows.execution.ID != "awx_1" {
		t.Fatalf("start = %d %s, want 202 and a started execution", recorder.Code, recorder.Body.String())
	}
}

// TestEveryListingRefusesTheSameBadLimit: one API, one answer. A limit that
// is not a positive number used to be ignored on some routes and refused on
// others.
func TestEveryListingRefusesTheSameBadLimit(t *testing.T) {
	managed := &fakeManagedAgents{definitions: []managedagents.Definition{{ID: "agt_primary", Kind: managedagents.KindPrimary, Name: "Carl", Status: managedagents.StatusActive}}}
	server := New(member(), &fakeThreads{}, Config{
		Agents: managed, Interactions: &fakeInteractions{},
	}, nil)
	for _, path := range []string{
		"/v1/organizations/org_1/agent-requests/inbox",
		"/v1/organizations/org_1/agent-requests/sent",
	} {
		response := serve(server, http.MethodGet, path+"?limit=abc", "", bearer)
		if response.Code != http.StatusBadRequest || errorCode(t, response) != "invalid_request" {
			t.Errorf("GET %s?limit=abc = %d %s, want 400", path, response.Code, response.Body.String())
		}
		if response := serve(server, http.MethodGet, path, "", bearer); response.Code != http.StatusOK {
			t.Errorf("GET %s with no limit = %d %s, want 200", path, response.Code, response.Body.String())
		}
	}
}

// TestAnInUseTeamAnswersConflictRatherThanAnInternalError: a team a workflow
// still runs cannot be deleted, and the member is told which — not shown the
// internal error a raw database refusal would become.
func TestAnInUseTeamAnswersConflictRatherThanAnInternalError(t *testing.T) {
	teams := &fakeAgentTeams{deleteErr: fmt.Errorf("%w: delete the workflows that run this team first", agentteams.ErrInUse)}
	server := New(member(), &fakeThreads{}, Config{Agents: &fakeManagedAgents{
		definitions: []managedagents.Definition{{ID: "agt_primary", Kind: managedagents.KindPrimary, Name: "Carl", Status: managedagents.StatusActive}},
	}, Teams: teams}, nil)
	response := serve(server, http.MethodDelete, "/v1/organizations/org_1/agent-teams/atm_revenue", "", bearer)
	if response.Code != http.StatusConflict || errorCode(t, response) != "team_in_use" {
		t.Fatalf("delete an in-use team = %d %s, want 409 team_in_use", response.Code, response.Body.String())
	}
	if !strings.Contains(response.Body.String(), "workflows") {
		t.Fatalf("the answer does not say what is still using the team: %s", response.Body.String())
	}
}
