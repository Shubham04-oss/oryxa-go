// Package v1 contains the stable version-one events emitted by connections.
// "Scope" is where a connection hangs (member, team or organization);
// "Scopes" are the provider permissions it holds.
package v1

import "time"

const (
	Source = "oryxa.connections"

	AppCreatedDetailType                   = "connections.app.created.v1"
	AppUpdatedDetailType                   = "connections.app.updated.v1"
	AppDisabledDetailType                  = "connections.app.disabled.v1"
	AppEnabledDetailType                   = "connections.app.enabled.v1"
	AppDeletedDetailType                   = "connections.app.deleted.v1"
	ConnectionConnectedDetailType          = "connections.connection.connected.v1"
	ConnectionReconnectedDetailType        = "connections.connection.reconnected.v1"
	ConnectionNeedsReconnectDetailType     = "connections.connection.needs-reconnect.v1"
	ConnectionDisconnectedDetailType       = "connections.connection.disconnected.v1"
	ConnectionCredentialsDeletedDetailType = "connections.connection.credentials-deleted.v1"
	ConnectionHealthChangedDetailType      = "connections.connection.health-changed.v1"
)

type ProviderAccount struct {
	ID   string `json:"id"`
	Name string `json:"name,omitempty"`
}

type AppCreated struct {
	AppID          string    `json:"app_id"`
	OrganizationID string    `json:"organization_id"`
	Provider       string    `json:"provider"`
	CredentialType string    `json:"credential_type"`
	Scopes         []string  `json:"scopes"`
	CreatedBy      string    `json:"created_by"`
	CreatedAt      time.Time `json:"created_at"`
}

// AppUpdated lists what changed in Changed: AppChangedScopes,
// AppChangedSecret, or AppChangedClient.
type AppUpdated struct {
	AppID          string    `json:"app_id"`
	OrganizationID string    `json:"organization_id"`
	Provider       string    `json:"provider"`
	Scopes         []string  `json:"scopes"`
	Changed        []string  `json:"changed"`
	UpdatedBy      string    `json:"updated_by"`
	UpdatedAt      time.Time `json:"updated_at"`
}

type AppDisabled struct {
	AppID          string    `json:"app_id"`
	OrganizationID string    `json:"organization_id"`
	Provider       string    `json:"provider"`
	DisabledBy     string    `json:"disabled_by"`
	DisabledAt     time.Time `json:"disabled_at"`
}

type AppEnabled struct {
	AppID          string    `json:"app_id"`
	OrganizationID string    `json:"organization_id"`
	Provider       string    `json:"provider"`
	EnabledBy      string    `json:"enabled_by"`
	EnabledAt      time.Time `json:"enabled_at"`
}

type AppDeleted struct {
	AppID          string    `json:"app_id"`
	OrganizationID string    `json:"organization_id"`
	Provider       string    `json:"provider"`
	DeletedBy      string    `json:"deleted_by,omitempty"`
	Reason         string    `json:"reason"`
	DeletedAt      time.Time `json:"deleted_at"`
}

const (
	AppChangedScopes = "scopes"
	AppChangedSecret = "secret"
	AppChangedClient = "client"

	AppDeletedReasonDeleted             = "deleted"
	AppDeletedReasonOrganizationDeleted = "organization_deleted"
)

type ConnectionConnected struct {
	ConnectionID    string          `json:"connection_id"`
	OrganizationID  string          `json:"organization_id"`
	AppID           string          `json:"app_id"`
	Provider        string          `json:"provider"`
	CredentialType  string          `json:"credential_type"`
	Scope           string          `json:"scope"`
	TeamID          string          `json:"team_id,omitempty"`
	MemberID        string          `json:"member_id,omitempty"`
	ProviderAccount ProviderAccount `json:"provider_account"`
	Scopes          []string        `json:"scopes"`
	ConnectedBy     string          `json:"connected_by"`
	ConnectedAt     time.Time       `json:"connected_at"`
}

type ConnectionReconnected struct {
	ConnectionID    string          `json:"connection_id"`
	OrganizationID  string          `json:"organization_id"`
	AppID           string          `json:"app_id"`
	Provider        string          `json:"provider"`
	Scope           string          `json:"scope"`
	TeamID          string          `json:"team_id,omitempty"`
	MemberID        string          `json:"member_id,omitempty"`
	ProviderAccount ProviderAccount `json:"provider_account"`
	Scopes          []string        `json:"scopes"`
	ReconnectedBy   string          `json:"reconnected_by"`
	ReconnectedAt   time.Time       `json:"reconnected_at"`
}

type ConnectionNeedsReconnect struct {
	ConnectionID    string          `json:"connection_id"`
	OrganizationID  string          `json:"organization_id"`
	AppID           string          `json:"app_id"`
	Provider        string          `json:"provider"`
	Scope           string          `json:"scope"`
	TeamID          string          `json:"team_id,omitempty"`
	MemberID        string          `json:"member_id,omitempty"`
	ProviderAccount ProviderAccount `json:"provider_account"`
	Reason          string          `json:"reason"`
	DetectedAt      time.Time       `json:"detected_at"`
}

type ConnectionDisconnected struct {
	ConnectionID    string          `json:"connection_id"`
	OrganizationID  string          `json:"organization_id"`
	AppID           string          `json:"app_id"`
	Provider        string          `json:"provider"`
	Scope           string          `json:"scope"`
	TeamID          string          `json:"team_id,omitempty"`
	MemberID        string          `json:"member_id,omitempty"`
	ProviderAccount ProviderAccount `json:"provider_account"`
	DisconnectedBy  string          `json:"disconnected_by,omitempty"`
	Reason          string          `json:"reason"`
	DisconnectedAt  time.Time       `json:"disconnected_at"`
}

// ConnectionCredentialsDeleted records that a disconnected connection's
// credentials are gone. Revoked says whether the provider revoked them too.
type ConnectionCredentialsDeleted struct {
	ConnectionID   string    `json:"connection_id"`
	OrganizationID string    `json:"organization_id"`
	AppID          string    `json:"app_id"`
	Provider       string    `json:"provider"`
	Revoked        bool      `json:"revoked"`
	DeletedAt      time.Time `json:"deleted_at"`
}

type ConnectionHealthChanged struct {
	ConnectionID   string    `json:"connection_id"`
	OrganizationID string    `json:"organization_id"`
	AppID          string    `json:"app_id"`
	Provider       string    `json:"provider"`
	Health         string    `json:"health"`
	PreviousHealth string    `json:"previous_health"`
	Reason         string    `json:"reason,omitempty"`
	CheckedAt      time.Time `json:"checked_at"`
}

const (
	NeedsReconnectReasonRefreshRejected     = "refresh_rejected"
	NeedsReconnectReasonUnauthorized        = "unauthorized"
	NeedsReconnectReasonAccountChanged      = "account_changed"
	NeedsReconnectReasonClientChanged       = "client_changed"
	NeedsReconnectReasonInstallationRemoved = "installation_removed"
	NeedsReconnectReasonCredentialRejected  = "credential_rejected"

	DisconnectedReasonDisconnected        = "disconnected"
	DisconnectedReasonMemberLeft          = "member_left"
	DisconnectedReasonTeamDeleted         = "team_deleted"
	DisconnectedReasonOrganizationDeleted = "organization_deleted"
	DisconnectedReasonAppDeleted          = "app_deleted"

	HealthUnknown  = "unknown"
	HealthHealthy  = "healthy"
	HealthDegraded = "degraded"
)
