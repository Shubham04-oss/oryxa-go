// Package domain holds integrations' model: subscriptions to provider events,
// the app-level webhooks some providers deliver through, and the rules both
// follow. It depends on nothing but the standard library.
package domain

import (
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strings"
	"time"
)

var (
	ErrInvalid             = errors.New("invalid input")
	ErrNotFound            = errors.New("not found")
	ErrForbidden           = errors.New("forbidden")
	ErrUnauthorized        = errors.New("unauthorized")
	ErrConflict            = errors.New("conflict")
	ErrNeedsReconnect      = errors.New("connection needs reconnect")
	ErrCredentialRejected  = errors.New("provider rejected the credential")
	ErrProviderUnavailable = errors.New("provider unavailable")
	// ErrWebhookSecretRequired means a provider delivers through its app, and
	// the app's signing secret has not been set.
	ErrWebhookSecretRequired = errors.New("the app's webhook signing secret is not set")
)

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
	// Agent says the caller is an agent acting for this member with a token
	// auth issued to its agent client: it acts as the member, with less reach.
	Agent bool
}

type ProviderAccount struct {
	ID   string `json:"id"`
	Name string `json:"name,omitempty"`
}

// Connection is what connections tells integrations about a connection.
type Connection struct {
	ID              string          `json:"id"`
	OrganizationID  string          `json:"organization_id"`
	AppID           string          `json:"app_id"`
	Provider        string          `json:"provider"`
	CredentialType  string          `json:"credential_type"`
	Scope           string          `json:"scope"`
	TeamID          string          `json:"team_id,omitempty"`
	MemberID        string          `json:"member_id,omitempty"`
	ProviderAccount ProviderAccount `json:"provider_account"`
	Status          string          `json:"status"`
}

// Credential is a token connections issued for a connection, with where it
// goes on a request.
type Credential struct {
	AccessToken string
	// In is "header" or "query"; Name and Prefix say how the token is sent.
	In       string
	Name     string
	Prefix   string
	Domain   string
	Metadata map[string]string
}

// Purpose is why a service asks connections for a credential.
type Purpose string

const (
	PurposeTrigger Purpose = "trigger"
	PurposeCleanup Purpose = "cleanup"
	PurposeAction  Purpose = "action"
)

// Holder is the hold integrations keeps on a connection while a registration
// it made at the provider may still exist.
const Holder = "integrations:subscriptions"

type SubscriptionStatus string

const (
	// SubscriptionPending is registering at the provider. It already answers
	// the provider's handshake.
	SubscriptionPending SubscriptionStatus = "pending"
	SubscriptionActive  SubscriptionStatus = "active"
	// SubscriptionFailed no longer delivers and cannot be restored.
	SubscriptionFailed SubscriptionStatus = "failed"
	// SubscriptionStopped no longer delivers because it was stopped.
	SubscriptionStopped SubscriptionStatus = "stopped"
)

// Receives reports whether deliveries for a subscription in this status are
// published.
func (status SubscriptionStatus) Receives() bool {
	return status == SubscriptionPending || status == SubscriptionActive
}

// Subscription is one connection's interest in a provider's events.
type Subscription struct {
	ID              string             `json:"id"`
	OrganizationID  string             `json:"organization_id"`
	ConnectionID    string             `json:"connection_id"`
	AppID           string             `json:"app_id"`
	Provider        string             `json:"provider"`
	Scope           string             `json:"scope"`
	TeamID          string             `json:"team_id,omitempty"`
	MemberID        string             `json:"member_id,omitempty"`
	ProviderAccount ProviderAccount    `json:"provider_account"`
	Target          map[string]string  `json:"target"`
	Events          []string           `json:"events"`
	Status          SubscriptionStatus `json:"status"`
	Reason          string             `json:"reason,omitempty"`
	CreatedBy       string             `json:"created_by"`
	CreatedAt       time.Time          `json:"created_at"`
	UpdatedAt       time.Time          `json:"updated_at"`
	ExpiresAt       *time.Time         `json:"expires_at,omitempty"`
	StoppedBy       string             `json:"stopped_by,omitempty"`
	StoppedAt       *time.Time         `json:"stopped_at,omitempty"`

	// Route is the opaque token in the subscription's webhook URL.
	Route string `json:"-"`
	// AccountKey routes an app delivery to the subscription, such as a Slack
	// workspace's team ID.
	AccountKey string `json:"-"`
	// RegistrationID is the provider's ID for what integrations registered.
	RegistrationID  string      `json:"-"`
	EncryptedSecret *SealedData `json:"-"`
	RenewAt         *time.Time  `json:"-"`
	CleanupAt       *time.Time  `json:"-"`
	Version         int64       `json:"-"`
}

// SubscriptionSelector picks the live subscriptions to stop in an
// organization: one, or all of a connection's, a creator's, or a creator's on
// one team. Empty fields select anything.
type SubscriptionSelector struct {
	OrganizationID string
	SubscriptionID string
	ConnectionID   string
	CreatedBy      string
	TeamID         string
}

// SubscriptionFilter narrows a list of an organization's subscriptions.
type SubscriptionFilter struct {
	ConnectionID string
	CreatedBy    string
	// Live keeps only pending and active subscriptions.
	Live bool
}

// Accepts reports whether a subscription asks for a provider event, named
// without the provider: an empty list asks for everything.
func (subscription Subscription) Accepts(event string) bool {
	return len(subscription.Events) == 0 || slices.Contains(subscription.Events, event)
}

