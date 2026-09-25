package domain

import "testing"

// TestOnlyAPersonsOwnSessionActsAsAdmin: an admin's own session gets an org
// admin's reach; an agent acting for that admin does not, since what it
// sees or manages can end up in front of a colleague it is answering rather
// than the admin itself.
func TestOnlyAPersonsOwnSessionActsAsAdmin(t *testing.T) {
	callers := map[string]struct {
		membership  OrgMembership
		actsAsAdmin bool
	}{
		"owner":                 {OrgMembership{Role: OrgRoleOwner}, true},
		"admin":                 {OrgMembership{Role: OrgRoleAdmin}, true},
		"member":                {OrgMembership{Role: OrgRoleMember}, false},
		"agent for an owner":    {OrgMembership{Role: OrgRoleOwner, Agent: true}, false},
		"agent for an admin":    {OrgMembership{Role: OrgRoleAdmin, Agent: true}, false},
		"agent for a member":    {OrgMembership{Role: OrgRoleMember, Agent: true}, false},
		"unrecognised role":     {OrgMembership{Role: OrgRole("pending")}, false},
		"agent with no role":    {OrgMembership{Agent: true}, false},
		"nothing known of them": {OrgMembership{}, false},
	}
	for name, caller := range callers {
		if got := caller.membership.ActsAsAdmin(); got != caller.actsAsAdmin {
			t.Errorf("%s: ActsAsAdmin() = %v, want %v", name, got, caller.actsAsAdmin)
		}
	}
}
