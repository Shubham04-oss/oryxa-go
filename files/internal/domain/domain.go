package domain

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode/utf8"
)

var (
	ErrInvalid      = errors.New("invalid input")
	ErrTooLarge     = errors.New("too large")
	ErrNotFound     = errors.New("not found")
	ErrConflict     = errors.New("conflict")
	ErrForbidden    = errors.New("forbidden")
	ErrUnauthorized = errors.New("unauthorized")
)

const (
	MaxBytes             = 8 << 20
	MinRetentionDays     = 1
	MaxRetentionDays     = 90
	DefaultRetentionDays = 30
)

type OrgRole string

const (
	OrgRoleOwner  OrgRole = "owner"
	OrgRoleAdmin  OrgRole = "admin"
	OrgRoleMember OrgRole = "member"
)

func (role OrgRole) Valid() bool {
	return role == OrgRoleOwner || role == OrgRoleAdmin || role == OrgRoleMember
}

func (role OrgRole) IsOrgAdmin() bool {
	return role == OrgRoleOwner || role == OrgRoleAdmin
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
	// What it reads can end up in front of any of them, so it gets a plain
	// member's reach whatever its member's role; see ActsAsAdmin.
	Agent bool
	// AgentRole is the agent's role in its claims (primary, specialist or
	// organization), when Agent is set.
	AgentRole string
	// AgentFiles is the file scopes a specialist's grant names: FilesMember,
	// FilesOrganization, team ids, or "*" for all. Nil, which a grant without
	// the field gives, is all.
	AgentFiles []string
}

// The file scopes a specialist's grant may name besides team ids.
const (
	FilesMember       = "member"
	FilesOrganization = "org"
)

// MayReachFiles refuses a specialist the files of a scope its grant does not
// name, as not found, so a file outside the grant looks the same as one that
// does not exist. A team's files are named by the team's id. Every other
// caller is limited only by what its member can see.
func (membership OrgMembership) MayReachFiles(scope Scope, teamID string) error {
	if !membership.FilesLimited() {
		return nil
	}
	var name string
	switch scope {
	case ScopeMember:
		name = FilesMember
	case ScopeOrganization:
		name = FilesOrganization
	case ScopeTeam:
		name = teamID
	}
	if name == "" || !slices.Contains(membership.AgentFiles, name) {
		return ErrNotFound
	}
	return nil
}

// FilesLimited is whether the caller is a specialist whose grant names some
// file scopes rather than all.
func (membership OrgMembership) FilesLimited() bool {
	return membership.Agent && membership.AgentRole == "specialist" &&
		membership.AgentFiles != nil && !slices.Contains(membership.AgentFiles, "*")
}

// ActsAsAdmin reports whether the caller acts with an org admin's reach:
// every member's files, every team's whether or not they are on it, the
// organization's files to create or delete, and the files only org owners
// and admins may read. Only a person's own session does. An agent acting for
// an admin gets what a plain member would: its own member files, the teams
// its member is actually on, and the organization's members-level files.
func (membership OrgMembership) ActsAsAdmin() bool {
	return membership.Role.IsOrgAdmin() && !membership.Agent
}

type TeamAccess struct {
	TeamID string
	// CanManage is teams' own answer, which is true for every org admin as
	// well as for the team's own admins.
	CanManage bool
	// Member says the caller is on the team. Teams also shows every team to
	// an org admin who is not.
	Member bool
	// TeamAdmin says the caller holds the team's admin role themselves,
	// rather than managing it as an org admin.
	TeamAdmin bool
}

func (access TeamAccess) CanSee() bool {
	return access.TeamID != ""
}

// Scope is where the object hangs. Privilege still comes from auth (and
// teams, for a team). Purpose is why it exists.
type Scope string

const (
	ScopeMember       Scope = "member"
	ScopeTeam         Scope = "team"
	ScopeOrganization Scope = "organization"
)

func (scope Scope) Valid() bool {
	switch scope {
	case ScopeMember, ScopeTeam, ScopeOrganization:
		return true
	default:
		return false
	}
}

func ParseScope(raw string) (Scope, error) {
	value := Scope(strings.TrimSpace(raw))
	if value == "" {
		return ScopeMember, nil
	}
	if !value.Valid() {
		return "", fmt.Errorf("scope is not allowed: %w", ErrInvalid)
	}
	return value, nil
}

type Purpose string

