package domain

// CanCreateTeam is organization-level only: a team does not exist yet, so
// there is no team admin to share the control with. org is the caller's own
// reach (see OrgMembership.ActsAsAdmin): an agent acting for an org admin
// does not create top-level teams, only the admin's own session does.
func CanCreateTeam(org OrgMembership) bool {
	return org.ActsAsAdmin()
}

// CanSeeTeam is true for organization admins and for members of the team.
// Other organization members do not learn that the team exists. An agent
// acting for an org admin sees a team only when its member actually belongs
// to it, the same as a plain member would.
func CanSeeTeam(org OrgMembership, onTeam bool) bool {
	return org.ActsAsAdmin() || onTeam
}

// CanManageTeam is the shared control: an organization admin (owner or
// admin) and a team admin may do the same things to the team and its
// members — update, delete, add, change roles, and remove others. An agent
// acting for an org admin manages a team only where its member is that
// team's own admin, not every team the way the admin's own session could.
func CanManageTeam(org OrgMembership, team Role) bool {
	return org.ActsAsAdmin() || team.IsTeamAdmin()
}

// CanLeaveTeam is true when the caller is on the team. Leaving is not a
// manage action; the last team admin still cannot leave.
func CanLeaveTeam(onTeam bool) bool {
	return onTeam
}
