package application

import (
	"context"
	"errors"
	"log/slog"
	"slices"
	"strings"
	"testing"
	"time"

	autheventsv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/auth/v1"
	teamseventsv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/teams/v1"

	"github.com/Shubham04-oss/oryxa-go/teams/internal/domain"
)

func TestCreateTeamRequiresOrgAdmin(t *testing.T) {
	service, _, _ := newService()
	_, err := service.CreateTeam(context.Background(), creds("user-member"), "org_1", CreateTeamInput{Name: "Engineering"})
	if !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("CreateTeam() error = %v, want forbidden", err)
	}
}

func TestCreateTeamAddsCreatorAsAdminAndEmitsEvents(t *testing.T) {
	service, store, _ := newService()
	team, err := service.CreateTeam(context.Background(), creds("user-owner"), "org_1", CreateTeamInput{Name: " Engineering "})
	if err != nil {
		t.Fatalf("CreateTeam() error = %v", err)
	}
	if team.Name != "Engineering" || team.OrganizationID != "org_1" || !strings.HasPrefix(team.ID, "team_") || team.Path != domain.TeamPath("org_1", team.ID) {
		t.Fatalf("team = %+v", team)
	}
	membership, err := store.Membership(context.Background(), team.ID, "user-owner")
	if err != nil || membership.Role != domain.RoleAdmin || membership.Path != domain.MemberPath(team.Path, "user-owner") {
		t.Fatalf("creator membership = %+v, err = %v", membership, err)
	}
	if !slices.Contains(store.detailTypes(), teamseventsv1.TeamCreatedDetailType) || !slices.Contains(store.detailTypes(), teamseventsv1.MemberAddedDetailType) {
		t.Fatalf("events = %v", store.detailTypes())
	}
}