// Access is the document ACL within an organization-scoped file.
type Access string

const (
	AccessMembers Access = "members"
	AccessAdmins  Access = "admins"
)

func (access Access) Valid() bool { return access == AccessMembers || access == AccessAdmins }

// The allowlist is closed, so a lifecycle or a later pipeline cannot invent a
// storage class. The integrations.* purposes were retired with that service on
// 2026-09-17; see migration 004.
const (
	PurposeUpload            Purpose = "upload"
	PurposePipelineArtifact  Purpose = "pipeline.artifact"
	PurposeBillingAttachment Purpose = "billing.attachment"
)

func (purpose Purpose) Valid() bool {
	switch purpose {
	case PurposeUpload, PurposePipelineArtifact, PurposeBillingAttachment:
		return true
	default:
		return false
	}
}

func (purpose Purpose) DefaultRetentionDays() int {
	switch purpose {
	case PurposeBillingAttachment:
		return MaxRetentionDays
	case PurposePipelineArtifact:
		return 7
	default:
		return DefaultRetentionDays
	}
}

func (purpose Purpose) Fits(scope Scope) error {
	if purpose == PurposeBillingAttachment && scope != ScopeOrganization {
		return fmt.Errorf("billing attachments belong on the organization: %w", ErrInvalid)
	}
	return nil
}

type Organization struct {
	ID      string `db:"organization_id"`
	Name    string `db:"name"`
	Deleted bool   `db:"deleted"`
}

type Object struct {
	ID             string    `json:"id" db:"id"`
	OrganizationID string    `json:"organization_id" db:"organization_id"`
	Scope          Scope     `json:"scope" db:"scope"`
	TeamID         string    `json:"team_id,omitempty" db:"team_id"`
	MemberID       string    `json:"member_id,omitempty" db:"member_id"`
	Filename       string    `json:"filename" db:"filename"`
	ContentType    string    `json:"content_type" db:"content_type"`
	Purpose        Purpose   `json:"purpose" db:"purpose"`
	Access         Access    `json:"access" db:"access"`
	SizeBytes      int64     `json:"size_bytes" db:"size_bytes"`
	SHA256         string    `json:"sha256" db:"sha256"`
	CreatedBy      string    `json:"created_by" db:"created_by"`
	CreatedAt      time.Time `json:"created_at" db:"created_at"`
	ExpiresAt      time.Time `json:"expires_at" db:"expires_at"`
}

func (object Object) EffectiveAccess() Access {
	if object.Purpose == PurposeBillingAttachment {
		return AccessAdmins
	}
	if object.Access == "" {
		return AccessMembers
	}
	return object.Access
}

func (object Object) DatePath() string {
	return object.CreatedAt.UTC().Format("2006/01/02")
}

func (object Object) Key() string {
	day := object.DatePath()
	switch object.Scope {
	case ScopeTeam:
		return "org/" + object.OrganizationID + "/team/" + object.TeamID + "/" + day + "/obj/" + object.ID
	case ScopeOrganization:
		return "org/" + object.OrganizationID + "/organization/" + day + "/obj/" + object.ID
	default:
		memberID := object.MemberID
		if memberID == "" {
			memberID = object.CreatedBy
		}
		return "org/" + object.OrganizationID + "/member/" + memberID + "/" + day + "/obj/" + object.ID
	}
}

func OrganizationPrefix(organizationID string) string {
	return "org/" + organizationID + "/"
}

func TeamPrefix(organizationID, teamID string) string {
	return "org/" + organizationID + "/team/" + teamID + "/"
}

func MemberPrefix(organizationID, memberID string) string {
	return "org/" + organizationID + "/member/" + memberID + "/"
}

type DateFilter struct {
	Year  int
	Month int
	Day   int
}

