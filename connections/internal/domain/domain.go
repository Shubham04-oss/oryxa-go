// Package domain holds connections' types and the rules that need no I/O:
// where a connection hangs, who may do what with it, scope checks, and status
// transitions.
package domain

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

var (
	ErrInvalid             = errors.New("invalid input")
	ErrNotFound            = errors.New("not found")
	ErrForbidden           = errors.New("forbidden")
	ErrUnauthorized        = errors.New("unauthorized")
	ErrConflict            = errors.New("conflict")
	ErrAlreadyConnected    = errors.New("provider account is already connected in this scope")
	ErrAccountChanged      = errors.New("provider account changed")
	ErrNeedsReconnect      = errors.New("connection needs reconnect")
	ErrAppDisabled         = errors.New("app is disabled")
	ErrScopeRequired       = errors.New("scope required")
	ErrCredentialRejected  = errors.New("provider rejected the credential")
	ErrSessionExpired      = errors.New("authorization session expired")
	ErrProviderUnavailable = errors.New("provider unavailable")
	ErrRateLimited         = errors.New("too many requests")
	// ErrAgentForbidden is what the public API answers when an agent's
	// membership asks for anything but a GET route.
	ErrAgentForbidden = errors.New("an agent cannot change this")
)

// ScopeRequiredError names the provider scopes a request is missing. It
// matches ErrScopeRequired with errors.Is.
type ScopeRequiredError struct {
	Missing []string
}

func (err ScopeRequiredError) Error() string {
	return ErrScopeRequired.Error() + ": " + strings.Join(err.Missing, " ")
}

func (err ScopeRequiredError) Unwrap() error { return ErrScopeRequired }

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

// OrgMembership is who the caller is in an organization, as auth answered.
type OrgMembership struct {
	OrganizationID string
	UserID         string
	Role           OrgRole
	// Agent is whether the caller's token is an agent's, as auth's
	// organization response named it. An agent's membership may only read on
	// the public API.
	Agent bool
}

// TeamAccess is what teams answered for the caller and one team.
type TeamAccess struct {
	TeamID    string
	CanManage bool
	Member    bool
}

func (access TeamAccess) CanSee() bool {
	return access.TeamID != ""
}

// Scope is who a connection belongs to. It is placement, not privilege:
// privilege still comes from auth, and from teams for a team connection.
// Provider permissions are the separate "scopes" on apps and connections.
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

type Placement struct {
	Scope    Scope
	TeamID   string
	MemberID string
}

// Place resolves where a new connection hangs. Member connections belong to
// the caller.
func Place(scope Scope, teamID, callerID string) (Placement, error) {
	teamID = strings.TrimSpace(teamID)
	switch scope {
	case ScopeMember:
		if teamID != "" {
			return Placement{}, fmt.Errorf("member connections cannot name a team: %w", ErrInvalid)
		}
		if callerID == "" {
			return Placement{}, fmt.Errorf("member connections need an owner: %w", ErrInvalid)
		}
		return Placement{Scope: ScopeMember, MemberID: callerID}, nil
	case ScopeTeam:
		if teamID == "" {
			return Placement{}, fmt.Errorf("team connections need a team_id: %w", ErrInvalid)
		}
		return Placement{Scope: ScopeTeam, TeamID: teamID}, nil
	case ScopeOrganization:
		if teamID != "" {
			return Placement{}, fmt.Errorf("organization connections cannot name a team: %w", ErrInvalid)
		}
		return Placement{Scope: ScopeOrganization}, nil
	default:
		return Placement{}, fmt.Errorf("scope is not allowed: %w", ErrInvalid)
	}
}

// Usage narrows who may use an organization connection.
type Usage string

const (
	UsageMembers Usage = "members"
	UsageAdmins  Usage = "admins"
)

func ParseUsage(raw string, scope Scope) (Usage, error) {
	value := Usage(strings.TrimSpace(raw))
	if value == "" {
		return UsageMembers, nil
	}
	if value != UsageMembers && value != UsageAdmins {
		return "", fmt.Errorf("usage is not allowed: %w", ErrInvalid)
	}
	if value == UsageAdmins && scope != ScopeOrganization {
		return "", fmt.Errorf("only organization connections can be limited to admins: %w", ErrInvalid)
	}
	return value, nil
}

