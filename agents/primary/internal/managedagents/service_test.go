package managedagents

import (
	"context"
	"errors"
	"testing"

	"google.golang.org/adk/v2/session"

	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/adksession"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/threads"
)

type fakeRepository struct {
	primary Definition
	agents  map[string]Definition
	created Definition
	updated UpdateInput
	ensured int
}

func newFakeRepository() *fakeRepository {
	primary := Definition{ID: "agt_primary", OrganizationID: "org_1", OwnerUserID: "user_1", Kind: KindPrimary, Name: "Carl-X7P4", Status: StatusActive, Revision: 1}
	return &fakeRepository{primary: primary, agents: map[string]Definition{primary.ID: primary}}
}

func (repo *fakeRepository) EnsurePrimary(context.Context, Scope) (Definition, error) {
	repo.ensured++
	return repo.primary, nil
}
func (repo *fakeRepository) Create(_ context.Context, scope Scope, definition Definition) (Definition, error) {
	definition.ID, definition.OrganizationID, definition.OwnerUserID = "agt_sales", scope.OrganizationID, scope.UserID
	definition.Kind, definition.Status, definition.Revision = KindSpecialist, StatusActive, 1
	repo.created = definition
	repo.agents[definition.ID] = definition
	return definition, nil
}
func (repo *fakeRepository) List(context.Context, Scope) ([]Definition, error) {
	return []Definition{repo.primary}, nil
}
func (repo *fakeRepository) Get(_ context.Context, scope Scope, id string) (Definition, error) {
	definition, ok := repo.agents[id]
	if !ok || definition.OrganizationID != scope.OrganizationID || definition.OwnerUserID != scope.UserID {
		return Definition{}, ErrNotFound
	}
	return definition, nil
}
func (repo *fakeRepository) Update(_ context.Context, _ Scope, id string, input UpdateInput) (Definition, error) {
	repo.updated = input
	definition := repo.agents[id]
	if input.Name != nil {
		definition.Name = *input.Name
	}
	if input.Description != nil {
		definition.Description = *input.Description
	}
	if input.Instructions != nil {
		definition.Instructions = *input.Instructions
	}
	if input.Status != nil {
		definition.Status = *input.Status
	}
	repo.agents[id] = definition
	return definition, nil
}
func (repo *fakeRepository) Delete(_ context.Context, _ Scope, id string) error {
	if _, ok := repo.agents[id]; !ok {
		return ErrNotFound
	}
	delete(repo.agents, id)
	return nil
}

func TestListEnsuresTheMembersPrimaryAgent(t *testing.T) {
	repo := newFakeRepository()
	service := &Service{repository: repo}
	listed, err := service.List(context.Background(), Scope{OrganizationID: "org_1", UserID: "user_1"})
	if err != nil {
		t.Fatal(err)
	}
	if repo.ensured != 1 || len(listed) != 1 || listed[0].Kind != KindPrimary {
		t.Fatalf("ensure=%d agents=%+v", repo.ensured, listed)
	}
}

func TestPrimaryIsEnsuredOnAnyEntryPoint(t *testing.T) {
	repo := newFakeRepository()
	service := &Service{repository: repo}
	primary, err := service.Primary(context.Background(), Scope{OrganizationID: "org_1", UserID: "user_1"})
	if err != nil {
		t.Fatal(err)
	}
	if primary.ID != "agt_primary" || repo.ensured != 1 {
		t.Fatalf("primary=%+v ensured=%d", primary, repo.ensured)
	}
}

func TestCreateNormalizesAndValidatesASpecialist(t *testing.T) {
	repo := newFakeRepository()
	service := &Service{repository: repo}
	created, err := service.Create(context.Background(), Scope{OrganizationID: "org_1", UserID: "user_1"}, CreateInput{
		Name: "  Sales   Agent ", Description: "  Owns pipeline reviews.  ", Instructions: "  Review CRM opportunities.  ",
	})
	if err != nil {
		t.Fatal(err)
	}
	if repo.ensured != 1 || created.Name != "Sales Agent" || created.Description != "Owns pipeline reviews." || created.Instructions != "Review CRM opportunities." {
		t.Fatalf("created = %+v; ensured=%d", created, repo.ensured)
	}
	_, err = service.Create(context.Background(), Scope{OrganizationID: "org_1", UserID: "user_1"}, CreateInput{Name: "Legal"})
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("missing instructions error = %v", err)
	}
}

func TestUpdateCanRenameThePrimaryButNeedsARealChange(t *testing.T) {
	repo := newFakeRepository()
	service := &Service{repository: repo}
	name := "  Crew  "
	updated, err := service.Update(context.Background(), Scope{OrganizationID: "org_1", UserID: "user_1"}, repo.primary.ID, UpdateInput{Name: &name})
	if err != nil {
		t.Fatal(err)
	}
	if updated.Name != "Crew" || repo.updated.Name == nil || *repo.updated.Name != "Crew" {
		t.Fatalf("updated = %+v, input = %+v", updated, repo.updated)
	}
	_, err = service.Update(context.Background(), Scope{OrganizationID: "org_1", UserID: "user_1"}, repo.primary.ID, UpdateInput{})
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("empty update error = %v", err)
	}
}

func TestDisabledAgentCannotRun(t *testing.T) {
	repo := newFakeRepository()
	disabled := Definition{ID: "agt_legal", OrganizationID: "org_1", OwnerUserID: "user_1", Kind: KindSpecialist, Name: "Legal", Instructions: "Review.", Status: StatusDisabled}
	repo.agents[disabled.ID] = disabled
	service := &Service{repository: repo}
	_, err := service.StartRun(context.Background(), threads.Member{OrganizationID: "org_1", UserID: "user_1"}, disabled.ID, "", threads.RunInput{Message: "Review this"})
	if !errors.Is(err, ErrDisabled) {
		t.Fatalf("StartRun error = %v", err)
	}
}

func TestAppNameDoesNotChangeWithDisplayName(t *testing.T) {
	if got := appName("agt_ab-cd"); got != "managed_agt_ab_cd" {
		t.Fatalf("appName = %q", got)
	}
}

func TestDeleteThreadWorksAfterTheAgentDefinitionIsGone(t *testing.T) {
	ctx := context.Background()
	sessions := session.InMemoryService()
	created, err := sessions.Create(ctx, &session.CreateRequest{
		AppName: appName("agt_deleted"), UserID: "user_1",
		State: map[string]any{adksession.StateOrganizationID: "org_1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	service := &Service{sessions: sessions}
	if err := service.DeleteThread(ctx, threads.Member{OrganizationID: "org_1", UserID: "user_1"}, "agt_deleted", created.Session.ID()); err != nil {
		t.Fatal(err)
	}
	_, err = sessions.Get(ctx, &session.GetRequest{
		AppName: appName("agt_deleted"), UserID: "user_1", SessionID: created.Session.ID(),
	})
	if !errors.Is(err, session.ErrNotFound) {
		t.Fatalf("deleted session error=%v", err)
	}
}
