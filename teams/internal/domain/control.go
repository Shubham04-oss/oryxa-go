package domain

// CanCreateTeam is organization-level only: a team does not exist yet, so
// there is no team admin to share the control with.
func CanCreateTeam(org OrgRole) bool {
	return org.IsOrgAdmin()
}

// CanSeeTeam is true for organization admins and for members of the team.
// Other organization members do not learn that the team exists.
func CanSeeTeam(org OrgRole, onTeam bool) bool {
	return org.IsOrgAdmin() || onTeam
}

// CanManageTeam is the shared control: an organization admin (owner or
// admin) and a team admin may do the same things to the team and its
// members — update, delete, add, change roles, and remove others.
func CanManageTeam(org OrgRole, team Role) bool {
	return org.IsOrgAdmin() || team.IsTeamAdmin()
}

// CanLeaveTeam is true when the caller is on the team. Leaving is not a
// manage action; the last team admin still cannot leave.
func CanLeaveTeam(onTeam bool) bool {
	return onTeam
}
