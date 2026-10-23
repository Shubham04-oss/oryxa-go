package domain

import (
	"slices"
	"testing"
)

var (
	truthRoles      = []OrgRole{OrgRoleOwner, OrgRoleAdmin, OrgRoleMember, ""}
	truthAgentRoles = []string{"", "primary", "specialist", "organization"}
	truthGrants     = [][]string{nil, {}, {"*"}, {"x"}, {"y", "x"}}
)

// wasAdmin and wasLimited are the rules as this service wrote them before
// internal/access held them; the truth table pins that nothing changed.
func wasAdmin(role OrgRole, agent bool) bool {
	return (role == "owner" || role == "admin") && !agent
}

func wasLimited(agent bool, agentRole string, grant []string) bool {
	return agent && agentRole == "specialist" && grant != nil && !slices.Contains(grant, "*")
}

func TestRoleRulesMatchTheirTruthTable(t *testing.T) {
	for _, role := range truthRoles {
		for _, agent := range []bool{false, true} {
			m := OrgMembership{Role: role, Agent: agent}
			if m.ActsAsAdmin() != wasAdmin(role, agent) {
				t.Errorf("role %q agent %v: ActsAsAdmin = %v", role, agent, m.ActsAsAdmin())
			}
		}
		if role.IsOrgAdmin() != wasAdmin(role, false) || role.Valid() != (role != "") {
			t.Errorf("role %q: IsOrgAdmin %v Valid %v", role, role.IsOrgAdmin(), role.Valid())
		}
	}
}
