package hooks

import (
	"context"
	"time"

	"github.com/Shubham04-oss/oryxa-go/apps/internal/domain"
	"github.com/Shubham04-oss/oryxa-go/apps/internal/integration"
	"github.com/Shubham04-oss/oryxa-go/internal/secrets"
)

// Store keeps endpoints and records deliveries. apps' postgres.Store is one.
// A missing endpoint is domain.ErrNotFound, and one that breaks a uniqueness
// rule (a second active deployment endpoint for a toolkit, say) is
// domain.ErrConflict.
type Store interface {
	CreateHookEndpoint(ctx context.Context, endpoint Endpoint) error
	HookEndpoint(ctx context.Context, id string) (Endpoint, error)
	// ActiveHookEndpoint is the active organization or deployment endpoint
	// for a toolkit. A deployment endpoint's organization is "".
	ActiveHookEndpoint(ctx context.Context, scope, organizationID, toolkit string) (Endpoint, error)
	HookEndpointsForConnection(ctx context.Context, connectionID string) ([]Endpoint, error)
	// SetHookEndpointRemote records the app's id for the subscription and
	// when it lapses.
	SetHookEndpointRemote(ctx context.Context, id, remoteID string, expiresAt *time.Time, now time.Time) error
	// RotateHookSecret makes sealed the endpoint's secret, keeping the one it
	// replaces as the previous until previousUntil. It answers false, and
	// changes nothing, unless the endpoint still has a secret at version.
	RotateHookSecret(ctx context.Context, id string, version int, sealed secrets.SealedData, previousUntil, now time.Time) (bool, error)
	// CaptureHookSecret keeps the secret an app sent, only while the
	// endpoint has none. It answers whether it did.
	CaptureHookSecret(ctx context.Context, id string, sealed secrets.SealedData, now time.Time) (bool, error)
	// MarkHookSecretRevealed records that a captured secret was shown, only
	// the first time. It answers whether this was the first time.
	MarkHookSecretRevealed(ctx context.Context, id string, now time.Time) (bool, error)
	// MarkHookValidated records the endpoint's first handshake, only for an
	// endpoint made after createdAfter that has not validated before. It
	// answers whether it did.
	MarkHookValidated(ctx context.Context, id string, createdAfter, now time.Time) (bool, error)
	// ReplaceHookEndpoint stores replacement and marks the active endpoint
	// oldID replaced by it, in one transaction. It is domain.ErrConflict,
	// and stores nothing, when oldID is not active.
	ReplaceHookEndpoint(ctx context.Context, oldID string, replacement Endpoint) error
	// DeleteHookEndpoint deletes an endpoint. An endpoint it had replaced is
	// active again, since whatever was meant to take over from it is gone.
	DeleteHookEndpoint(ctx context.Context, id string) (bool, error)
	// DeleteHookEndpointsForConnection deletes a connection's endpoints and
	// answers them, for their subscriptions to be removed at the app.
	DeleteHookEndpointsForConnection(ctx context.Context, connectionID string) ([]Endpoint, error)
	// HookEndpointsExpiring answers active endpoints that lapse by before,
	// soonest first.
	HookEndpointsExpiring(ctx context.Context, before time.Time, limit int) ([]Endpoint, error)
	// DeleteExpiredHookEndpoints deletes endpoints that have lapsed, and
	// forgets previous secrets whose overlap has ended.
	DeleteExpiredHookEndpoints(ctx context.Context, now time.Time) (int64, error)
	// HookDeliveryHandled says whether a delivery's dedupe key is recorded.
	HookDeliveryHandled(ctx context.Context, key string) (bool, error)
	// RecordHookDelivery records a delivery and its events in one
	// transaction: the delivery's dedupe mark, and for each event not
	// already recorded under its own key, the mark, the trigger's tally and
	// the outbox row. It answers false, and writes nothing, when the
	// delivery's mark was already there.
	RecordHookDelivery(ctx context.Context, delivery Delivery) (bool, error)
}

// Delivery is what one verified delivery became, for the store to record.
type Delivery struct {
	// Key dedupes the delivery: hook:{endpoint}:{delivery id}.
	Key        string
	ReceivedAt time.Time
	Events     []DeliveredEvent
}

// DeliveredEvent is one event for one subscription, as the outbox carries it.
type DeliveredEvent struct {
	// Key dedupes the event for its trigger, across deliveries:
	// trigger:{trigger id}:{event id}. It is "" when the app gave the event
	// no id, and then only the delivery's own key dedupes it.
	Key       string
	TriggerID string
	// Event is apps.trigger.received.v1.
	Event domain.Event
}

// Sealer seals and opens endpoint secrets. internal/secrets.Envelope, bound
// to the service oryxa-apps, is one.
type Sealer interface {
	Seal(ctx context.Context, binding secrets.Binding, plaintext []byte) (secrets.SealedData, error)
	Open(ctx context.Context, binding secrets.Binding, sealed secrets.SealedData) ([]byte, error)
}

// Integrations finds a toolkit's integration: its manifest, to make
// endpoints, and its Pusher, to parse what they receive.
// *integration.Registry is one.
type Integrations interface {
	Lookup(toolkit string) (integration.Integration, bool)
}

// Subscriptions answers which of apps' trigger subscriptions a verified event
// is for. apps implements it over its connections and triggers, and answers
// only live subscriptions made through Oryxa's own integrations, on active
// connections. hooks checks every answer against the delivery's reach, and
// drops any that falls outside it.
type Subscriptions interface {
	// ForEndpoint answers the subscriptions to trigger delivered through a
	// connection endpoint. All of them are on the endpoint's own connection.
	// A replaced endpoint is asked about too, while a renewal overlaps.
	ForEndpoint(ctx context.Context, endpoint Endpoint, trigger string) ([]Subscription, error)
	// ForAccount answers the subscriptions to trigger on toolkit's
	// connections whose account ref is accountRef: the member's own
	// connection, never another member's that can merely see the same thing.
	// organizationID narrows it to one organization when it is not "".
	ForAccount(ctx context.Context, toolkit, organizationID, accountRef, trigger string) ([]Subscription, error)
}

// Subscription is one member's subscription to one trigger, as an event is
// delivered to it.
type Subscription struct {
	TriggerID      string
	ConnectionID   string
	OrganizationID string
	MemberID       string
	Toolkit        string
	Trigger        string
	// AccountRef is the connection's own account ref, the one ForAccount
	// matched. ForEndpoint may leave it "".
	AccountRef string
}