type CredentialType string

const (
	CredentialOAuth2                  CredentialType = "oauth2"
	CredentialOAuth2ClientCredentials CredentialType = "oauth2_client_credentials"
	CredentialInstallation            CredentialType = "installation"
	CredentialAPIKey                  CredentialType = "api_key"
	CredentialBearer                  CredentialType = "bearer"
	CredentialBasic                   CredentialType = "basic"
)

func (credentialType CredentialType) Valid() bool {
	switch credentialType {
	case CredentialOAuth2, CredentialOAuth2ClientCredentials, CredentialInstallation,
		CredentialAPIKey, CredentialBearer, CredentialBasic:
		return true
	default:
		return false
	}
}

// Authorizes reports whether connections of this type are made through a
// provider redirect: OAuth consent or an app installation.
func (credentialType CredentialType) Authorizes() bool {
	return credentialType == CredentialOAuth2 || credentialType == CredentialInstallation
}

// KeyBased reports whether the connection holds a secret its owner supplies.
func (credentialType CredentialType) KeyBased() bool {
	return credentialType == CredentialAPIKey || credentialType == CredentialBearer || credentialType == CredentialBasic
}

// Fits rejects placements a credential type cannot have. No person consents
// to client credentials, so they cannot belong to a member.
func (credentialType CredentialType) Fits(scope Scope) error {
	if !credentialType.Valid() {
		return fmt.Errorf("credential type is not allowed: %w", ErrInvalid)
	}
	if credentialType == CredentialOAuth2ClientCredentials && scope == ScopeMember {
		return fmt.Errorf("client credentials belong to a team or the organization: %w", ErrInvalid)
	}
	return nil
}

type AppStatus string

const (
	AppStatusActive   AppStatus = "active"
	AppStatusDisabled AppStatus = "disabled"
)

type Status string

const (
	StatusActive         Status = "active"
	StatusNeedsReconnect Status = "needs_reconnect"
	StatusDisconnected   Status = "disconnected"
)

// ReconnectReason says why a connection needs reconnecting.
type ReconnectReason string

const (
	ReconnectRefreshRejected     ReconnectReason = "refresh_rejected"
	ReconnectUnauthorized        ReconnectReason = "unauthorized"
	ReconnectAccountChanged      ReconnectReason = "account_changed"
	ReconnectClientChanged       ReconnectReason = "client_changed"
	ReconnectInstallationRemoved ReconnectReason = "installation_removed"
	ReconnectCredentialRejected  ReconnectReason = "credential_rejected"
)

func (reason ReconnectReason) Valid() bool {
	switch reason {
	case ReconnectRefreshRejected, ReconnectUnauthorized, ReconnectAccountChanged,
		ReconnectClientChanged, ReconnectInstallationRemoved, ReconnectCredentialRejected:
		return true
	default:
		return false
	}
}

// DisconnectReason says why a connection ended.
type DisconnectReason string

const (
	DisconnectRequested           DisconnectReason = "disconnected"
	DisconnectMemberLeft          DisconnectReason = "member_left"
	DisconnectTeamDeleted         DisconnectReason = "team_deleted"
	DisconnectOrganizationDeleted DisconnectReason = "organization_deleted"
	DisconnectAppDeleted          DisconnectReason = "app_deleted"
)

func (reason DisconnectReason) Valid() bool {
	switch reason {
	case DisconnectRequested, DisconnectMemberLeft, DisconnectTeamDeleted,
		DisconnectOrganizationDeleted, DisconnectAppDeleted:
		return true
	default:
		return false
	}
}

type Health string

const (
	HealthUnknown  Health = "unknown"
	HealthHealthy  Health = "healthy"
	HealthDegraded Health = "degraded"
)

// Purpose is why a service asks for a token.
type Purpose string

const (
	PurposeAction    Purpose = "action"
	PurposeTrigger   Purpose = "trigger"
	PurposeCleanup   Purpose = "cleanup"
	PurposeHeartbeat Purpose = "heartbeat"
)

func (purpose Purpose) Valid() bool {
	switch purpose {
	case PurposeAction, PurposeTrigger, PurposeCleanup, PurposeHeartbeat:
		return true
	default:
		return false
	}
}

