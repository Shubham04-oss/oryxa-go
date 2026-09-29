package application

import (
	"context"
	"time"

	"github.com/Shubham04-oss/oryxa-go/apps/internal/authclient"
	"github.com/Shubham04-oss/oryxa-go/apps/internal/domain"
	"github.com/Shubham04-oss/oryxa-go/internal/composio"
)

// Store is what the application needs of apps' own tables. Every write that
// announces something writes its events to the outbox in the same
// transaction, so a change and its event cannot disagree.
type Store interface {
	// CreateConnection records a pending connection, with the hash of the
	// state its browser round trip carries, and writes event. ErrConflict if
	// the account or the state is already recorded.
	CreateConnection(ctx context.Context, connection domain.Connection, stateHash string, event domain.Event) error
	// PendingConnection finds the connection a state hash names while it is
	// still pending. ErrNotFound otherwise.
	PendingConnection(ctx context.Context, stateHash string) (domain.Connection, error)
	// ActivateConnection makes a pending connection active, clears its state
	// and writes event. It answers false, changing nothing, if the
	// connection is no longer pending.
	ActivateConnection(ctx context.Context, id, label string, now time.Time, event domain.Event) (domain.Connection, bool, error)
	// FailConnection marks a pending connection failed, clears its state and
	// writes event. It answers false, writing nothing, if the connection is
	// no longer pending.
	FailConnection(ctx context.Context, id, reason string, now time.Time, event domain.Event) (bool, error)
	// Connection reads one connection of an organization, whatever its
	// status. ErrNotFound for another organization's.
	Connection(ctx context.Context, organizationID, id string) (domain.Connection, error)
	// ConnectionByAccount reads the connection a Composio account backs.
	ConnectionByAccount(ctx context.Context, composioAccountID string) (domain.Connection, error)
	// Connections lists an organization's connections, newest first.
	Connections(ctx context.Context, organizationID string, query domain.ConnectionQuery) ([]domain.Connection, error)
	// ExpireConnection marks an active connection expired and writes event.
	// It answers false if the connection was not active.
	ExpireConnection(ctx context.Context, id, reason string, now time.Time, event domain.Event) (bool, error)
	// RemoveConnection marks a connection and every live trigger on it
	// removed, and writes the events built from them. It answers false,
	// writing nothing, if the connection was already removed.
	RemoveConnection(ctx context.Context, id, reason string, now time.Time, events func(domain.Connection, []domain.Trigger) ([]domain.Event, error)) (bool, error)
	// ConnectionsAwaitingSubscribe answers the active connections owed an
	// auto-subscribe, counting an attempt against each and leasing it until
	// leaseUntil as it claims them, so a second pass cannot claim the same
	// connection while this one still owns it.
	ConnectionsAwaitingSubscribe(ctx context.Context, maxAttempts, limit int, now, leaseUntil time.Time) ([]domain.Connection, error)
	// RefundAutoSubscribeAttempt refunds a claimed attempt that never reached
	// the provider because workspace policy blocked it. The lease and attempt
	// must still match so a stale worker cannot alter a later claim.
	RefundAutoSubscribeAttempt(ctx context.Context, id string, attempt int, leaseUntil time.Time) error
	// MarkSubscribed records that a connection has been subscribed to what
	// its app delivers.
	MarkSubscribed(ctx context.Context, id string, now time.Time) error
	// FailPendingConnectionsBefore fails every pending connection created
	// before cutoff, whose link has long expired, writing an event for each
	// from the connections it is failing, and answers how many.
	FailPendingConnectionsBefore(ctx context.Context, cutoff, now time.Time, events func([]domain.Connection) ([]domain.Event, error)) (int64, error)

	// AuthConfig reads the auth config recorded for a toolkit. ErrNotFound if
	// none is.
	AuthConfig(ctx context.Context, toolkit string) (AuthConfig, error)
	// SaveAuthConfig records a toolkit's auth config unless one already is,
	// and answers whichever is recorded, so two members connecting the same
	// app at once agree on one.
	SaveAuthConfig(ctx context.Context, config AuthConfig) (AuthConfig, error)

	// CreateTrigger records a trigger and writes event. If a live one already
	// has the same Composio trigger id — Composio answers the same instance
	// for the same account, trigger and config — it answers that one
	// instead, with false, and writes nothing.
	CreateTrigger(ctx context.Context, trigger domain.Trigger, event domain.Event) (domain.Trigger, bool, error)
	// Trigger reads one of an organization's triggers.
	Trigger(ctx context.Context, organizationID, id string) (domain.Trigger, error)
	// TriggerByComposioID reads the live trigger a Composio instance backs.
	TriggerByComposioID(ctx context.Context, composioTriggerID string) (domain.Trigger, error)
	// Triggers lists an organization's live triggers, optionally one
	// member's or one connection's, newest first. TriggerQuery.IncludeRemoved
	// also lists what was removed, for a caller that needs a connection's
	// whole history rather than what still delivers.
	Triggers(ctx context.Context, organizationID string, query TriggerQuery) ([]domain.Trigger, error)
	// RemoveTrigger marks a trigger removed and writes event. It answers
	// false, writing nothing, if it already was.
	RemoveTrigger(ctx context.Context, id string, now time.Time, event domain.Event) (bool, error)
	// DisableTrigger marks an active trigger disabled and writes event. It
	// answers false if it was not active.
	DisableTrigger(ctx context.Context, id, reason string, now time.Time, event domain.Event) (bool, error)

	// StartAction records a tool run as running. When the action carries an
	// idempotency key another action of the member's already has, it records
	// nothing and answers that action, with false.
	StartAction(ctx context.Context, action domain.Action) (domain.Action, bool, error)
	// FinishAction records how a running action ended, and writes event
	// when there is one.
	FinishAction(ctx context.Context, action domain.Action, event *domain.Event) error

	// RecordWebhook records a webhook delivery as handled together with the
	// events it caused, in one transaction. It answers false, writing
	// nothing, if the delivery was already handled.
	RecordWebhook(ctx context.Context, eventID, eventType string, now time.Time, events ...domain.Event) (bool, error)
	// RecordTriggerDelivery is RecordWebhook for a delivery a trigger made,
	// counting it against that trigger in the same transaction, so what a
	// trigger says it delivered is exactly what was published for it. A
	// redelivery writes nothing and answers false.
	RecordTriggerDelivery(ctx context.Context, eventID, eventType, triggerID string, now time.Time, event domain.Event) (bool, error)
	// EventHandled and MarkEventHandled deduplicate bus events.
	EventHandled(ctx context.Context, eventID string) (bool, error)
	MarkEventHandled(ctx context.Context, eventID, detailType string, now time.Time) error
	// DeleteHandledEventsBefore forgets handled deliveries and events older
	// than cutoff.
	DeleteHandledEventsBefore(ctx context.Context, cutoff time.Time) (int64, error)
}

