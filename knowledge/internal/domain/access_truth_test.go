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

func TestRoleAndGrantRulesMatchTheirTruthTable(t *testing.T) {
	for _, role := range truthRoles {
		for _, agent := range []bool{false, true} {
			for _, agentRole := range truthAgentRoles {
				for _, grant := range truthGrants {
					m := OrgMembership{Role: role, Agent: agent, AgentRole: agentRole, AgentKnowledge: grant}
					limited := wasLimited(agent, agentRole, grant)
					// ActsAsAdmin is new here: knowledge used the bare role, which let
					// an agent acting for an admin reach as one (gap G2).
					if m.ActsAsAdmin() != wasAdmin(role, agent) || m.KnowledgeLimited() != limited {
						t.Errorf("%+v: ActsAsAdmin %v KnowledgeLimited %v", m, m.ActsAsAdmin(), m.KnowledgeLimited())
					}
					for _, id := range []string{"x", "z"} {
						refused := limited && !slices.Contains(grant, id)
						if (m.MayReadSource(id) != nil) != refused {
							t.Errorf("%+v: MayReadSource(%q) = %v", m, id, m.MayReadSource(id))
						}
					}
				}
			}
		}
	}
}
