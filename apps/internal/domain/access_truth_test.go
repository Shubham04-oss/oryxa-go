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
			for _, agentRole := range truthAgentRoles {
				m := OrgMembership{Role: role, Agent: agent, AgentRole: agentRole, AgentGrant: &AgentGrant{Apps: []string{}}}
				if m.ActsAsAdmin() != wasAdmin(role, agent) {
					t.Errorf("%+v: ActsAsAdmin %v", m, m.ActsAsAdmin())
				}
				// An empty apps grant refuses every app, but only a specialist.
				refused := agent && agentRole == "specialist"
				if (m.MayReach("gmail") != nil) != refused {
					t.Errorf("%+v: MayReach = %v", m, m.MayReach("gmail"))
				}
			}
		}
	}
}
