package domain

import (
	"encoding/json"
	"errors"
	"time"
)

var (
	ErrNotFound = errors.New("integration resource not found")
	ErrConflict = errors.New("integration resource conflict")
)

type OAuthAppMode string

const (
	OAuthAppModeBYOC    OAuthAppMode = "byoc"
	OAuthAppModeManaged OAuthAppMode = "managed"
)

type OAuthAppStatus string

const (
	OAuthAppStatusActive   OAuthAppStatus = "active"
	OAuthAppStatusDisabled OAuthAppStatus = "disabled"
)

// SealedData is an envelope-encrypted value. The plaintext data key is never
// persisted; EncryptedDataKey can only be opened with the same KMS context.
type SealedData struct {
	Version          int    `json:"version"`
	Nonce            []byte `json:"nonce"`
	Ciphertext       []byte `json:"ciphertext"`
	EncryptedDataKey []byte `json:"encrypted_data_key"`
}

type OAuthAppProfile struct {
	ID                 string
	OrganizationID     string
	Provider           string
	PieceName          string
	PieceVersion       string
	Mode               OAuthAppMode
	ClientID           string
	EncryptedSecret    SealedData
	Scopes             []string
	Status             OAuthAppStatus
	CreatedBy          string
	CreatedAt          time.Time
	UpdatedAt          time.Time
	CredentialRevision int64
}

type OAuthAuthorizationState struct {
	StateHash         [32]byte
	OrganizationID    string
	MemberID          string
	ProfileID         string
	Provider          string
	RedirectURI       string
	ReturnURI         string
	Scopes            []string
	EncryptedVerifier SealedData
	ExpiresAt         time.Time
	CreatedAt         time.Time
}

type ProviderIdentity struct {
	SubjectID   string `json:"subject_id"`
	AccountID   string `json:"account_id,omitempty"`
	AccountName string `json:"account_name,omitempty"`
	TenantID    string `json:"tenant_id,omitempty"`
	TenantName  string `json:"tenant_name,omitempty"`
}

type ConnectionStatus string

const (
	ConnectionStatusActive                  ConnectionStatus = "active"
	ConnectionStatusReauthorizationRequired ConnectionStatus = "reauthorization_required"
	ConnectionStatusDisconnected            ConnectionStatus = "disconnected"
)

type Connection struct {
	ID                   string
	OrganizationID       string
	MemberID             string
	ProfileID            string
	Provider             string
	PieceName            string
	PieceVersion         string
	Identity             ProviderIdentity
	GrantedScopes        []string
	EncryptedCredentials SealedData
	Status               ConnectionStatus
	CreatedAt            time.Time
	UpdatedAt            time.Time
	Version              int64
}

type MemberIntegrationState string

const (
	MemberIntegrationAvailable MemberIntegrationState = "available"
	MemberIntegrationConnected MemberIntegrationState = "connected"
)

// MemberIntegration is safe to return to an organization member. It never
// contains an OAuth client secret, access token, refresh token, KMS data key,
// or encrypted credential envelope.
type MemberIntegration struct {
	ProfileID    string                 `json:"profile_id"`
	Provider     string                 `json:"provider"`
	PieceName    string                 `json:"piece_name"`
	PieceVersion string                 `json:"piece_version"`
	Mode         OAuthAppMode           `json:"mode"`
	Scopes       []string               `json:"scopes"`
	State        MemberIntegrationState `json:"state"`
	Connection   *MemberConnection      `json:"connection,omitempty"`
}

type MemberConnection struct {
	ID          string           `json:"id"`
	Identity    ProviderIdentity `json:"identity"`
	ConnectedAt time.Time        `json:"connected_at"`
}

// ConnectionCatalogEntry is the credential-free durable connection record
// that other services may consume or project into their own data stores.
type ConnectionCatalogEntry struct {
	ConnectionID   string           `json:"connection_id"`
	OrganizationID string           `json:"organization_id"`
	MemberID       string           `json:"member_id"`
	ProfileID      string           `json:"profile_id"`
	Provider       string           `json:"provider"`
	PieceName      string           `json:"piece_name"`
	PieceVersion   string           `json:"piece_version"`
	Identity       ProviderIdentity `json:"identity"`
	Scopes         []string         `json:"scopes"`
	ConnectedAt    time.Time        `json:"connected_at"`
	UpdatedAt      time.Time        `json:"updated_at"`
}

