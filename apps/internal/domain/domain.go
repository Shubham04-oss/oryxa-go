// Package domain is what apps is about: a member's connections to
// third-party apps, the triggers subscribed on them, and the tools run in
// them, independent of Composio's own shapes and of how any of it is stored.
package domain

import (
	"encoding/json"
	"errors"
	"regexp"
	"strings"
	"time"
)

var (
	ErrInvalid      = errors.New("invalid input")
	ErrNotFound     = errors.New("not found")
	ErrForbidden    = errors.New("forbidden")
	ErrUnauthorized = errors.New("unauthorized")
	ErrConflict     = errors.New("conflict")
	ErrRateLimited  = errors.New("too many requests")
	// ErrUnavailable means Composio, auth or the app did not answer.
	ErrUnavailable = errors.New("unavailable")
	// ErrNotConnected means the member has no active connection to the app a
	// call needs.
	ErrNotConnected = errors.New("the app is not connected")
	// ErrChooseConnection means the member has several connections to the
	// app and the call did not say which.
	ErrChooseConnection = errors.New("several connections could be meant")
	// ErrNeedsReconnect means the connection exists but can no longer act:
	// it expired, or Composio refuses it.
	ErrNeedsReconnect = errors.New("the connection needs reconnecting")
	// ErrNotReadOnly means a call that may only read named a tool Composio
	// does not mark as read-only.
	ErrNotReadOnly = errors.New("the tool is not read-only")
	// ErrNotConnectable means the app cannot be connected with what this
	// project holds, such as an OAuth app Composio has no app of its own for.
	ErrNotConnectable = errors.New("the app cannot be connected here")
)

// OrgRole is the caller's role in an organization, as auth answers it.
type OrgRole string

const (
	OrgRoleOwner  OrgRole = "owner"
	OrgRoleAdmin  OrgRole = "admin"
	OrgRoleMember OrgRole = "member"
)

func (role OrgRole) IsOrgAdmin() bool { return role == OrgRoleOwner || role == OrgRoleAdmin }

// OrgMembership is what auth answered for the caller and one organization.
type OrgMembership struct {
	OrganizationID string
	UserID         string
	Role           OrgRole
	// Agent says the caller is an agent acting for this member. An agent may
	// use the member's connections, but never make or remove one.
	Agent bool
}

// ConnectionStatus is where a connection is in its life.
type ConnectionStatus string

const (
	// ConnectionPending waits for the member to finish in their browser.
	ConnectionPending ConnectionStatus = "pending"
	ConnectionActive  ConnectionStatus = "active"
	// ConnectionFailed never completed.
	ConnectionFailed ConnectionStatus = "failed"
	// ConnectionExpired can no longer renew its credential.
	ConnectionExpired ConnectionStatus = "expired"
	ConnectionRemoved ConnectionStatus = "removed"
)

// Connection is one member's connection to one app in one organization.
// Composio holds the credential; this holds whose it is.
type Connection struct {
	ID             string
	OrganizationID string
	MemberID       string
	Toolkit        string
	AuthConfigID   string
	// ComposioUserID is the user Composio files the account under, one per
	// member per organization, so a member's connections in one organization
	// can never be reached from another.
	ComposioUserID string
	// ComposioAccountID is Composio's connected account, known once a link
	// was created.
	ComposioAccountID string
	Status            ConnectionStatus
	StatusReason      string
	// Label tells several connections to one app apart, such as
	// github_red-castle.
	Label string
	// ReturnTo is where the member's browser goes once they finish.
	ReturnTo    string
	CreatedAt   time.Time
	UpdatedAt   time.Time
	ConnectedAt *time.Time
	RemovedAt   *time.Time
}

// ConnectionQuery narrows a listing. An org admin may list the organization's
// connections; anyone else lists their own.
type ConnectionQuery struct {
	MemberID string
	Toolkit  string
	// Statuses keeps only these; empty means every status but removed.
	Statuses []ConnectionStatus
}

// ComposioUserID is the Composio user a member is in one organization. It is
// stable, so the same member reconnecting finds their own accounts, and it
// differs per organization, so nothing crosses between them.
func ComposioUserID(organizationID, memberID string) string {
	return "oryxa." + organizationID + "." + memberID
}

// TriggerStatus is whether a trigger still delivers.
type TriggerStatus string

const (
	TriggerActive   TriggerStatus = "active"
	TriggerDisabled TriggerStatus = "disabled"
	TriggerRemoved  TriggerStatus = "removed"
)

// Trigger is a member's subscription to one event their app delivers.
type Trigger struct {
	ID                string
	OrganizationID    string
	MemberID          string
	ConnectionID      string
	Toolkit           string
	Slug              string
	Config            json.RawMessage
	ComposioTriggerID string
	Status            TriggerStatus
	StatusReason      string
	CreatedAt         time.Time
	UpdatedAt         time.Time
}

// ActionStatus is how a tool run went.
type ActionStatus string

const (
	ActionRunning   ActionStatus = "running"
	ActionSucceeded ActionStatus = "succeeded"
	ActionFailed    ActionStatus = "failed"
)

// Action is one tool run in a member's app, recorded so a write asked again
// under the same key answers what happened rather than happening twice.
type Action struct {
	ID             string
	OrganizationID string
	MemberID       string
	ConnectionID   string
	Toolkit        string
	Tool           string
	ReadOnly       bool
	Agent          bool
	IdempotencyKey string
	Status         ActionStatus
	// Result is what the app answered, kept for a write so a replay can
	// answer it; a read keeps none.
	Result    json.RawMessage
	Error     string
	LogID     string
	CreatedAt time.Time
	// FinishedAt is unset while the run is in flight.
	FinishedAt *time.Time
}

// Event is one outbox row: what happened, for the bus.
type Event struct {
	ID         string
	DetailType string
	OccurredAt time.Time
	Data       json.RawMessage
}

// MaxResultBytes is the most of a tool's answer apps keeps for a replay.
const MaxResultBytes = 256 << 10

// MinIdempotencyKey and MaxIdempotencyKey bound a write's key.
const (
	MinIdempotencyKey = 8
	MaxIdempotencyKey = 128
)

var (
	toolkitSlug = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,63}$`)
	toolSlug    = regexp.MustCompile(`^[A-Z0-9][A-Z0-9_]{0,127}$`)
)

// CheckToolkit normalizes and checks a toolkit slug, such as github.
func CheckToolkit(slug string) (string, error) {
	slug = strings.ToLower(strings.TrimSpace(slug))
	if !toolkitSlug.MatchString(slug) {
		return "", errors.Join(ErrInvalid, errors.New("a toolkit is a lowercase slug such as github"))
	}
	return slug, nil
}

// CheckTool normalizes and checks a tool slug, such as GITHUB_CREATE_AN_ISSUE.
func CheckTool(slug string) (string, error) {
	slug = strings.ToUpper(strings.TrimSpace(slug))
	if !toolSlug.MatchString(slug) {
		return "", errors.Join(ErrInvalid, errors.New("a tool is an uppercase slug such as GITHUB_CREATE_AN_ISSUE"))
	}
	return slug, nil
}

// CheckIdempotencyKey checks a write's key.
func CheckIdempotencyKey(key string) (string, error) {
	key = strings.TrimSpace(key)
	if len(key) < MinIdempotencyKey || len(key) > MaxIdempotencyKey {
		return "", errors.Join(ErrInvalid, errors.New("an idempotency key is 8 to 128 characters"))
	}
	return key, nil
}
