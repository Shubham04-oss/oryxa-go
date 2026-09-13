// Package v1 contains the stable version-one events emitted by integrations.
package v1

import "time"

const (
	Source = "oryxa.integrations"

	ConnectionConnectedDetailType               = "integrations.connection.connected.v1"
	ConnectionReauthorizationRequiredDetailType = "integrations.connection.reauthorization-required.v1"
	ConnectionDisconnectedDetailType            = "integrations.connection.disconnected.v1"
	OAuthAppRegisteredDetailType                = "integrations.oauth-app.registered.v1"
	OAuthAppCredentialsRotatedDetailType        = "integrations.oauth-app.credentials-rotated.v1"
	OAuthAppDisabledDetailType                  = "integrations.oauth-app.disabled.v1"
	CatalogOperationEnabledDetailType           = "integrations.catalog.operation-enabled.v1"
	CatalogOperationDisabledDetailType          = "integrations.catalog.operation-disabled.v1"
	TriggerEnabledDetailType                    = "integrations.trigger.enabled.v1"
	TriggerDisabledDetailType                   = "integrations.trigger.disabled.v1"
	SlackMessageReceivedDetailType              = "integrations.slack.message-received.v1"
)

type ProviderIdentity struct {
	SubjectID   string `json:"subject_id"`
	AccountID   string `json:"account_id,omitempty"`
	AccountName string `json:"account_name,omitempty"`
	TenantID    string `json:"tenant_id,omitempty"`
	TenantName  string `json:"tenant_name,omitempty"`
}

type ConnectionConnected struct {
	ConnectionID string           `json:"connection_id"`
	MemberID     string           `json:"member_id"`
	ProfileID    string           `json:"profile_id"`
	Provider     string           `json:"provider"`
	Identity     ProviderIdentity `json:"identity"`
	Scopes       []string         `json:"scopes"`
	ConnectedAt  time.Time        `json:"connected_at"`
}

type OAuthAppRegistered struct {
	ProfileID    string    `json:"profile_id"`
	Provider     string    `json:"provider"`
	Mode         string    `json:"mode"`
	Scopes       []string  `json:"scopes"`
	RegisteredBy string    `json:"registered_by"`
	RegisteredAt time.Time `json:"registered_at"`
}

type OAuthAppCredentialsRotated struct {
	ProfileID          string    `json:"profile_id"`
	Provider           string    `json:"provider"`
	CredentialRevision int64     `json:"credential_revision"`
	RotatedBy          string    `json:"rotated_by"`
	RotatedAt          time.Time `json:"rotated_at"`
}

type OAuthAppDisabled struct {
	ProfileID  string    `json:"profile_id"`
	Provider   string    `json:"provider"`
	DisabledBy string    `json:"disabled_by"`
	DisabledAt time.Time `json:"disabled_at"`
}

type ConnectionReauthorizationRequired struct {
	ConnectionID string    `json:"connection_id"`
	Provider     string    `json:"provider"`
	Reason       string    `json:"reason"`
	RequiredAt   time.Time `json:"required_at"`
}

type ConnectionDisconnected struct {
	ConnectionID   string    `json:"connection_id"`
	Provider       string    `json:"provider"`
	DisconnectedBy string    `json:"disconnected_by"`
	DisconnectedAt time.Time `json:"disconnected_at"`
}

type CatalogOperationEnabled struct {
	PieceName    string    `json:"piece_name"`
	PieceVersion string    `json:"piece_version"`
	Operation    string    `json:"operation"`
	EnabledAt    time.Time `json:"enabled_at"`
}

type CatalogOperationDisabled struct {
	PieceName    string    `json:"piece_name"`
	PieceVersion string    `json:"piece_version"`
	Operation    string    `json:"operation"`
	Reason       string    `json:"reason"`
	DisabledAt   time.Time `json:"disabled_at"`
}

type TriggerEnabled struct {
	TriggerID    string    `json:"trigger_id"`
	ConnectionID string    `json:"connection_id"`
	ProfileID    string    `json:"profile_id"`
	Provider     string    `json:"provider"`
	TriggerName  string    `json:"trigger_name"`
	EnabledBy    string    `json:"enabled_by"`
	EnabledAt    time.Time `json:"enabled_at"`
}

type TriggerDisabled struct {
	TriggerID    string    `json:"trigger_id"`
	ConnectionID string    `json:"connection_id"`
	Provider     string    `json:"provider"`
	DisabledBy   string    `json:"disabled_by"`
	DisabledAt   time.Time `json:"disabled_at"`
}

// SlackMessageReceived is the stable, credential-free domain projection of
// Slack's message event. Raw webhook headers and provider tokens never cross
// the integrations boundary.
type SlackMessageReceived struct {
	TriggerID    string         `json:"trigger_id"`
	ConnectionID string         `json:"connection_id"`
	WorkspaceID  string         `json:"workspace_id"`
	Messages     []SlackMessage `json:"messages"`
	ReceivedAt   time.Time      `json:"received_at"`
}

type SlackMessage struct {
	ChannelID       string `json:"channel_id"`
	UserID          string `json:"user_id,omitempty"`
	Text            string `json:"text,omitempty"`
	Timestamp       string `json:"timestamp"`
	ThreadTimestamp string `json:"thread_timestamp,omitempty"`
	Subtype         string `json:"subtype,omitempty"`
}