// AppWebhook is where a provider that delivers through its app, such as
// Slack, sends one organization app's events, and the secret they are signed
// with.
type AppWebhook struct {
	AppID          string    `json:"app_id"`
	OrganizationID string    `json:"organization_id"`
	Provider       string    `json:"provider"`
	UpdatedBy      string    `json:"updated_by"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`

	Route           string     `json:"-"`
	EncryptedSecret SealedData `json:"-"`
}

// SealedData is an envelope-encrypted value. The plaintext data key is never
// stored; EncryptedDataKey opens only with the same KMS encryption context.
type SealedData struct {
	Version          int    `json:"version"`
	Nonce            []byte `json:"nonce"`
	Ciphertext       []byte `json:"ciphertext"`
	EncryptedDataKey []byte `json:"encrypted_data_key"`
}

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

const (
	// MaxTargetFields and MaxTargetValue bound what a subscription watches.
	MaxTargetFields = 8
	MaxTargetValue  = 256
	// MaxEvents bounds the events a subscription asks for.
	MaxEvents = 50
	// MinWebhookSecret is the shortest app signing secret accepted.
	MinWebhookSecret = 16
	// MaxWebhookSecret is the longest.
	MaxWebhookSecret = 256
)

var (
	targetKeyPattern = regexp.MustCompile(`^[a-z][a-z0-9_]{0,39}$`)
	eventPattern     = regexp.MustCompile(`^[A-Za-z0-9_.:*-]{1,100}$`)
)

// CheckTarget checks a subscription's target and events before a provider
// reads them: a few short string fields, and a bounded list of event names.
// Events come back trimmed, without duplicates, in their original order.
func CheckTarget(target map[string]string, events []string) (map[string]string, []string, error) {
	if len(target) > MaxTargetFields {
		return nil, nil, fmt.Errorf("target has more than %d fields: %w", MaxTargetFields, ErrInvalid)
	}
	checked := make(map[string]string, len(target))
	for _, key := range slices.Sorted(maps.Keys(target)) {
		value := strings.TrimSpace(target[key])
		if !targetKeyPattern.MatchString(key) {
			return nil, nil, fmt.Errorf("target field %q is not a lowercase name: %w", key, ErrInvalid)
		}
		if value == "" || len(value) > MaxTargetValue || strings.ContainsFunc(value, isControl) {
			return nil, nil, fmt.Errorf("target field %s must be 1 to %d printable characters: %w", key, MaxTargetValue, ErrInvalid)
		}
		checked[key] = value
	}
	if len(events) > MaxEvents {
		return nil, nil, fmt.Errorf("a subscription asks for at most %d events: %w", MaxEvents, ErrInvalid)
	}
	cleaned := []string{}
	for _, event := range events {
		event = strings.TrimSpace(event)
		if !eventPattern.MatchString(event) {
			return nil, nil, fmt.Errorf("event %q is not an event name: %w", event, ErrInvalid)
		}
		if !slices.Contains(cleaned, event) {
			cleaned = append(cleaned, event)
		}
	}
	return checked, cleaned, nil
}

// CheckWebhookSecret checks an app's signing secret as an admin gives it.
type ActionStatus string

const (
	ActionRunning   ActionStatus = "running"
	ActionCompleted ActionStatus = "completed"
	ActionFailed    ActionStatus = "failed"
)

// ActionResource is what an action created or changed at the provider.
type ActionResource struct {
	Type string `json:"type"`
	ID   string `json:"id,omitempty"`
	Name string `json:"name,omitempty"`
	URL  string `json:"url,omitempty"`
}

// Action is one action a caller ran with a connection. An action a connection
// already ran under the same idempotency key is never run twice: the first
// outcome answers again.
type Action struct {
	ID              string          `json:"id"`
	OrganizationID  string          `json:"organization_id"`
	ConnectionID    string          `json:"connection_id"`
	AppID           string          `json:"app_id"`
	Provider        string          `json:"provider"`
	Name            string          `json:"action"`
	Scope           string          `json:"scope"`
	TeamID          string          `json:"team_id,omitempty"`
	MemberID        string          `json:"member_id,omitempty"`
	ProviderAccount ProviderAccount `json:"provider_account"`
	// RequestedBy is the member the action ran as.
	RequestedBy string          `json:"requested_by"`
	Status      ActionStatus    `json:"status"`
	Error       string          `json:"error,omitempty"`
	Resource    *ActionResource `json:"resource,omitempty"`
	Result      json.RawMessage `json:"result,omitempty"`
	StartedAt   time.Time       `json:"started_at"`
	EndedAt     *time.Time      `json:"ended_at,omitempty"`

	IdempotencyKey string `json:"-"`
}

const (
	MinIdempotencyKey = 8
	MaxIdempotencyKey = 200
)

// CheckIdempotencyKey checks the key a caller repeats an action under, which
// must be its own: two different actions never share one.
func CheckIdempotencyKey(key string) (string, error) {
	key = strings.TrimSpace(key)
	if len(key) < MinIdempotencyKey || len(key) > MaxIdempotencyKey || strings.ContainsFunc(key, isControl) {
		return "", fmt.Errorf("idempotency_key must be %d to %d printable characters: %w", MinIdempotencyKey, MaxIdempotencyKey, ErrInvalid)
	}
	return key, nil
}

func CheckWebhookSecret(secret string) (string, error) {
	secret = strings.TrimSpace(secret)
	if len(secret) < MinWebhookSecret || len(secret) > MaxWebhookSecret || strings.ContainsFunc(secret, isControl) {
		return "", fmt.Errorf("secret must be %d to %d printable characters: %w", MinWebhookSecret, MaxWebhookSecret, ErrInvalid)
	}
	return secret, nil
}

func isControl(character rune) bool { return character < 0x20 || character == 0x7f }