type Event struct {
	EventID        string    `json:"event_id"`
	Source         string    `json:"source"`
	DetailType     string    `json:"detail_type"`
	Version        int       `json:"version"`
	OrganizationID string    `json:"organization_id"`
	OccurredAt     time.Time `json:"occurred_at"`
	Data           any       `json:"data"`
}

type OutboxEvent struct {
	Event
	Attempts int
}

type WebhookRouteMode string

const (
	WebhookRoutePerConnection WebhookRouteMode = "per_connection"
	WebhookRouteAppLevel      WebhookRouteMode = "app_level"
	// SlackEventsRouteTrigger is the shared Dynamo/Postgres route key for
	// every Slack Events API trigger. Slack allows one Events URL per app, so
	// admission fans out by workspace rather than by trigger name.
	SlackEventsRouteTrigger = "slack-events"
)

type WebhookRoute struct {
	OpaqueRoute        string
	OrganizationID     string
	ProfileID          string
	ConnectionID       string
	Provider           string
	PieceName          string
	PieceVersion       string
	TriggerName        string
	Mode               WebhookRouteMode
	SigningSecret      SealedData
	TimestampHeader    string
	TimestampWindow    time.Duration
	MaxBodyBytes       int64
	Active             bool
	CredentialRevision int64
}

type TriggerStatus string

const (
	TriggerStatusActive   TriggerStatus = "active"
	TriggerStatusDisabled TriggerStatus = "disabled"
)

// Trigger is durable product configuration. WebhookRoute is deliberately a
// separate, opaque ingress-routing record: an untrusted provider request can
// select a route but never a tenant connection directly.
type Trigger struct {
	ID                 string
	OrganizationID     string
	MemberID           string
	ConnectionID       string
	ProfileID          string
	Provider           string
	PieceName          string
	PieceVersion       string
	TriggerName        string
	ProviderIdentifier string
	Props              json.RawMessage
	Status             TriggerStatus
	CreatedAt          time.Time
	UpdatedAt          time.Time
	Version            int64
}

// MicrosoftGraphCleanupJob is the durable, credential-free instruction to
// remove an external Graph subscription after its local trigger is disabled.
// The connection is populated only while a trusted worker holds the job; it
// is never persisted in this struct or exposed outside the worker boundary.
type MicrosoftGraphCleanupJob struct {
	OrganizationID string
	TriggerID      string
	ConnectionID   string
	ProfileID      string
	TriggerName    string
	// CreatedAt is the disable-time safety boundary. A renewal PATCH that was
	// already in flight can have extended the remote subscription after local
	// state stopped being updated, so cleanup must retain the job until the
	// maximum possible remote lease from this point has elapsed.
	CreatedAt  time.Time
	Attempt    int
	LeaseToken string
	Connection Connection
}

// GoogleCleanupJob is the durable instruction to stop a Google Calendar
// channel or Gmail users.watch after its local trigger is disabled.
type GoogleCleanupJob struct {
	OrganizationID string
	TriggerID      string
	ConnectionID   string
	ProfileID      string
	TriggerName    string
	CreatedAt      time.Time
	Attempt        int
	LeaseToken     string
	Connection     Connection
}

// GitHubCleanupJob is the durable instruction to delete a GitHub repository
// or organization hook after its local trigger is disabled. The connection is
// populated only while a trusted worker holds the job.
type GitHubCleanupJob struct {
	OrganizationID string
	TriggerID      string
	ConnectionID   string
	ProfileID      string
	TriggerName    string
	CreatedAt      time.Time
	Attempt        int
	LeaseToken     string
	Connection     Connection
}

type ExecutionStatus string

const (
	ExecutionStatusProcessing ExecutionStatus = "processing"
	ExecutionStatusSucceeded  ExecutionStatus = "succeeded"
	ExecutionStatusFailed     ExecutionStatus = "failed"
)

type TriggerExecution struct {
	ID             string
	OrganizationID string
	TriggerID      string
	InvocationID   string
	Status         ExecutionStatus
	Attempt        int
	StartedAt      time.Time
	CompletedAt    *time.Time
}

// ActionExecution is the durable idempotency and audit boundary around a
// connector action. Only the validated, provider-neutral result is persisted;
// raw piece output and credentials remain inside the execution process.
type ActionExecution struct {
	ID                 string
	OrganizationID     string
	MemberID           string
	ConnectionID       string
	Provider           string
	PieceName          string
	PieceVersion       string
	ActionName         string
	IdempotencyKeyHash string
	InputHash          string
	Status             ExecutionStatus
	Attempt            int
	SafeResult         json.RawMessage
	StartedAt          time.Time
	CompletedAt        *time.Time
}
