// Package v1 contains the stable version-one events emitted by integrations:
// what providers deliver to subscriptions, what actions run with connections
// did, and the life of each subscription.
//
// Every provider shares one detail type per kind of event. A provider event is
// integrations.trigger.received.v1 whichever provider sent it, and names what
// happened in Event, such as github.issues.opened, so a rule selects events by
// detail.data.provider and detail.data.event. Events never carry a token,
// secret, signature or credential.
//
// Scope is where the connection behind the subscription hangs: member, team or
// organization, as in connections. CreatedBy is the member the subscription
// belongs to, and whom work a trigger starts runs as; integrations asks
// connections for that member's credential.
//
// A delivery a provider repeats is published once, but delivery to a consumer
// is at least once, as everywhere on the bus: deduplicate on the envelope's
// event_id before acting on a trigger.
//
// The action events are defined but not published yet: the tools that call
// providers are not built, so nothing emits ActionCompleted or ActionFailed.
// Everything else here is published today.
package v1

import (
	"encoding/json"
	"time"
)

const (
	Source = "oryxa.integrations"

	TriggerReceivedDetailType     = "integrations.trigger.received.v1"
	ActionCompletedDetailType     = "integrations.action.completed.v1"
	ActionFailedDetailType        = "integrations.action.failed.v1"
	SubscriptionCreatedDetailType = "integrations.subscription.created.v1"
	SubscriptionFailedDetailType  = "integrations.subscription.failed.v1"
	SubscriptionStoppedDetailType = "integrations.subscription.stopped.v1"
)

// MaxPayloadBytes is the most of a provider's payload, or an action's result,
// that an event carries. EventBridge takes events of at most 256 KiB, so a
// larger one is left out and flagged; its Resource still names what to fetch.
const MaxPayloadBytes = 128 << 10

type ProviderAccount struct {
	ID   string `json:"id"`
	Name string `json:"name,omitempty"`
}

// Resource is the provider object an event or action is about, in the
// provider's terms: an issue, with its ID, title and page.
type Resource struct {
	Type string `json:"type"`
	ID   string `json:"id,omitempty"`
	Name string `json:"name,omitempty"`
	URL  string `json:"url,omitempty"`
}

// Actor is who did something at the provider, by the provider's ID for them.
type Actor struct {
	ID   string `json:"id"`
	Name string `json:"name,omitempty"`
}

// TriggerReceived is one event a provider delivered to a subscription. Events
// a subscription does not ask for are not published, and a delivery the
// provider repeats is published once.
type TriggerReceived struct {
	SubscriptionID string `json:"subscription_id"`
	OrganizationID string `json:"organization_id"`
	ConnectionID   string `json:"connection_id"`
	AppID          string `json:"app_id"`
	Provider       string `json:"provider"`
	// Event is the provider's name for what happened, after the provider's
	// name: github.issues.opened, slack.message, linear.issue.create,
	// microsoft.message.created.
	Event           string          `json:"event"`
	Scope           string          `json:"scope"`
	TeamID          string          `json:"team_id,omitempty"`
	MemberID        string          `json:"member_id,omitempty"`
	ProviderAccount ProviderAccount `json:"provider_account"`
	// CreatedBy is the member whose subscription this is. Work the trigger
	// starts runs as them, and stops when they lose the connection.
	CreatedBy string   `json:"created_by"`
	Resource  Resource `json:"resource"`
	Actor     *Actor   `json:"actor,omitempty"`
	// DeliveryID is the provider's ID for the delivery, when it gives one.
	DeliveryID string    `json:"delivery_id,omitempty"`
	OccurredAt time.Time `json:"occurred_at"`
	ReceivedAt time.Time `json:"received_at"`
	// Payload is what the provider sent about the event, as it sent it, when it
	// fits MaxPayloadBytes. PayloadOmitted says it did not.
	Payload        json.RawMessage `json:"payload,omitempty"`
	PayloadOmitted bool            `json:"payload_omitted,omitempty"`
}

// ActionCompleted is an action a service ran with a connection that the
// provider carried out.
type ActionCompleted struct {
	ActionID       string `json:"action_id"`
	OrganizationID string `json:"organization_id"`
	ConnectionID   string `json:"connection_id"`
	AppID          string `json:"app_id"`
	Provider       string `json:"provider"`
	// Action is the provider operation, after the provider's name:
	// github.issues.create, slack.chat.postMessage.
	Action          string          `json:"action"`
	Scope           string          `json:"scope"`
	TeamID          string          `json:"team_id,omitempty"`
	MemberID        string          `json:"member_id,omitempty"`
	ProviderAccount ProviderAccount `json:"provider_account"`
	// Service is the Oryxa service that ran the action, and RequestedBy the
	// member it ran for, when a member asked.
	Service     string    `json:"service"`
	RequestedBy string    `json:"requested_by,omitempty"`
	Resource    *Resource `json:"resource,omitempty"`
	// Result is what the provider answered, when it fits MaxPayloadBytes.
	Result        json.RawMessage `json:"result,omitempty"`
	ResultOmitted bool            `json:"result_omitted,omitempty"`
	StartedAt     time.Time       `json:"started_at"`
	CompletedAt   time.Time       `json:"completed_at"`
}

