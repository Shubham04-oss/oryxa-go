package domain

import "fmt"

// Caller is the member asking. Membership is what auth answered for the
// organization. Team is what teams answered for the connection's team; it
// stays zero when the connection hangs elsewhere or teams concealed the team.
type Caller struct {
	Membership OrgMembership
	Team       TeamAccess
}

func (caller Caller) orgAdmin() bool {
	return caller.Membership.Role.IsOrgAdmin()
}

func (caller Caller) seesTeam(teamID string) bool {
	return teamID != "" && caller.Team.TeamID == teamID && caller.Team.CanSee()
}

// MayManageApps: only organization admins create, change, disable or delete
// apps.
func MayManageApps(caller Caller) error {
	if !caller.orgAdmin() {
		return ErrForbidden
	}
	return nil
}

// MayCreate decides whether the caller may create a connection at a
// placement. A team the caller cannot see is concealed as not found.
func MayCreate(caller Caller, placement Placement) error {
	switch placement.Scope {
	case ScopeMember:
		if placement.MemberID == "" || placement.MemberID != caller.Membership.UserID {
			return ErrForbidden
		}
		return nil
	case ScopeTeam:
		if !caller.seesTeam(placement.TeamID) {
			return ErrNotFound
		}
		if !caller.orgAdmin() && !caller.Team.CanManage {
			return ErrForbidden
		}
		return nil
	case ScopeOrganization:
		if !caller.orgAdmin() {
			return ErrForbidden
		}
		return nil
	default:
		return fmt.Errorf("scope is not allowed: %w", ErrInvalid)
	}
}

// MaySee decides whether the caller may see a connection's metadata.
// Anything the caller cannot see is concealed as not found.
func MaySee(caller Caller, connection Connection) error {
	if connection.OrganizationID == "" || connection.OrganizationID != caller.Membership.OrganizationID {
		return ErrNotFound
	}
	switch connection.Scope {
	case ScopeMember:
		if connection.MemberID == caller.Membership.UserID || caller.orgAdmin() {
			return nil
		}
	case ScopeTeam:
		if caller.seesTeam(connection.TeamID) {
			return nil
		}
	case ScopeOrganization:
		return nil
	}
	return ErrNotFound
}

// MayUse decides whether the caller may have tokens issued on a connection.
// Organization admins see member connections but cannot use them.
func MayUse(caller Caller, connection Connection) error {
	if err := MaySee(caller, connection); err != nil {
		return err
	}
	switch connection.Scope {
	case ScopeMember:
		if connection.MemberID == caller.Membership.UserID {
			return nil
		}
	case ScopeTeam:
		return nil
	case ScopeOrganization:
		if connection.Usage != UsageAdmins || caller.orgAdmin() {
			return nil
		}
	}
	return ErrForbidden
}

// MayManage decides who may reconnect a connection, replace its credential,
// rename it, or make it the default.
func MayManage(caller Caller, connection Connection) error {
	if err := MaySee(caller, connection); err != nil {
		return err
	}
	switch connection.Scope {
	case ScopeMember:
		if connection.MemberID == caller.Membership.UserID {
			return nil
		}
	case ScopeTeam:
		if caller.orgAdmin() || caller.Team.CanManage {
			return nil
		}
	case ScopeOrganization:
		if caller.orgAdmin() {
			return nil
		}
	}
	return ErrForbidden
}

// MayDisconnect is MayManage, except organization admins may also disconnect
// any member's connection.
func MayDisconnect(caller Caller, connection Connection) error {
	if connection.Scope == ScopeMember && caller.orgAdmin() {
		return MaySee(caller, connection)
	}
	return MayManage(caller, connection)
}

// ServiceMayUse decides whether a service running work on its own for a member,
// without forwarding that member's credentials, may have tokens issued on a
// connection. Connections cannot ask auth or teams on that member's behalf, so
// the service vouches for team and organization access: the service that owns
// the work stops it when the member leaves. A member
// connection must still be the acting member's own, and an admins-only
// organization connection, whose admin check needs auth, serves only cleanup.
func ServiceMayUse(connection Connection, actingMemberID string, purpose Purpose) error {
	if actingMemberID == "" {
		return fmt.Errorf("acting_member_id is required: %w", ErrInvalid)
	}
	switch connection.Scope {
	case ScopeMember:
		if connection.MemberID != actingMemberID {
			return ErrForbidden
		}
	case ScopeTeam:
	case ScopeOrganization:
		if connection.Usage == UsageAdmins && purpose != PurposeCleanup {
			return ErrForbidden
		}
	default:
		return ErrNotFound
	}
	return nil
}
