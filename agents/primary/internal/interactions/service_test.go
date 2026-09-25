package interactions

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/agents"
)

type fakeDirectory struct{ directory agents.OrganizationDirectory }

func (directory fakeDirectory) Load(context.Context, string, string) (agents.OrganizationDirectory, error) {
	return directory.directory, nil
}
func (fakeDirectory) FindPeople(context.Context, string, string, string, int) ([]agents.DirectoryPerson, error) {
	return nil, nil
}
func (fakeDirectory) FindTeams(context.Context, string, string, string, int) ([]agents.DirectoryTeam, error) {
	return nil, nil
}
func (fakeDirectory) FindAgents(context.Context, string, string, string, int) ([]agents.DirectoryAgent, error) {
	return nil, nil
}

// fakeRepository is the ledger in memory. Like Store, it keeps one request
// per organization, requesting agent and idempotency key: Create under a key
// already used stores nothing and returns the request stored under it.
type fakeRepository struct {
	requests map[string]agents.Interaction
	keys     map[string]string
	creates  int
	holds    int
}

func newFakeRepository() *fakeRepository {
	return &fakeRepository{requests: map[string]agents.Interaction{}, keys: map[string]string{}}
}

func keyScope(organizationID, requesterAgentID, key string) string {
	return organizationID + "|" + requesterAgentID + "|" + key
}

func (repo *fakeRepository) Create(_ context.Context, request agents.Interaction, key string) (agents.Interaction, error) {
	repo.creates++
	scope := keyScope(request.OrganizationID, request.Requester.AgentID, key)
	if id, ok := repo.keys[scope]; ok {
		return repo.requests[id], nil
	}
	repo.keys[scope] = request.ID
	repo.requests[request.ID] = request
	return request, nil
}
func (repo *fakeRepository) FindByKey(_ context.Context, organizationID, requesterAgentID, key string) (agents.Interaction, error) {
	id, ok := repo.keys[keyScope(organizationID, requesterAgentID, key)]
	if !ok {
		return agents.Interaction{}, ErrNotFound
	}
	return repo.requests[id], nil
}
func (repo *fakeRepository) Get(_ context.Context, id string) (agents.Interaction, error) {
	request, ok := repo.requests[id]
	if !ok {
		return agents.Interaction{}, ErrNotFound
	}
	return request, nil
}
func (repo *fakeRepository) Inbox(_ context.Context, organizationID, userID string, _ int) ([]agents.Interaction, error) {
	var found []agents.Interaction
	for _, request := range repo.requests {
		if request.OrganizationID == organizationID && request.Target.UserID == userID {
			found = append(found, request)
		}
	}
	return found, nil
}
func (repo *fakeRepository) Sent(_ context.Context, organizationID, userID string, _ int) ([]agents.Interaction, error) {
	var found []agents.Interaction
	for _, request := range repo.requests {
		if request.OrganizationID == organizationID && request.Requester.UserID == userID {
			found = append(found, request)
		}
	}
	return found, nil
}
func (repo *fakeRepository) Decide(_ context.Context, id string, input agents.InteractionDecisionInput, decider agents.InteractionDecider, at time.Time) (agents.Interaction, error) {
	request := repo.requests[id]
	request.Status = StatusRejected
	if input.Decision == DecisionApproved {
		request.Status = StatusApproved
	}
	request.Decision = &agents.InteractionDecision{Decision: input.Decision, Message: input.Message, AllowedTools: input.AllowedTools, GrantedPermissions: input.GrantedPermissions, DecidedAt: at, DecidedBy: &decider}
	request.Revision++
	repo.requests[id] = request
	return request, nil
}

