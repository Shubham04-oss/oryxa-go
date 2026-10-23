package domain

import (
	"encoding/json"
	"errors"
	"fmt"
	"github.com/Shubham04-oss/oryxa-go/internal/access"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

var (
	ErrInvalid      = errors.New("invalid input")
	ErrNotFound     = errors.New("not found")
	ErrConflict     = errors.New("conflict")
	ErrForbidden    = errors.New("forbidden")
	ErrUnauthorized = errors.New("unauthorized")
	// ErrTooManyRequests means the caller has asked for more than their share
	// of something that fans out, and may ask again once the window passes.
	ErrTooManyRequests = errors.New("too many requests")
)

// OrgRole is the caller's place in the organization, as auth reports it.
// OrgRole is access.Role: the rules for it live there.
type OrgRole = access.Role

const (
	OrgRoleOwner  OrgRole = "owner"
	OrgRoleAdmin  OrgRole = "admin"
	OrgRoleMember OrgRole = "member"
)

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
	// Agent says the caller is an agent acting for this member rather than
	// the member themselves, which auth says of every token it mints for an
	// agent. An agent may run with its member absent, answering someone
	// else: a teammate's interaction request, a room it was mentioned in.
	// What it reads or does can end up in front of any of them, so it gets a
	// plain member's reach whatever its member's role; see ActsAsAdmin.
	Agent bool
}

// ActsAsAdmin reports whether the caller acts with an org admin's reach:
// every team in the organization, whether or not its member is on it, and
// the admin-only writes CanCreateTeam and CanManageTeam gate. Only a
// person's own session does. An agent acting for an admin gets what a plain
// member would: the teams its member is actually on, and management only
// where its member is a team admin in its own right.
func (membership OrgMembership) ActsAsAdmin() bool {
	return access.Actor{Role: membership.Role, Agent: membership.Agent}.ActsAsAdmin()
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

// MaxTeamNameLength bounds a team's name, in characters.
const MaxTeamNameLength = 120

// CleanTeamName trims a name a member chose and refuses one that is empty,
// too long, or holds control characters. A name travels far — into email
// subjects and bodies, event envelopes, and other members' listings — and a
// newline or an escape in it is never a name anyone meant: it is an attempt to
// make one of those places read something its sender did not write.
func CleanTeamName(raw string) (string, error) {
	name := strings.TrimSpace(raw)
	if name == "" || utf8.RuneCountInString(name) > MaxTeamNameLength {
		return "", fmt.Errorf("team name must be between 1 and %d characters: %w", MaxTeamNameLength, ErrInvalid)
	}
	for _, r := range name {
		// Control characters, including the CR and LF of header injection and
		// the bidi marks that reorder text, have no place in a name.
		if r < 0x20 || r == 0x7f || unicode.Is(unicode.Cf, r) {
			return "", fmt.Errorf("team name may not hold control characters: %w", ErrInvalid)
		}
	}
	return name, nil
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
	// TraceContext is the outbox row's stored trace, carried through from
	// outbox.Event so Publisher can link its producer span to the write that
	// created this event. Code building an Event to record leaves it unset:
	// the write path captures the current trace itself (see insertEvents),
	// rather than trusting a caller to have filled this in.
	TraceContext string
}