func TestTeamsVisibility(t *testing.T) {
	service, _, _ := newService()
	ctx := context.Background()
	created, err := service.CreateTeam(ctx, creds("user-owner"), "org_1", CreateTeamInput{Name: "Engineering"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.AddMember(ctx, creds("user-owner"), "org_1", created.ID, AddMemberInput{UserID: "user-member", Role: domain.RoleMember}); err != nil {
		t.Fatal(err)
	}

	asOwner, err := service.Teams(ctx, creds("user-owner"), "org_1")
	if err != nil || len(asOwner) != 1 || !asOwner[0].CanManage {
		t.Fatalf("owner teams = %+v, err = %v", asOwner, err)
	}
	asMember, err := service.Teams(ctx, creds("user-member"), "org_1")
	if err != nil || len(asMember) != 1 || asMember[0].CanManage || asMember[0].Role != domain.RoleMember {
		t.Fatalf("member teams = %+v, err = %v", asMember, err)
	}

	other, err := service.CreateTeam(ctx, creds("user-owner"), "org_1", CreateTeamInput{Name: "Design"})
	if err != nil {
		t.Fatal(err)
	}
	asMember, err = service.Teams(ctx, creds("user-member"), "org_1")
	if err != nil || len(asMember) != 1 || asMember[0].ID != created.ID {
		t.Fatalf("member should not see %s: %+v", other.ID, asMember)
	}
	if _, err := service.Team(ctx, creds("user-member"), "org_1", other.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("Team() error = %v, want not found", err)
	}
}

func TestOrgAdminAndTeamAdminShareManageControl(t *testing.T) {
	service, _, _ := newService()
	ctx := context.Background()
	team, err := service.CreateTeam(ctx, creds("user-owner"), "org_1", CreateTeamInput{Name: "Engineering"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.AddMember(ctx, creds("user-admin"), "org_1", team.ID, AddMemberInput{UserID: "user-member", Role: domain.RoleAdmin}); err != nil {
		t.Fatal(err)
	}
	if _, err := service.AddMember(ctx, creds("user-admin"), "org_1", team.ID, AddMemberInput{UserID: "user-outsider", Role: domain.RoleMember}); err != nil {
		t.Fatal(err)
	}

	renamed, err := service.UpdateTeam(ctx, creds("user-member"), "org_1", team.ID, UpdateTeamInput{Name: "Platform"})
	if err != nil || renamed.Name != "Platform" {
		t.Fatalf("team admin update = %+v, err = %v", renamed, err)
	}
	renamed, err = service.UpdateTeam(ctx, creds("user-admin"), "org_1", team.ID, UpdateTeamInput{Name: "Core"})
	if err != nil || renamed.Name != "Core" {
		t.Fatalf("org admin update = %+v, err = %v", renamed, err)
	}
	if _, err := service.UpdateTeam(ctx, creds("user-outsider"), "org_1", team.ID, UpdateTeamInput{Name: "Nope"}); !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("team member update error = %v, want forbidden", err)
	}
}

func TestAddMemberMustBelongToOrganization(t *testing.T) {
	service, _, _ := newService()
	ctx := context.Background()
	team, err := service.CreateTeam(ctx, creds("user-owner"), "org_1", CreateTeamInput{Name: "Engineering"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = service.AddMember(ctx, creds("user-owner"), "org_1", team.ID, AddMemberInput{UserID: "user-stranger", Role: domain.RoleMember})
	if !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("AddMember() error = %v, want invalid", err)
	}
}

func TestTeamMemberCannotAddMembers(t *testing.T) {
	service, _, _ := newService()
	ctx := context.Background()
	team, err := service.CreateTeam(ctx, creds("user-owner"), "org_1", CreateTeamInput{Name: "Engineering"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.AddMember(ctx, creds("user-owner"), "org_1", team.ID, AddMemberInput{UserID: "user-outsider", Role: domain.RoleMember}); err != nil {
		t.Fatal(err)
	}
	_, err = service.AddMember(ctx, creds("user-outsider"), "org_1", team.ID, AddMemberInput{UserID: "user-member", Role: domain.RoleMember})
	if !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("AddMember() error = %v, want forbidden", err)
	}
}

func TestLastTeamAdminCannotLeaveOrBeDemoted(t *testing.T) {
	service, _, _ := newService()
	ctx := context.Background()
	team, err := service.CreateTeam(ctx, creds("user-owner"), "org_1", CreateTeamInput{Name: "Engineering"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.ChangeMemberRole(ctx, creds("user-owner"), "org_1", team.ID, "user-owner", domain.RoleMember); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("demote last admin error = %v, want conflict", err)
	}
	if err := service.RemoveMember(ctx, creds("user-owner"), "org_1", team.ID, "user-owner"); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("leave as last admin error = %v, want conflict", err)
	}
}

func TestRemoveMemberReasons(t *testing.T) {
	service, store, _ := newService()
	ctx := context.Background()
	team, err := service.CreateTeam(ctx, creds("user-owner"), "org_1", CreateTeamInput{Name: "Engineering"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.AddMember(ctx, creds("user-owner"), "org_1", team.ID, AddMemberInput{UserID: "user-member", Role: domain.RoleMember}); err != nil {
		t.Fatal(err)
	}
	if err := service.RemoveMember(ctx, creds("user-member"), "org_1", team.ID, "user-member"); err != nil {
		t.Fatal(err)
	}
	if !containsReason(store.events, teamseventsv1.MemberRemovedReasonLeft) {
		t.Fatalf("leave reason missing in %+v", store.events)
	}

	if _, err := service.AddMember(ctx, creds("user-owner"), "org_1", team.ID, AddMemberInput{UserID: "user-member", Role: domain.RoleMember}); err != nil {
		t.Fatal(err)
	}
	if err := service.RemoveMember(ctx, creds("user-owner"), "org_1", team.ID, "user-member"); err != nil {
		t.Fatal(err)
	}
	if !containsReason(store.events, teamseventsv1.MemberRemovedReasonRemoved) {
		t.Fatalf("remove reason missing in %+v", store.events)
	}
}

func TestDeleteTeamEmitsMemberRemovals(t *testing.T) {
	service, store, _ := newService()
	ctx := context.Background()
	team, err := service.CreateTeam(ctx, creds("user-owner"), "org_1", CreateTeamInput{Name: "Engineering"})
	if err != nil {
		t.Fatal(err)
	}
	if err := service.DeleteTeam(ctx, creds("user-owner"), "org_1", team.ID); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(store.detailTypes(), teamseventsv1.TeamDeletedDetailType) || !containsReason(store.events, teamseventsv1.MemberRemovedReasonTeamDeleted) {
		t.Fatalf("events = %v", store.detailTypes())
	}
}

func TestOrganizationDeletedRemovesTeams(t *testing.T) {
	service, store, _ := newService()
	ctx := context.Background()
	team, err := service.CreateTeam(ctx, creds("user-owner"), "org_1", CreateTeamInput{Name: "Engineering"})
	if err != nil {
		t.Fatal(err)
	}
	if err := service.OrganizationDeleted(ctx, autheventsv1.OrganizationDeleted{
		OrganizationID: "org_1", Name: "Acme", DeletedBy: "user-owner", DeletedAt: time.Date(2026, 9, 13, 14, 0, 0, 0, time.UTC),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Team(ctx, "org_1", team.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("team still present: %v", err)
	}
	if !containsReason(store.events, teamseventsv1.MemberRemovedReasonOrganizationDeleted) {
		t.Fatalf("events = %v", store.detailTypes())
	}
}

func TestAuthMemberRemovedClearsTeamMemberships(t *testing.T) {
	service, store, _ := newService()
	ctx := context.Background()
	team, err := service.CreateTeam(ctx, creds("user-owner"), "org_1", CreateTeamInput{Name: "Engineering"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.AddMember(ctx, creds("user-owner"), "org_1", team.ID, AddMemberInput{UserID: "user-member", Role: domain.RoleMember}); err != nil {
		t.Fatal(err)
	}
	if err := service.MemberRemoved(ctx, autheventsv1.MemberRemoved{
		OrganizationID: "org_1", OrganizationName: "Acme", UserID: "user-member", Email: "member@example.com",
		Reason: autheventsv1.MemberRemovedReasonLeft, RemovedBy: "user-member", RemovedAt: time.Date(2026, 9, 13, 15, 0, 0, 0, time.UTC),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Membership(ctx, team.ID, "user-member"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("membership still present: %v", err)
	}
	if !containsReason(store.events, teamseventsv1.MemberRemovedReasonOrganizationMembershipEnded) {
		t.Fatalf("events = %v", store.detailTypes())
	}
}

func TestCreateNestedTeamRequiresParentManage(t *testing.T) {
	service, _, _ := newService()
	ctx := context.Background()
	parent, err := service.CreateTeam(ctx, creds("user-owner"), "org_1", CreateTeamInput{Name: "Engineering"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.AddMember(ctx, creds("user-owner"), "org_1", parent.ID, AddMemberInput{UserID: "user-member", Role: domain.RoleMember}); err != nil {
		t.Fatal(err)
	}
	if _, err := service.CreateTeam(ctx, creds("user-member"), "org_1", CreateTeamInput{Name: "Frontend", ParentID: parent.ID}); !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("member nest error = %v, want forbidden", err)
	}
	if _, err := service.CreateTeam(ctx, creds("user-member"), "org_1", CreateTeamInput{Name: "Hidden", ParentID: "team_missing"}); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("missing parent error = %v, want not found", err)
	}
	if _, err := service.AddMember(ctx, creds("user-owner"), "org_1", parent.ID, AddMemberInput{UserID: "user-outsider", Role: domain.RoleAdmin}); err != nil {
		t.Fatal(err)
	}
	child, err := service.CreateTeam(ctx, creds("user-outsider"), "org_1", CreateTeamInput{Name: "Frontend", ParentID: parent.ID})
	if err != nil || child.Path != domain.ChildPath(parent.Path, child.ID) {
		t.Fatalf("child = %+v, err = %v", child, err)
	}
	if _, err := service.Team(ctx, creds("user-member"), "org_1", child.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("member saw child: %v", err)
	}
}

func TestSearchAndDescendantsRespectVisibility(t *testing.T) {
	service, _, _ := newService()
	ctx := context.Background()
	parent, err := service.CreateTeam(ctx, creds("user-owner"), "org_1", CreateTeamInput{Name: "Engineering"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.CreateTeam(ctx, creds("user-owner"), "org_1", CreateTeamInput{Name: "Design"}); err != nil {
		t.Fatal(err)
	}
	child, err := service.CreateTeam(ctx, creds("user-owner"), "org_1", CreateTeamInput{Name: "Frontend", ParentID: parent.ID})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.AddMember(ctx, creds("user-owner"), "org_1", parent.ID, AddMemberInput{UserID: "user-member", Role: domain.RoleMember}); err != nil {
		t.Fatal(err)
	}

	asOwner, err := service.SearchTeams(ctx, creds("user-owner"), "org_1", "Eng", 10)
	if err != nil || len(asOwner) != 1 || asOwner[0].ID != parent.ID {
		t.Fatalf("owner search = %+v, err = %v", asOwner, err)
	}
	asMember, err := service.SearchTeams(ctx, creds("user-member"), "org_1", "Design", 10)
	if err != nil || len(asMember) != 0 {
		t.Fatalf("member search leaked Design: %+v, err = %v", asMember, err)
	}

	tree, err := service.TeamDescendants(ctx, creds("user-owner"), "org_1", parent.ID)
	if err != nil || len(tree) != 2 {
		t.Fatalf("owner descendants = %+v, err = %v", tree, err)
	}
	visible, err := service.TeamDescendants(ctx, creds("user-member"), "org_1", parent.ID)
	if err != nil || len(visible) != 1 || visible[0].ID != parent.ID {
		t.Fatalf("member descendants = %+v, err = %v", visible, err)
	}
	if _, err := service.TeamDescendants(ctx, creds("user-member"), "org_1", child.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("hidden descendants error = %v, want not found", err)
	}

	people, err := service.MemberDescendants(ctx, creds("user-owner"), "org_1", parent.ID)
	if err != nil || len(people) != 3 {
		t.Fatalf("owner member descendants = %+v, err = %v", people, err)
	}
	if !hasMembership(people, parent.ID, "user-member") || !hasMembership(people, child.ID, "user-owner") {
		t.Fatalf("owner member descendants missing nested people: %+v", people)
	}
	visiblePeople, err := service.MemberDescendants(ctx, creds("user-member"), "org_1", parent.ID)
	if err != nil || len(visiblePeople) != 2 || hasMembership(visiblePeople, child.ID, "user-owner") {
		t.Fatalf("member descendants leaked the child: %+v, err = %v", visiblePeople, err)
	}
	if _, err := service.MemberDescendants(ctx, creds("user-member"), "org_1", child.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("hidden member descendants error = %v, want not found", err)
	}
}

func TestEmbeddingNearestAndSubtreeDelete(t *testing.T) {
	service, store, _ := newService()
	ctx := context.Background()
	parent, err := service.CreateTeam(ctx, creds("user-owner"), "org_1", CreateTeamInput{Name: "Engineering"})
	if err != nil {
		t.Fatal(err)
	}
	child, err := service.CreateTeam(ctx, creds("user-owner"), "org_1", CreateTeamInput{Name: "Frontend", ParentID: parent.ID})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.AddMember(ctx, creds("user-owner"), "org_1", parent.ID, AddMemberInput{UserID: "user-member", Role: domain.RoleMember}); err != nil {
		t.Fatal(err)
	}

	values := make([]float32, domain.EmbeddingDimensions)
	values[0] = 1
	if err := service.SetTeamEmbedding(ctx, creds("user-member"), "org_1", parent.ID, values); !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("member embedding error = %v, want forbidden", err)
	}
	if err := service.SetTeamEmbedding(ctx, creds("user-owner"), "org_1", parent.ID, values); err != nil {
		t.Fatal(err)
	}
	nearest, err := service.TeamsNearest(ctx, creds("user-owner"), "org_1", values, 5)
	if err != nil || len(nearest) != 1 || nearest[0].ID != parent.ID {
		t.Fatalf("nearest = %+v, err = %v", nearest, err)
	}

	if err := service.DeleteTeam(ctx, creds("user-owner"), "org_1", parent.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Team(ctx, "org_1", child.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("child survived parent delete: %v", err)
	}
}

func TestSearchMembersReturnsTeamsAndHidesTheRest(t *testing.T) {
	service, _, _ := newService()
	ctx := context.Background()
	engineering, err := service.CreateTeam(ctx, creds("user-owner"), "org_1", CreateTeamInput{Name: "Engineering"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.CreateTeam(ctx, creds("user-admin"), "org_1", CreateTeamInput{Name: "Design"}); err != nil {
		t.Fatal(err)
	}
	if _, err := service.AddMember(ctx, creds("user-owner"), "org_1", engineering.ID, AddMemberInput{UserID: "user-member", Role: domain.RoleMember}); err != nil {
		t.Fatal(err)
	}

	asOwner, err := service.SearchMembers(ctx, creds("user-owner"), "org_1", "member", 10)
	if err != nil || len(asOwner) != 1 || asOwner[0].UserID != "user-member" || asOwner[0].Path != domain.UserPath("org_1", "user-member") ||
		len(asOwner[0].Teams) != 1 || asOwner[0].Teams[0].ID != engineering.ID ||
		asOwner[0].Teams[0].Path != engineering.Path || asOwner[0].Teams[0].MemberPath != domain.MemberPath(engineering.Path, "user-member") {
		t.Fatalf("owner member search = %+v, err = %v", asOwner, err)
	}
	outsider, err := service.SearchMembers(ctx, creds("user-owner"), "org_1", "outsider", 10)
	if err != nil || len(outsider) != 1 || outsider[0].UserID != "user-outsider" || outsider[0].Path != domain.UserPath("org_1", "user-outsider") || len(outsider[0].Teams) != 0 {
		t.Fatalf("unplaced member = %+v, err = %v", outsider, err)
	}
	asMember, err := service.SearchMembers(ctx, creds("user-member"), "org_1", "admin", 10)
	if err != nil || len(asMember) != 1 || asMember[0].UserID != "user-admin" || len(asMember[0].Teams) != 0 {
		t.Fatalf("member should not see Design on admin: %+v, err = %v", asMember, err)
	}
	if _, err := service.SearchMembers(ctx, creds("user-owner"), "org_1", "", 10); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("empty member search error = %v, want invalid", err)
	}
}

func TestUnknownOrganizationIsNotFound(t *testing.T) {
	service, _, _ := newService()
	if _, err := service.Teams(context.Background(), creds("user-owner"), "org_missing"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("Teams() error = %v, want not found", err)
	}
}

func newService() (*Service, *fakeStore, *fakeDirectory) {
	store := newFakeStore()
	directory := &fakeDirectory{
		callers: map[string]domain.OrgMembership{
			"Bearer user-owner":    {OrganizationID: "org_1", OrganizationName: "Acme", UserID: "user-owner", Email: "owner@example.com", Role: domain.OrgRoleOwner},
			"Bearer user-admin":    {OrganizationID: "org_1", OrganizationName: "Acme", UserID: "user-admin", Email: "admin@example.com", Role: domain.OrgRoleAdmin},
			"Bearer user-member":   {OrganizationID: "org_1", OrganizationName: "Acme", UserID: "user-member", Email: "member@example.com", Role: domain.OrgRoleMember},
			"Bearer user-outsider": {OrganizationID: "org_1", OrganizationName: "Acme", UserID: "user-outsider", Email: "out@example.com", Role: domain.OrgRoleMember},
		},
		members: []domain.OrgMembership{
			{OrganizationID: "org_1", UserID: "user-owner", Email: "owner@example.com", Role: domain.OrgRoleOwner},
			{OrganizationID: "org_1", UserID: "user-admin", Email: "admin@example.com", Role: domain.OrgRoleAdmin},
			{OrganizationID: "org_1", UserID: "user-member", Email: "member@example.com", Role: domain.OrgRoleMember},
			{OrganizationID: "org_1", UserID: "user-outsider", Email: "out@example.com", Role: domain.OrgRoleMember},
		},
	}
	service := New(store, directory, slog.New(slog.DiscardHandler))
	service.now = func() time.Time { return time.Date(2026, 9, 13, 12, 0, 0, 0, time.UTC) }
	return service, store, directory
}

func hasMembership(members []domain.Membership, teamID, userID string) bool {
	for _, member := range members {
		if member.TeamID == teamID && member.UserID == userID {
			return true
		}
	}
	return false
}

func containsReason(events []domain.Event, reason string) bool {
	for _, event := range events {
		if event.DetailType == teamseventsv1.MemberRemovedDetailType && strings.Contains(string(event.Data), `"`+reason+`"`) {
			return true
		}
	}
	return false
}