// ActionFailed is an action that did not complete. Error is one of the
// ActionError values; the provider's answer is never included.
type ActionFailed struct {
	ActionID        string          `json:"action_id"`
	OrganizationID  string          `json:"organization_id"`
	ConnectionID    string          `json:"connection_id"`
	AppID           string          `json:"app_id"`
	Provider        string          `json:"provider"`
	Action          string          `json:"action"`
	Scope           string          `json:"scope"`
	TeamID          string          `json:"team_id,omitempty"`
	MemberID        string          `json:"member_id,omitempty"`
	ProviderAccount ProviderAccount `json:"provider_account"`
	Service         string          `json:"service"`
	RequestedBy     string          `json:"requested_by,omitempty"`
	Resource        *Resource       `json:"resource,omitempty"`
	Error           string          `json:"error"`
	// Retryable says the same action may succeed if run again unchanged.
	Retryable bool      `json:"retryable"`
	StartedAt time.Time `json:"started_at"`
	FailedAt  time.Time `json:"failed_at"`
}

// SubscriptionCreated is a subscription the provider now delivers to.
type SubscriptionCreated struct {
	SubscriptionID  string          `json:"subscription_id"`
	OrganizationID  string          `json:"organization_id"`
	ConnectionID    string          `json:"connection_id"`
	AppID           string          `json:"app_id"`
	Provider        string          `json:"provider"`
	Scope           string          `json:"scope"`
	TeamID          string          `json:"team_id,omitempty"`
	MemberID        string          `json:"member_id,omitempty"`
	ProviderAccount ProviderAccount `json:"provider_account"`
	// Target is what the subscription watches, in the provider's terms, such
	// as {"repository": "acme/api"}. Events are the provider events it asks
	// for; empty means every event the target sends.
	Target    map[string]string `json:"target"`
	Events    []string          `json:"events"`
	CreatedBy string            `json:"created_by"`
	CreatedAt time.Time         `json:"created_at"`
	// ExpiresAt is when the provider stops delivering unless the subscription
	// is renewed, which integrations does before then.
	ExpiresAt *time.Time `json:"expires_at,omitempty"`
}

// SubscriptionFailed is a subscription the provider no longer delivers to and
// integrations cannot restore. Someone must subscribe again.
type SubscriptionFailed struct {
	SubscriptionID string    `json:"subscription_id"`
	OrganizationID string    `json:"organization_id"`
	ConnectionID   string    `json:"connection_id"`
	AppID          string    `json:"app_id"`
	Provider       string    `json:"provider"`
	Scope          string    `json:"scope"`
	TeamID         string    `json:"team_id,omitempty"`
	MemberID       string    `json:"member_id,omitempty"`
	CreatedBy      string    `json:"created_by"`
	Reason         string    `json:"reason"`
	FailedAt       time.Time `json:"failed_at"`
}

// SubscriptionStopped is a subscription that no longer delivers because it
// was stopped. Its registration at the provider is removed after.
type SubscriptionStopped struct {
	SubscriptionID string `json:"subscription_id"`
	OrganizationID string `json:"organization_id"`
	ConnectionID   string `json:"connection_id"`
	AppID          string `json:"app_id"`
	Provider       string `json:"provider"`
	Scope          string `json:"scope"`
	TeamID         string `json:"team_id,omitempty"`
	MemberID       string `json:"member_id,omitempty"`
	CreatedBy      string `json:"created_by"`
	// StoppedBy is who stopped it, when a person did.
	StoppedBy string    `json:"stopped_by,omitempty"`
	Reason    string    `json:"reason"`
	StoppedAt time.Time `json:"stopped_at"`
}

const (
	ActionErrorInvalidRequest      = "invalid_request"
	ActionErrorNotFound            = "not_found"
	ActionErrorForbidden           = "forbidden"
	ActionErrorCredentialRejected  = "credential_rejected"
	ActionErrorNeedsReconnect      = "needs_reconnect"
	ActionErrorScopeRequired       = "scope_required"
	ActionErrorRateLimited         = "rate_limited"
	ActionErrorProviderUnavailable = "provider_unavailable"
	ActionErrorProviderError       = "provider_error"

	// SubscriptionFailedReasonExpired: the provider's registration lapsed
	// before integrations could renew it.
	SubscriptionFailedReasonExpired = "expired"
	// SubscriptionFailedReasonProviderRejected: the provider refused to
	// renew the registration.
	SubscriptionFailedReasonProviderRejected = "provider_rejected"
	// SubscriptionFailedReasonProviderRemoved: the provider removed the
	// registration itself.
	SubscriptionFailedReasonProviderRemoved = "provider_removed"

	SubscriptionStoppedReasonStopped             = "stopped"
	SubscriptionStoppedReasonDisconnected        = "disconnected"
	SubscriptionStoppedReasonMemberLeft          = "member_left"
	SubscriptionStoppedReasonTeamMemberLeft      = "team_member_left"
	SubscriptionStoppedReasonOrganizationDeleted = "organization_deleted"
)
