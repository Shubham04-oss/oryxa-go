package domain

import (
	"encoding/json"
	"errors"
	"time"
)

var (
	ErrInvalid      = errors.New("invalid input")
	ErrNotFound     = errors.New("not found")
	ErrConflict     = errors.New("conflict")
	ErrForbidden    = errors.New("forbidden")
	ErrUnauthorized = errors.New("unauthorized")
)

// OrgRole is the caller's place in the organization, as auth reports it.
type OrgRole string

const (
	OrgRoleOwner  OrgRole = "owner"
	OrgRoleAdmin  OrgRole = "admin"
	OrgRoleMember OrgRole = "member"
)

func (role OrgRole) Valid() bool {
	return role == OrgRoleOwner || role == OrgRoleAdmin || role == OrgRoleMember
}

// IsOrgAdmin is the organization-level control: owners and admins may manage
// every team in the organization.
func (role OrgRole) IsOrgAdmin() bool {
	return role == OrgRoleOwner || role == OrgRoleAdmin
}

// Role is a member's place on a team.
type Role string

const (
	RoleAdmin  Role = "admin"
	RoleMember Role = "member"
)

func (role Role) Valid() bool {
	return role == RoleAdmin || role == RoleMember
}

// IsTeamAdmin is the team-level control. It is the same power as an
// organization admin, on this team only.
func (role Role) IsTeamAdmin() bool {
	return role == RoleAdmin
}

type OrgMembership struct {
	OrganizationID   string
	OrganizationName string
	UserID           string
	Email            string
	DisplayName      string
	Role             OrgRole
}

type Organization struct {
	ID      string `db:"organization_id"`
	Name    string `db:"name"`
	Deleted bool   `db:"deleted"`
}

type Team struct {
	ID             string    `json:"id" db:"id"`
	OrganizationID string    `json:"organization_id" db:"organization_id"`
	Name           string    `json:"name" db:"name"`
	Path           string    `json:"path,omitempty" db:"path"`
	CreatedBy      string    `json:"created_by" db:"created_by"`
	CreatedAt      time.Time `json:"created_at" db:"created_at"`
	UpdatedAt      time.Time `json:"updated_at" db:"updated_at"`
}

type Membership struct {
	TeamID         string    `json:"team_id" db:"team_id"`
	OrganizationID string    `json:"organization_id" db:"organization_id"`
	UserID         string    `json:"user_id" db:"user_id"`
	Email          string    `json:"email" db:"email"`
	DisplayName    string    `json:"display_name,omitempty" db:"display_name"`
	Role           Role      `json:"role" db:"role"`
	NotifyEmail    bool      `json:"notify_email" db:"notify_email"`
	Path           string    `json:"path,omitempty" db:"path"`
	JoinedAt       time.Time `json:"joined_at" db:"joined_at"`
}

// User is a person teams has seen in an organization, hanging under
// the org in ltree. Auth remains the source of org membership.
type User struct {
	OrganizationID string    `json:"organization_id" db:"organization_id"`
	UserID         string    `json:"user_id" db:"user_id"`
	Email          string    `json:"email" db:"email"`
	DisplayName    string    `json:"display_name,omitempty" db:"display_name"`
	Path           string    `json:"path,omitempty" db:"path"`
	UpdatedAt      time.Time `json:"updated_at" db:"updated_at"`
}

// OrganizationMembership is one person's place on one team, with enough
// team fields for a member search to name the team.
type OrganizationMembership struct {
	UserID      string `db:"user_id"`
	Email       string `db:"email"`
	DisplayName string `db:"display_name"`
	Role        Role   `db:"role"`
	TeamID      string `db:"team_id"`
	TeamName    string `db:"team_name"`
	Path        string `db:"path"`
	MemberPath  string `db:"member_path"`
}

// Event is what teams puts on EventBridge, in the envelope every Oryxa
// publisher uses.
type Event struct {
	ID         string
	DetailType string
	OccurredAt time.Time
	Data       json.RawMessage
}

type OutboxEvent struct {
	Event
	Attempts int
}