func (filter DateFilter) Range() (time.Time, time.Time, error) {
	if filter.Year == 0 && filter.Month == 0 && filter.Day == 0 {
		return time.Time{}, time.Time{}, nil
	}
	if filter.Year < 1 {
		return time.Time{}, time.Time{}, fmt.Errorf("year is required when filtering by month or day: %w", ErrInvalid)
	}
	// Past four digits it is not a year anyone means, and the database cannot
	// hold a timestamp that far out: it would answer with an error of its own.
	if filter.Year > 9999 {
		return time.Time{}, time.Time{}, fmt.Errorf("year must be between 1 and 9999: %w", ErrInvalid)
	}
	if filter.Day != 0 && filter.Month == 0 {
		return time.Time{}, time.Time{}, fmt.Errorf("month is required when filtering by day: %w", ErrInvalid)
	}
	if filter.Month == 0 {
		start := time.Date(filter.Year, time.January, 1, 0, 0, 0, 0, time.UTC)
		return start, start.AddDate(1, 0, 0), nil
	}
	if filter.Month < 1 || filter.Month > 12 {
		return time.Time{}, time.Time{}, fmt.Errorf("month must be between 1 and 12: %w", ErrInvalid)
	}
	if filter.Day == 0 {
		start := time.Date(filter.Year, time.Month(filter.Month), 1, 0, 0, 0, 0, time.UTC)
		return start, start.AddDate(0, 1, 0), nil
	}
	start := time.Date(filter.Year, time.Month(filter.Month), filter.Day, 0, 0, 0, 0, time.UTC)
	if start.Year() != filter.Year || start.Month() != time.Month(filter.Month) || start.Day() != filter.Day {
		return time.Time{}, time.Time{}, fmt.Errorf("day is not in that month: %w", ErrInvalid)
	}
	return start, start.AddDate(0, 0, 1), nil
}

// ObjectQuery is how list narrows by placement and UTC created_at. Empty
// Scope means the caller's member files plus organization files they may see.
type ObjectQuery struct {
	Scope       Scope
	TeamID      string
	MemberID    string
	Admin       bool
	Purpose     Purpose
	CreatedFrom time.Time
	CreatedTo   time.Time
}

func Place(scope Scope, teamID, createdBy string) (teamIDOut, memberID string, err error) {
	switch scope {
	case ScopeMember:
		if strings.TrimSpace(teamID) != "" {
			return "", "", fmt.Errorf("member files cannot name a team: %w", ErrInvalid)
		}
		if createdBy == "" {
			return "", "", fmt.Errorf("member files need an owner: %w", ErrInvalid)
		}
		return "", createdBy, nil
	case ScopeTeam:
		teamID = strings.TrimSpace(teamID)
		if teamID == "" {
			return "", "", fmt.Errorf("team files need a team_id: %w", ErrInvalid)
		}
		return teamID, "", nil
	case ScopeOrganization:
		if strings.TrimSpace(teamID) != "" {
			return "", "", fmt.Errorf("organization files cannot name a team: %w", ErrInvalid)
		}
		return "", "", nil
	default:
		return "", "", fmt.Errorf("scope is not allowed: %w", ErrInvalid)
	}
}

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

func Filename(raw string) (string, error) {
	name := strings.TrimSpace(raw)
	if name == "" || name == "." || name == ".." || strings.ContainsAny(name, `/\:`) {
		return "", fmt.Errorf("filename is required and must be a single path segment: %w", ErrInvalid)
	}
	if utf8.RuneCountInString(name) > 255 {
		return "", fmt.Errorf("filename must be at most 255 characters: %w", ErrInvalid)
	}
	if strings.ContainsRune(name, 0) {
		return "", fmt.Errorf("filename is invalid: %w", ErrInvalid)
	}
	return name, nil
}

func ContentType(raw string) (string, error) {
	value := strings.ToLower(strings.TrimSpace(strings.Split(raw, ";")[0]))
	if value == "" {
		value = "application/octet-stream"
	}
	switch value {
	case "application/octet-stream", "application/json", "application/pdf", "application/zip",
		"text/plain", "text/csv", "text/markdown",
		"image/png", "image/jpeg", "image/gif", "image/webp":
		return value, nil
	default:
		return "", fmt.Errorf("content type %q is not allowed: %w", value, ErrInvalid)
	}
}

func RetentionDays(purpose Purpose, days int) (int, error) {
	if days == 0 {
		return purpose.DefaultRetentionDays(), nil
	}
	if days < MinRetentionDays || days > MaxRetentionDays {
		return 0, fmt.Errorf("retention_days must be between %d and %d: %w", MinRetentionDays, MaxRetentionDays, ErrInvalid)
	}
	return days, nil
}

func Size(n int) error {
	if n <= 0 {
		return fmt.Errorf("file content is required: %w", ErrInvalid)
	}
	if n > MaxBytes {
		return fmt.Errorf("file must be at most %d bytes: %w", MaxBytes, ErrInvalid)
	}
	return nil
}