// SealedData is an envelope-encrypted value. The plaintext data key is never
// stored; EncryptedDataKey opens only with the same KMS encryption context.
type SealedData struct {
	Version          int    `json:"version"`
	Nonce            []byte `json:"nonce"`
	Ciphertext       []byte `json:"ciphertext"`
	EncryptedDataKey []byte `json:"encrypted_data_key"`
}

type ProviderAccount struct {
	ID   string `json:"id"`
	Name string `json:"name,omitempty"`
}

// App is one organization enabling one provider. Its client ID and secret
// never appear in JSON; the HTTP layer decides what an admin sees.
type App struct {
	ID             string         `json:"id"`
	OrganizationID string         `json:"organization_id"`
	Provider       string         `json:"provider"`
	CredentialType CredentialType `json:"credential_type"`
	Scopes         []string       `json:"scopes"`
	Settings       AppSettings    `json:"settings,omitzero"`
	Status         AppStatus      `json:"status"`
	CreatedBy      string         `json:"created_by"`
	CreatedAt      time.Time      `json:"created_at"`
	UpdatedAt      time.Time      `json:"updated_at"`
	ClientID       string         `json:"-"`
	// EncryptedSecret seals the app's client secret and private key.
	EncryptedSecret *SealedData `json:"-"`
	Revision        int64       `json:"-"`
}

// AppSettings are an app's provider settings that are not secret.
type AppSettings struct {
	// GitHubAppID and GitHubAppSlug name the GitHub App of an installation
	// app.
	GitHubAppID   string `json:"github_app_id,omitempty"`
	GitHubAppSlug string `json:"github_app_slug,omitempty"`
	// Domain is the organization's own address at a provider reached through
	// one, such as the Atlassian site API tokens work on.
	Domain string `json:"domain,omitempty"`
}

// Connection is one provider account reached from a scope. Its credentials
// never appear in JSON.
type Connection struct {
	ID              string           `json:"id"`
	OrganizationID  string           `json:"organization_id"`
	AppID           string           `json:"app_id"`
	Provider        string           `json:"provider"`
	CredentialType  CredentialType   `json:"credential_type"`
	Scope           Scope            `json:"scope"`
	TeamID          string           `json:"team_id,omitempty"`
	MemberID        string           `json:"member_id,omitempty"`
	Usage           Usage            `json:"usage"`
	ProviderAccount ProviderAccount  `json:"provider_account"`
	Label           string           `json:"label,omitempty"`
	Default         bool             `json:"default"`
	Scopes          []string         `json:"scopes"`
	Status          Status           `json:"status"`
	Reason          string           `json:"reason,omitempty"`
	StatusChangedAt time.Time        `json:"status_changed_at"`
	LastUsedAt      *time.Time       `json:"last_used_at,omitempty"`
	CreatedBy       string           `json:"created_by"`
	CreatedAt       time.Time        `json:"connected_at"`
	UpdatedAt       time.Time        `json:"updated_at"`
	Health          ConnectionHealth `json:"health"`
	// Blocked says why a live connection cannot be used right now, such as
	// "app_disabled".
	Blocked              string      `json:"blocked,omitempty"`
	EncryptedCredentials *SealedData `json:"-"`
	CredentialsDeletedAt *time.Time  `json:"-"`
	// LastReportedAt is when a caller last reported the provider rejecting
	// the connection's credential.
	LastReportedAt *time.Time `json:"-"`
	Version        int64      `json:"-"`
}

func (connection Connection) Placement() Placement {
	return Placement{Scope: connection.Scope, TeamID: connection.TeamID, MemberID: connection.MemberID}
}

const MaxLabelLength = 120

// Label is a connection's display name: optional, one line, at most
// MaxLabelLength characters.
func Label(raw string) (string, error) {
	label := strings.TrimSpace(raw)
	if utf8.RuneCountInString(label) > MaxLabelLength {
		return "", fmt.Errorf("label must be at most %d characters: %w", MaxLabelLength, ErrInvalid)
	}
	if strings.IndexFunc(label, unicode.IsControl) >= 0 {
		return "", fmt.Errorf("label must be one line of text: %w", ErrInvalid)
	}
	return label, nil
}

type Event struct {
	ID         string
	DetailType string
	OccurredAt time.Time
	Data       json.RawMessage
}
