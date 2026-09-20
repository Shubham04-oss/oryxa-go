package agentteams

import (
	"context"
	"errors"
	"testing"
)

type fakeRepository struct {
	team  Team
	input UpdateInput
}

func (repo *fakeRepository) Create(_ context.Context, scope Scope, team Team) (Team, error) {
	team.OrganizationID, team.OwnerUserID = scope.OrganizationID, scope.UserID
	repo.team = team
	return team, nil
}

func (repo *fakeRepository) List(context.Context, Scope) ([]Team, error) {
	return []Team{repo.team}, nil
}

func (repo *fakeRepository) Get(context.Context, Scope, string) (Team, error) {
	if repo.team.ID == "" {
		return Team{}, ErrNotFound
	}
	return repo.team, nil
}

func (repo *fakeRepository) Update(_ context.Context, _ Scope, _ string, input UpdateInput) (Team, error) {
	repo.input = input
	if input.Name != nil {
		repo.team.Name = *input.Name
	}
	if input.Description != nil {
		repo.team.Description = *input.Description
	}
	if input.Status != nil {
		repo.team.Status = *input.Status
	}
	if input.Members != nil {
		repo.team.Members = memberStubs(*input.Members)
	}
	return repo.team, nil
}

func (repo *fakeRepository) Delete(context.Context, Scope, string) error { return nil }

func TestCreateNormalizesAReusableTeamSpecification(t *testing.T) {
	repo := &fakeRepository{}
	service, err := New(repo)
	if err != nil {
		t.Fatal(err)
	}
	created, err := service.Create(context.Background(), Scope{OrganizationID: "org_1", UserID: "user_1"}, CreateInput{
		Name: "  Revenue   Operations ", Description: "  Qualify the pipeline.  ",
		Members: []MemberInput{{AgentID: " agt_sales ", Role: "  Qualify   opportunities "}, {AgentID: "agt_engineering", Role: "Answer technical questions"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if created.ID == "" || created.Name != "Revenue Operations" || created.Description != "Qualify the pipeline." {
		t.Fatalf("created=%+v", created)
	}
	if len(created.Members) != 2 || created.Members[0].AgentID != "agt_sales" || created.Members[0].Role != "Qualify opportunities" || created.Members[1].Position != 1 {
		t.Fatalf("members=%+v", created.Members)
	}
}

func TestTeamValidationKeepsMembershipUnambiguous(t *testing.T) {
	repo := &fakeRepository{}
	service, _ := New(repo)
	scope := Scope{OrganizationID: "org_1", UserID: "user_1"}

	_, err := service.Create(context.Background(), scope, CreateInput{Name: "Revenue", Members: []MemberInput{
		{AgentID: "agt_sales", Role: "Qualify"}, {AgentID: "agt_sales", Role: "Close"},
	}})
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("duplicate member error=%v", err)
	}
	if _, err := service.Update(context.Background(), scope, "atm_1", UpdateInput{}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("empty update error=%v", err)
	}
}

func TestUpdateValidatesTheCompleteResult(t *testing.T) {
	repo := &fakeRepository{team: Team{
		ID: "atm_1", Name: "Revenue", Status: StatusActive,
		Members: []Member{{AgentID: "agt_sales", Role: "Qualify", Position: 0}},
	}}
	service, _ := New(repo)
	empty := []MemberInput{}
	if _, err := service.Update(context.Background(), Scope{OrganizationID: "org_1", UserID: "user_1"}, "atm_1", UpdateInput{Members: &empty}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("empty membership error=%v", err)
	}
	disabled := StatusDisabled
	updated, err := service.Update(context.Background(), Scope{OrganizationID: "org_1", UserID: "user_1"}, "atm_1", UpdateInput{Status: &disabled})
	if err != nil || updated.Status != StatusDisabled {
		t.Fatalf("updated=%+v error=%v", updated, err)
	}
}
