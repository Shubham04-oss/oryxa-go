package domain

import "testing"

func TestControlMatrix(t *testing.T) {
	tests := []struct {
		name   string
		org    OrgRole
		agent  bool
		team   Role
		onTeam bool
		create bool
		see    bool
		manage bool
		leave  bool
	}{
		{name: "org owner, not on team", org: OrgRoleOwner, onTeam: false, create: true, see: true, manage: true, leave: false},
		{name: "org admin, not on team", org: OrgRoleAdmin, onTeam: false, create: true, see: true, manage: true, leave: false},
		{name: "org member, not on team", org: OrgRoleMember, onTeam: false, create: false, see: false, manage: false, leave: false},
		{name: "org member, team member", org: OrgRoleMember, team: RoleMember, onTeam: true, create: false, see: true, manage: false, leave: true},
		{name: "org member, team admin", org: OrgRoleMember, team: RoleAdmin, onTeam: true, create: false, see: true, manage: true, leave: true},
		{name: "org admin, team member", org: OrgRoleAdmin, team: RoleMember, onTeam: true, create: true, see: true, manage: true, leave: true},
		{name: "org owner, team admin", org: OrgRoleOwner, team: RoleAdmin, onTeam: true, create: true, see: true, manage: true, leave: true},
		// An agent acting for an org admin gets a plain member's reach: it
		// creates nothing, sees and manages nothing off a team its member
		// isn't on, and needs its member to be the team's own admin to
		// manage it — exactly as if the org role were "member".
		{name: "admin's agent, not on team", org: OrgRoleOwner, agent: true, onTeam: false, create: false, see: false, manage: false, leave: false},
		{name: "admin's agent, team member", org: OrgRoleAdmin, agent: true, team: RoleMember, onTeam: true, create: false, see: true, manage: false, leave: true},
		{name: "admin's agent, team admin", org: OrgRoleOwner, agent: true, team: RoleAdmin, onTeam: true, create: false, see: true, manage: true, leave: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			org := OrgMembership{Role: test.org, Agent: test.agent}
			if got := CanCreateTeam(org); got != test.create {
				t.Fatalf("CanCreateTeam() = %v, want %v", got, test.create)
			}
			if got := CanSeeTeam(org, test.onTeam); got != test.see {
				t.Fatalf("CanSeeTeam() = %v, want %v", got, test.see)
			}
			if got := CanManageTeam(org, test.team); got != test.manage {
				t.Fatalf("CanManageTeam() = %v, want %v", got, test.manage)
			}
			if got := CanLeaveTeam(test.onTeam); got != test.leave {
				t.Fatalf("CanLeaveTeam() = %v, want %v", got, test.leave)
			}
		})
	}
}

func TestOrgAdminAndTeamAdminAreTheSameControl(t *testing.T) {
	if !CanManageTeam(OrgMembership{Role: OrgRoleAdmin}, "") || !CanManageTeam(OrgMembership{Role: OrgRoleMember}, RoleAdmin) {
		t.Fatal("organization admin and team admin must both manage a team")
	}
	if CanManageTeam(OrgMembership{Role: OrgRoleMember}, RoleMember) {
		t.Fatal("a team member is not a team admin")
	}
}

// TestAgentNeverGetsMoreThanATeamAdminCan: whatever CanManageTeam decides for
// an agent acting for an org admin, a plain member with the same team role
// decides the same way — an agent's org role must never widen its reach.
func TestAgentNeverGetsMoreThanATeamAdminCan(t *testing.T) {
	for _, orgRole := range []OrgRole{OrgRoleOwner, OrgRoleAdmin, OrgRoleMember} {
		for _, teamRole := range []Role{RoleAdmin, RoleMember, ""} {
			agent := OrgMembership{Role: orgRole, Agent: true}
			plainMember := OrgMembership{Role: OrgRoleMember}
			if CanManageTeam(agent, teamRole) != CanManageTeam(plainMember, teamRole) {
				t.Fatalf("org role %q agent, team role %q: CanManageTeam(agent) = %v, want it to match a plain member",
					orgRole, teamRole, CanManageTeam(agent, teamRole))
			}
		}
	}
}
