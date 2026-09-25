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

// TestAgentActingForAnOrgAdminSeesOnlyItsMembersTeams: an org admin's own
// session sees and can manage every team. A token minted for an agent acting
// for that same admin must not: it gets a plain member's reach, because what
// it reads can end up in front of someone the admin never showed it to. It
// sees a team only when its member actually belongs to it, and can_manage is
// true only where its member is that team's own admin — never merely because
// teams would let the admin's own session manage any team.
func TestAgentActingForAnOrgAdminSeesOnlyItsMembersTeams(t *testing.T) {
	service, _, _ := newService()
	ctx := context.Background()
	// Alpha: user-admin created it, so is its own team admin.
	alpha, err := service.CreateTeam(ctx, creds("user-admin"), "org_1", CreateTeamInput{Name: "Alpha"})
	if err != nil {
		t.Fatal(err)
	}
	// Beta: user-owner's team; user-admin is added as a plain member.
	beta, err := service.CreateTeam(ctx, creds("user-owner"), "org_1", CreateTeamInput{Name: "Beta"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.AddMember(ctx, creds("user-owner"), "org_1", beta.ID, AddMemberInput{UserID: "user-admin", Role: domain.RoleMember}); err != nil {
		t.Fatal(err)
	}
	// Gamma: user-owner's team; user-admin is never placed on it.
	gamma, err := service.CreateTeam(ctx, creds("user-owner"), "org_1", CreateTeamInput{Name: "Gamma"})
	if err != nil {
		t.Fatal(err)
	}

	// The admin's own session still sees and manages all three.
	live, err := service.Teams(ctx, creds("user-admin"), "org_1")
	if err != nil || len(live) != 3 {
		t.Fatalf("admin's own session teams = %+v, err = %v", live, err)
	}
	if _, err := service.Team(ctx, creds("user-admin"), "org_1", gamma.ID); err != nil {
		t.Fatalf("admin's own session should reach Gamma: %v", err)
	}

	// The agent acting for the same admin sees only Alpha and Beta.
	asAgent, err := service.Teams(ctx, agentCreds("user-admin"), "org_1")
	if err != nil || len(asAgent) != 2 {
		t.Fatalf("admin's agent teams = %+v, err = %v", asAgent, err)
	}
	for _, team := range asAgent {
		switch team.ID {
		case alpha.ID:
			if !team.CanManage || team.Role != domain.RoleAdmin {
				t.Fatalf("agent on Alpha (its member's own team) = %+v, want can_manage and team admin", team)
			}
		case beta.ID:
			if team.CanManage || team.Role != domain.RoleMember {
				t.Fatalf("agent on Beta (its member is a plain team member) = %+v, want no can_manage", team)
			}
		default:
			t.Fatalf("agent should not see team %+v", team)
		}
	}

	// Reading Gamma directly, and reading Beta's can_manage, must agree with
	// the list above: an admin's agent can't list OR read a team its member
	// isn't on, and can_manage on one it can read is true only as team admin.
	if _, err := service.Team(ctx, agentCreds("user-admin"), "org_1", gamma.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("admin's agent read of Gamma error = %v, want not found", err)
	}
	betaDetail, err := service.Team(ctx, agentCreds("user-admin"), "org_1", beta.ID)
	if err != nil || betaDetail.CanManage {
		t.Fatalf("admin's agent read of Beta = %+v, err = %v, want can_manage false", betaDetail, err)
	}
	alphaDetail, err := service.Team(ctx, agentCreds("user-admin"), "org_1", alpha.ID)
	if err != nil || !alphaDetail.CanManage {
		t.Fatalf("admin's agent read of Alpha = %+v, err = %v, want can_manage true", alphaDetail, err)
	}
}

// TestAgentActingForAnOrgAdminCannotWriteBeyondItsTeamAdminReach: the writes
// teams gates on CanManageTeam or CanCreateTeam must refuse an admin's agent
// everywhere its member is not itself a team admin, even though the same
// writes succeed both for the admin's own session and for the agent where
// its member genuinely is that team's admin.
func TestAgentActingForAnOrgAdminCannotWriteBeyondItsTeamAdminReach(t *testing.T) {
	service, _, _ := newService()
	ctx := context.Background()
	alpha, err := service.CreateTeam(ctx, creds("user-admin"), "org_1", CreateTeamInput{Name: "Alpha"})
	if err != nil {
		t.Fatal(err)
	}
	beta, err := service.CreateTeam(ctx, creds("user-owner"), "org_1", CreateTeamInput{Name: "Beta"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.AddMember(ctx, creds("user-owner"), "org_1", beta.ID, AddMemberInput{UserID: "user-admin", Role: domain.RoleMember}); err != nil {
		t.Fatal(err)
	}

	// Creating a top-level team is admin-only, and the agent is not one.
	if _, err := service.CreateTeam(ctx, agentCreds("user-admin"), "org_1", CreateTeamInput{Name: "Delta"}); !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("admin's agent CreateTeam() error = %v, want forbidden", err)
	}
	if _, err := service.CreateTeam(ctx, creds("user-admin"), "org_1", CreateTeamInput{Name: "Delta"}); err != nil {
		t.Fatalf("admin's own session CreateTeam() error = %v, want success", err)
	}

	// Beta: the admin's own session may rename, add members to, and delete
	// it purely on org-wide reach; the agent may not, since its member is
	// only a plain member there.
	if _, err := service.UpdateTeam(ctx, agentCreds("user-admin"), "org_1", beta.ID, UpdateTeamInput{Name: "Nope"}); !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("admin's agent UpdateTeam(Beta) error = %v, want forbidden", err)
	}
	if _, err := service.AddMember(ctx, agentCreds("user-admin"), "org_1", beta.ID, AddMemberInput{UserID: "user-outsider", Role: domain.RoleMember}); !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("admin's agent AddMember(Beta) error = %v, want forbidden", err)
	}
	if err := service.DeleteTeam(ctx, agentCreds("user-admin"), "org_1", beta.ID); !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("admin's agent DeleteTeam(Beta) error = %v, want forbidden", err)
	}
	if _, err := service.UpdateTeam(ctx, creds("user-admin"), "org_1", beta.ID, UpdateTeamInput{Name: "Renamed"}); err != nil {
		t.Fatalf("admin's own session UpdateTeam(Beta) error = %v, want success", err)
	}

	// Alpha: the agent's member is genuinely Alpha's own team admin, so the
	// same writes succeed there — an agent still keeps its member's real
	// team-admin standing, only not the org-wide reach.
	if _, err := service.UpdateTeam(ctx, agentCreds("user-admin"), "org_1", alpha.ID, UpdateTeamInput{Name: "Alpha2"}); err != nil {
		t.Fatalf("admin's agent UpdateTeam(Alpha) error = %v, want success", err)
	}
	if _, err := service.AddMember(ctx, agentCreds("user-admin"), "org_1", alpha.ID, AddMemberInput{UserID: "user-outsider", Role: domain.RoleMember}); err != nil {
		t.Fatalf("admin's agent AddMember(Alpha) error = %v, want success", err)
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

func TestSearchRefusesAQueryTooLongToBeALookup(t *testing.T) {
	service, _, _ := newService()
	ctx := context.Background()
	long := strings.Repeat("plat ", domain.MaxSearchQueryLength)
	if _, err := service.SearchTeams(ctx, creds("user-owner"), "org_1", long, 10); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("team search error = %v, want invalid", err)
	}
	if _, err := service.SearchMembers(ctx, creds("user-owner"), "org_1", long, 10); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("member search error = %v, want invalid", err)
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

func TestOrganizationDirectoryReturnsTheRosterAndOnlyVisibleTeamLinks(t *testing.T) {
	service, _, _ := newService()
	ctx := context.Background()
	engineering, err := service.CreateTeam(ctx, creds("user-owner"), "org_1", CreateTeamInput{Name: "Engineering"})
	if err != nil {
		t.Fatal(err)
	}
	design, err := service.CreateTeam(ctx, creds("user-admin"), "org_1", CreateTeamInput{Name: "Design"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.AddMember(ctx, creds("user-owner"), "org_1", engineering.ID, AddMemberInput{UserID: "user-member", Role: domain.RoleMember}); err != nil {
		t.Fatal(err)
	}

	directory, err := service.OrganizationDirectory(ctx, creds("user-member"), "org_1")
	if err != nil {
		t.Fatal(err)
	}
	if directory.Organization.ID != "org_1" || directory.Organization.Name != "Acme" || len(directory.Members) != 4 || len(directory.Teams) != 1 || directory.Teams[0].ID != engineering.ID {
		t.Fatalf("member directory = %+v", directory)
	}
	for _, person := range directory.Members {
		for _, team := range person.Teams {
			if team.ID == design.ID {
				t.Fatalf("hidden Design leaked through %+v", person)
			}
		}
	}
	ownerDirectory, err := service.OrganizationDirectory(ctx, creds("user-owner"), "org_1")
	if err != nil || len(ownerDirectory.Teams) != 2 {
		t.Fatalf("owner directory = %+v, %v", ownerDirectory, err)
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
			// Agent tokens: same identity and org role as the live session
			// above, plus Agent — auth mints these for an agent acting for
			// the member rather than the member themselves.
			"Bearer agent-for-user-owner":  {OrganizationID: "org_1", OrganizationName: "Acme", UserID: "user-owner", Email: "owner@example.com", Role: domain.OrgRoleOwner, Agent: true},
			"Bearer agent-for-user-admin":  {OrganizationID: "org_1", OrganizationName: "Acme", UserID: "user-admin", Email: "admin@example.com", Role: domain.OrgRoleAdmin, Agent: true},
			"Bearer agent-for-user-member": {OrganizationID: "org_1", OrganizationName: "Acme", UserID: "user-member", Email: "member@example.com", Role: domain.OrgRoleMember, Agent: true},
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