// Hold and Close change the fake ledger as Store does.
func (repo *fakeRepository) Hold(_ context.Context, id string, at time.Time) (agents.Interaction, error) {
	request, ok := repo.requests[id]
	switch {
	case !ok:
		return agents.Interaction{}, ErrNotFound
	case !request.ExpiresAt.After(at):
		return agents.Interaction{}, ErrExpired
	case request.Status == StatusPending && request.Approval == ApprovalOwner:
		return request, nil
	case request.Status == StatusPending:
	case request.Status == StatusApproved:
		request.Status, request.Decision = StatusPending, nil
	default:
		return agents.Interaction{}, ErrConflict
	}
	request.Approval = ApprovalOwner
	request.Revision++
	request.UpdatedAt = at
	repo.requests[id] = request
	repo.holds++
	return request, nil
}
func (repo *fakeRepository) Close(_ context.Context, id, reason string, at time.Time) (agents.Interaction, error) {
	request, ok := repo.requests[id]
	switch {
	case !ok:
		return agents.Interaction{}, ErrNotFound
	case !request.ExpiresAt.After(at):
		return agents.Interaction{}, ErrExpired
	case request.Status == StatusPending:
		request.Status = StatusRejected
		request.Decision = &agents.InteractionDecision{Decision: DecisionRejected, Message: reason, AllowedTools: []string{}, GrantedPermissions: []string{}, DecidedAt: at,
			DecidedBy: &agents.InteractionDecider{Kind: DecidedBySystem}}
	case request.Status == StatusApproved:
		request.Status = StatusFailed
		request.Result = &agents.InteractionResult{Status: StatusFailed, Summary: reason, ToolsUsed: []string{}, Artifacts: []string{}, CompletedAt: at}
	default:
		return agents.Interaction{}, ErrConflict
	}
	request.Revision++
	request.UpdatedAt = at
	repo.requests[id] = request
	return request, nil
}
func (repo *fakeRepository) Respond(_ context.Context, id string, input agents.InteractionResponseInput, at time.Time) (agents.Interaction, error) {
	request := repo.requests[id]
	request.Status = input.Status
	request.Result = &agents.InteractionResult{Status: input.Status, Summary: input.Summary, Content: input.Content, ToolsUsed: input.ToolsUsed, Artifacts: input.Artifacts, CompletedAt: at}
	request.Revision++
	repo.requests[id] = request
	return request, nil
}

var testNow = time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)

func testService(t *testing.T) (*Service, *fakeRepository) {
	t.Helper()
	repo := newFakeRepository()
	service, err := New(Config{Repository: repo, Directory: fakeDirectory{directory: agents.OrganizationDirectory{
		People: []agents.DirectoryPerson{{UserID: "user_b", Email: "b@example.com", DisplayName: "B", Agents: []agents.DirectoryAgent{{
			ID: "agt_engineering", OwnerUserID: "user_b", OwnerEmail: "b@example.com", OwnerDisplayName: "B", Kind: "specialist", Name: "Engineering",
		}}}},
	}}, Now: func() time.Time { return testNow }})
	if err != nil {
		t.Fatal(err)
	}
	return service, repo
}

var requester = agents.InteractionActor{OrganizationID: "org_1", UserID: "user_a", AgentID: "agt_a", AgentName: "Crew", AgentKind: "primary"}
var target = agents.InteractionActor{OrganizationID: "org_1", UserID: "user_b", AgentID: "agt_engineering", AgentName: "Engineering", AgentKind: "specialist"}
var targetPrimary = agents.InteractionActor{OrganizationID: "org_1", UserID: "user_b", AgentID: "agt_b", AgentName: "Carl", AgentKind: "primary"}

