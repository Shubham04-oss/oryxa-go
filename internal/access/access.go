// Package access is the one place Oryxa's services decide what a caller may
// reach (docs/design/permissions, §6 step 1). Each service still asks auth who
// the caller is and keeps its own membership type, but the rules themselves
// live here, so they can't drift apart in six copies:
//
//   - an organization's owners and admins manage it, and only from their own
//     session: an agent acting for an admin gets a plain member's reach;
//   - a specialist agent is narrowed by its grant, where a missing list or
//     "*" means everything and an empty list means nothing;
//   - on a team, an agent counts only its member's own place.
//
// Later steps put a relationship engine (SpiceDB) behind these same calls.
package access

import "slices"

// Role is a member's role in an organization, as auth answers it.
type Role string

const (
	Owner  Role = "owner"
	Admin  Role = "admin"
	Member Role = "member"
)

// Valid reports whether the role is one auth gives.
func (role Role) Valid() bool { return role == Owner || role == Admin || role == Member }

// IsOrgAdmin is the role's organization-level control. Who may use it is
// Actor.ActsAsAdmin's to say.
func (role Role) IsOrgAdmin() bool { return role == Owner || role == Admin }

// The roles an agent's token names. A token that names none is the primary's.
const (
	AgentPrimary      = "primary"
	AgentSpecialist   = "specialist"
	AgentOrganization = "organization"
)

// All, in a grant's list, is everything of that kind.
const All = "*"

// Actor is who is calling, as far as the rules here need: the member's role,
// and whether an agent acts for them and as which kind.
type Actor struct {
	Role      Role
	Agent     bool
	AgentRole string
}

// ActsAsAdmin reports whether the caller has an org admin's reach: every
// member's things, every team whether or not they are on it, and what only
// owners and admins may do. Only a person's own session does.
func (actor Actor) ActsAsAdmin() bool { return actor.Role.IsOrgAdmin() && !actor.Agent }

// Specialist reports whether the caller is a specialist agent, the one kind a
// grant narrows.
func (actor Actor) Specialist() bool { return actor.Agent && actor.AgentRole == AgentSpecialist }

// Limited reports whether a specialist's grant list names some things rather
// than all: nil (a grant without the field) and All mean all.
func (actor Actor) Limited(grant []string) bool {
	return actor.Specialist() && grant != nil && !slices.Contains(grant, All)
}

// Reaches reports whether the grant list lets the caller reach id. Every
// caller but a limited specialist reaches all its member can.
func (actor Actor) Reaches(grant []string, id string) bool {
	return !actor.Limited(grant) || (id != "" && slices.Contains(grant, id))
}

// TeamReach narrows what teams answered about one team to what the caller
// may use. Teams answers an agent's token as its member, so for an agent
// acting for an admin it shows every team and offers to manage each; an agent
// gets a plain member's reach instead: a team its member is on, managed only
// where its member is a team admin.
func (actor Actor) TeamReach(canSee, member, teamAdmin, canManage bool) (visible, manage bool) {
	if !canSee {
		return false, false
	}
	if actor.Agent {
		return member, member && teamAdmin
	}
	return true, canManage
}
