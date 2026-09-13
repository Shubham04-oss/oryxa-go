package domain

import "testing"

func TestControlMatrix(t *testing.T) {
	tests := []struct {
		name   string
		org    OrgRole
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
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := CanCreateTeam(test.org); got != test.create {
				t.Fatalf("CanCreateTeam() = %v, want %v", got, test.create)
			}
			if got := CanSeeTeam(test.org, test.onTeam); got != test.see {
				t.Fatalf("CanSeeTeam() = %v, want %v", got, test.see)
			}
			if got := CanManageTeam(test.org, test.team); got != test.manage {
				t.Fatalf("CanManageTeam() = %v, want %v", got, test.manage)
			}
			if got := CanLeaveTeam(test.onTeam); got != test.leave {
				t.Fatalf("CanLeaveTeam() = %v, want %v", got, test.leave)
			}
		})
	}
}

func TestOrgAdminAndTeamAdminAreTheSameControl(t *testing.T) {
	if !CanManageTeam(OrgRoleAdmin, "") || !CanManageTeam(OrgRoleMember, RoleAdmin) {
		t.Fatal("organization admin and team admin must both manage a team")
	}
	if CanManageTeam(OrgRoleMember, RoleMember) {
		t.Fatal("a team member is not a team admin")
	}
}