func TestCreateResolvesTheTargetAndRecordsStrictScope(t *testing.T) {
	service, _ := testService(t)
	created, err := service.Create(context.Background(), requester, "member-token", "inv:call", agents.InteractionCreateInput{
		TargetAgentID: "agt_engineering", Purpose: "Architecture question", Task: "Explain the deployment boundary.",
		RequestedTools: []string{" knowledge_search ", "knowledge_search"}, RequestedPermissions: []string{"read:architecture"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if created.Requester.UserID != "user_a" || created.Target.UserID != "user_b" || created.Target.UserEmail != "b@example.com" ||
		created.Status != StatusPending || len(created.RequestedTools) != 1 || created.ExpiresAt.Sub(created.CreatedAt) != defaultExpiry {
		t.Fatalf("created=%+v", created)
	}
	if _, err := service.Create(context.Background(), requester, "member-token", "other", agents.InteractionCreateInput{
		TargetAgentID: "agt_missing", Purpose: "Question", Task: "Answer.",
	}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing target error=%v", err)
	}
}

func TestTargetOrPrimaryCanDecideWithinRequestedScope(t *testing.T) {
	service, _ := testService(t)
	created, err := service.Create(context.Background(), requester, "token", "key", agents.InteractionCreateInput{
		TargetAgentID: "agt_engineering", Purpose: "Question", Task: "Answer.",
		RequestedTools: []string{"knowledge_search"}, RequestedPermissions: []string{"read:architecture"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Decide(context.Background(), requester, created.ID, agents.InteractionDecisionInput{Decision: DecisionApproved, Message: "No"}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("requester decision error=%v", err)
	}
	if _, err := service.Decide(context.Background(), target, created.ID, agents.InteractionDecisionInput{
		Decision: DecisionApproved, Message: "Approved", AllowedTools: []string{"apps_write"},
	}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("broadened decision error=%v", err)
	}
	approved, err := service.Decide(context.Background(), targetPrimary, created.ID, agents.InteractionDecisionInput{
		Decision: DecisionApproved, Message: "Approved", AllowedTools: []string{"knowledge_search"}, GrantedPermissions: []string{"read:architecture"},
	})
	if err != nil || approved.Status != StatusApproved || approved.Decision == nil {
		t.Fatalf("approved=%+v err=%v", approved, err)
	}
}

func TestResponseCannotClaimAnUnapprovedTool(t *testing.T) {
	service, _ := testService(t)
	created, _ := service.Create(context.Background(), requester, "token", "key", agents.InteractionCreateInput{
		TargetAgentID: "agt_engineering", Purpose: "Question", Task: "Answer.", RequestedTools: []string{"knowledge_search"},
	})
	_, _ = service.Decide(context.Background(), target, created.ID, agents.InteractionDecisionInput{
		Decision: DecisionApproved, Message: "Approved", AllowedTools: []string{"knowledge_search"},
	})
	if _, err := service.Respond(context.Background(), target, created.ID, agents.InteractionResponseInput{
		Status: StatusCompleted, Summary: "Done", ToolsUsed: []string{"apps_write"},
	}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("unapproved tool error=%v", err)
	}
	completed, err := service.Respond(context.Background(), target, created.ID, agents.InteractionResponseInput{
		Status: StatusCompleted, Summary: "Done", Content: "Use the deployment API.", ToolsUsed: []string{"knowledge_search"},
	})
	if err != nil || completed.Status != StatusCompleted || completed.Result == nil {
		t.Fatalf("completed=%+v err=%v", completed, err)
	}
}

func TestExpiredRequestCannotBeDecided(t *testing.T) {
	service, repo := testService(t)
	created, _ := service.Create(context.Background(), requester, "token", "key", agents.InteractionCreateInput{
		TargetAgentID: "agt_engineering", Purpose: "Question", Task: "Answer.", ExpiresInSeconds: 300,
	})
	request := repo.requests[created.ID]
	request.ExpiresAt = testNow.Add(-time.Second)
	repo.requests[created.ID] = request
	read, err := service.Get(context.Background(), target, created.ID)
	if err != nil || read.Status != StatusExpired {
		t.Fatalf("read=%+v err=%v", read, err)
	}
	if _, err := service.Decide(context.Background(), target, created.ID, agents.InteractionDecisionInput{Decision: DecisionRejected, Message: "Too late"}); !errors.Is(err, ErrExpired) {
		t.Fatalf("expired decision error=%v", err)
	}
}

func TestSpecialistsSeeOnlyTheirOwnRequestsWhilePrimaryMediatesAll(t *testing.T) {
	service, repo := testService(t)
	request := agents.Interaction{
		ID: "irq_other", OrganizationID: "org_1",
		Requester: agents.InteractionParty{UserID: "user_a", AgentID: "agt_a"},
		Target:    agents.InteractionParty{UserID: "user_b", AgentID: "agt_legal"},
		Status:    StatusPending, ExpiresAt: testNow.Add(time.Hour),
	}
	repo.requests[request.ID] = request
	if inbox, err := service.Inbox(context.Background(), target, 20); err != nil || len(inbox) != 0 {
		t.Fatalf("specialist inbox=%+v err=%v", inbox, err)
	}
	if _, err := service.Get(context.Background(), target, request.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("specialist read other request error=%v", err)
	}
	if inbox, err := service.Inbox(context.Background(), targetPrimary, 20); err != nil || len(inbox) != 1 {
		t.Fatalf("primary inbox=%+v err=%v", inbox, err)
	}
}
