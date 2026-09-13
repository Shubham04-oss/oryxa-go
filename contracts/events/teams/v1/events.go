// Package v1 contains the stable version-one event contracts emitted by teams.
package v1

import "time"

const (
	Source = "oryxa.teams"

	TeamCreatedDetailType       = "teams.team.created.v1"
	TeamUpdatedDetailType       = "teams.team.updated.v1"
	TeamDeletedDetailType       = "teams.team.deleted.v1"
	MemberAddedDetailType       = "teams.member.added.v1"
	MemberRoleChangedDetailType = "teams.member.role-changed.v1"
	MemberRemovedDetailType     = "teams.member.removed.v1"
	TeamCalledDetailType        = "teams.team.called.v1"
	TeamMembersCalledDetailType = "teams.team.members-called.v1"
	TeamsListedDetailType       = "teams.teams.listed.v1"
	MemberPingedDetailType      = "teams.member.pinged.v1"
)

// Tools that emit the call and ping events.
const (
	ToolListTeams       = "list_teams"
	ToolCallTeam        = "call_team"
	ToolCallTeamMembers = "call_team_members"
)

// Reasons a team membership ends, as MemberRemoved.Reason.
const (
	MemberRemovedReasonLeft                        = "left"
	MemberRemovedReasonRemoved                     = "removed"
	MemberRemovedReasonTeamDeleted                 = "team_deleted"
	MemberRemovedReasonOrganizationDeleted         = "organization_deleted"
	MemberRemovedReasonOrganizationMembershipEnded = "organization_membership_ended"
)

type TeamCreated struct {
	TeamID           string    `json:"team_id"`
	OrganizationID   string    `json:"organization_id"`
	OrganizationName string    `json:"organization_name"`
	Name             string    `json:"name"`
	CreatedBy        string    `json:"created_by"`
	CreatedAt        time.Time `json:"created_at"`
}

type TeamUpdated struct {
	TeamID           string    `json:"team_id"`
	OrganizationID   string    `json:"organization_id"`
	OrganizationName string    `json:"organization_name"`
	Name             string    `json:"name"`
	UpdatedBy        string    `json:"updated_by"`
	UpdatedAt        time.Time `json:"updated_at"`
}

type TeamDeleted struct {
	TeamID           string    `json:"team_id"`
	OrganizationID   string    `json:"organization_id"`
	OrganizationName string    `json:"organization_name"`
	Name             string    `json:"name"`
	DeletedBy        string    `json:"deleted_by"`
	DeletedAt        time.Time `json:"deleted_at"`
}

type MemberAdded struct {
	TeamID           string    `json:"team_id"`
	TeamName         string    `json:"team_name"`
	OrganizationID   string    `json:"organization_id"`
	OrganizationName string    `json:"organization_name"`
	UserID           string    `json:"user_id"`
	Email            string    `json:"email"`
	Role             string    `json:"role"`
	AddedBy          string    `json:"added_by"`
	AddedAt          time.Time `json:"added_at"`
}

type MemberRoleChanged struct {
	TeamID           string    `json:"team_id"`
	TeamName         string    `json:"team_name"`
	OrganizationID   string    `json:"organization_id"`
	OrganizationName string    `json:"organization_name"`
	UserID           string    `json:"user_id"`
	Email            string    `json:"email"`
	PreviousRole     string    `json:"previous_role"`
	Role             string    `json:"role"`
	ChangedBy        string    `json:"changed_by"`
	ChangedAt        time.Time `json:"changed_at"`
}

// MemberRemoved is emitted for every team membership that ends: a member
// leaving, being removed, the team being deleted, the organization being
// deleted, or the member losing their place in the organization.
type MemberRemoved struct {
	TeamID           string    `json:"team_id"`
	TeamName         string    `json:"team_name"`
	OrganizationID   string    `json:"organization_id"`
	OrganizationName string    `json:"organization_name"`
	UserID           string    `json:"user_id"`
	Email            string    `json:"email"`
	PreviousRole     string    `json:"previous_role"`
	Reason           string    `json:"reason"`
	RemovedBy        string    `json:"removed_by"`
	RemovedAt        time.Time `json:"removed_at"`
}

// TeamCalled is emitted when an AI calls a team or its members. Consumers
// that need to notify people should use MemberPinged — one per member in
// the same transaction — rather than expanding this event themselves.
type TeamCalled struct {
	TeamID           string    `json:"team_id"`
	TeamName         string    `json:"team_name"`
	OrganizationID   string    `json:"organization_id"`
	OrganizationName string    `json:"organization_name"`
	Tool             string    `json:"tool"`
	CalledBy         string    `json:"called_by"`
	CalledAt         time.Time `json:"called_at"`
	MemberCount      int       `json:"member_count"`
}

type ListedTeam struct {
	TeamID string `json:"team_id"`
	Name   string `json:"name"`
}

// TeamsListed is emitted when an AI lists teams. MemberPinged is emitted
// for every member of every listed team in the same transaction.
type TeamsListed struct {
	OrganizationID   string       `json:"organization_id"`
	OrganizationName string       `json:"organization_name"`
	Teams            []ListedTeam `json:"teams"`
	CalledBy         string       `json:"called_by"`
	CalledAt         time.Time    `json:"called_at"`
}

// MemberPinged is the fan-out: this person was reached because a team
// tool ran. Email, agents, and other consumers subscribe to this.
type MemberPinged struct {
	TeamID           string    `json:"team_id"`
	TeamName         string    `json:"team_name"`
	OrganizationID   string    `json:"organization_id"`
	OrganizationName string    `json:"organization_name"`
	UserID           string    `json:"user_id"`
	Email            string    `json:"email"`
	Role             string    `json:"role"`
	Tool             string    `json:"tool"`
	NotifyEmail      bool      `json:"notify_email"`
	CalledBy         string    `json:"called_by"`
	CalledAt         time.Time `json:"called_at"`
}