// AuthConfig is the Composio auth config apps connects a toolkit with.
type AuthConfig struct {
	Toolkit         string
	AuthConfigID    string
	AuthScheme      string
	ComposioManaged bool
	CreatedAt       time.Time
}

// TriggerQuery narrows a trigger listing.
type TriggerQuery struct {
	MemberID     string
	ConnectionID string
	// IncludeRemoved also lists triggers a member has removed. Auto-subscribe
	// reads a connection's whole history this way, since a removed row is
	// exactly what says the member opted out and must not be re-added.
	IncludeRemoved bool
}

// Directory is auth, which decides who the caller is.
type Directory interface {
	Membership(ctx context.Context, organizationID string, credentials authclient.Credentials) (domain.OrgMembership, error)
}

// Composio is the part of Composio's API apps uses, so a test answers
// without a network. *composio.Client satisfies it.
type Composio interface {
	Toolkits(ctx context.Context, query composio.ToolkitQuery) (composio.Page[composio.Toolkit], error)
	Toolkit(ctx context.Context, slug string) (composio.Toolkit, error)
	AuthConfigs(ctx context.Context, query composio.AuthConfigQuery) (composio.Page[composio.AuthConfig], error)
	CreateManagedAuthConfig(ctx context.Context, toolkit, name string) (composio.AuthConfig, error)
	CreateUserSuppliedAuthConfig(ctx context.Context, toolkit, scheme, name string) (composio.AuthConfig, error)
	CreateConnectLink(ctx context.Context, request composio.LinkRequest) (composio.ConnectLink, error)
	ConnectedAccount(ctx context.Context, id string) (composio.ConnectedAccount, error)
	DeleteConnectedAccount(ctx context.Context, id string, revoke bool) error
	Tools(ctx context.Context, query composio.ToolQuery) (composio.Page[composio.Tool], error)
	Tool(ctx context.Context, slug string) (composio.Tool, error)
	ExecuteTool(ctx context.Context, slug string, request composio.ExecuteRequest) (composio.ExecuteResult, error)
	Proxy(ctx context.Context, request composio.ProxyRequest) (composio.ProxyResponse, error)
	TriggerTypes(ctx context.Context, query composio.TriggerTypeQuery) (composio.Page[composio.TriggerType], error)
	UpsertTrigger(ctx context.Context, slug string, subscription composio.TriggerSubscription) (string, error)
	DeleteTrigger(ctx context.Context, id string) error
}
