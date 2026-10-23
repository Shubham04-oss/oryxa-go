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
					m := OrgMembership{Role: role, Agent: agent, AgentRole: agentRole, AgentFiles: grant}
					limited := wasLimited(agent, agentRole, grant)
					if m.ActsAsAdmin() != wasAdmin(role, agent) || m.FilesLimited() != limited {
						t.Errorf("%+v: ActsAsAdmin %v FilesLimited %v", m, m.ActsAsAdmin(), m.FilesLimited())
					}
					for _, c := range []struct {
						scope Scope
						team  string
						name  string
					}{{ScopeTeam, "x", "x"}, {ScopeTeam, "z", "z"}, {ScopeTeam, "", ""}, {ScopeMember, "", "member"}, {ScopeOrganization, "", "org"}} {
						refused := limited && (c.name == "" || !slices.Contains(grant, c.name))
						if (m.MayReachFiles(c.scope, c.team) != nil) != refused {
							t.Errorf("%+v: MayReachFiles(%q, %q) = %v", m, c.scope, c.team, m.MayReachFiles(c.scope, c.team))
						}
					}
				}
			}
		}
	}
}
