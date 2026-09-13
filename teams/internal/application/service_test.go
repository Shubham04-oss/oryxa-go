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
	if err != nil || membership.Role != domain.RoleAdmin {
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

func containsReason(events []domain.Event, reason string) bool {
	for _, event := range events {
		if event.DetailType == teamseventsv1.MemberRemovedDetailType && strings.Contains(string(event.Data), `"`+reason+`"`) {
			return true
		}
	}
	return false
}
