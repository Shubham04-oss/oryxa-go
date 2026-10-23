package application

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/Shubham04-oss/oryxa-go/apps/internal/authclient"
	"github.com/Shubham04-oss/oryxa-go/apps/internal/domain"
	"github.com/Shubham04-oss/oryxa-go/internal/composio"
	"github.com/Shubham04-oss/oryxa-go/internal/secrets"
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
	// ActivatePipedreamConnection is ActivateConnection for a Pipedream
	// connection, recording the account it was read back as. ErrConflict if
	// another connection already records that account.
	ActivatePipedreamConnection(ctx context.Context, id, accountID, label string, now time.Time, event domain.Event) (domain.Connection, bool, error)
	// PipedreamConnections pages through every organization's active
	// Pipedream connections that have an account, in id order after afterID.
	PipedreamConnections(ctx context.Context, afterID string, limit int) ([]domain.Connection, error)
	// FailConnection marks a pending connection failed, clears its state and
	// writes event. It answers false, writing nothing, if the connection is
	// no longer pending.
	FailConnection(ctx context.Context, id, reason string, now time.Time, event domain.Event) (bool, error)
	// Connection reads one connection of an organization, whatever its
	// status. ErrNotFound for another organization's.
	Connection(ctx context.Context, organizationID, id string) (domain.Connection, error)
	// ApplyNativeConnection records what the integrations service reported
	// of a connection; see postgres.Store.ApplyNativeConnection for the order
	// it applies reports in.
	ApplyNativeConnection(ctx context.Context, connection domain.Connection, at time.Time) error
	// NativeOrganizations lists, in order, the organizations with any native
	// connection row, removed ones included: the ones reconciling looks at.
	NativeOrganizations(ctx context.Context) ([]string, error)
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
	// CreatePipedreamTrigger records a trigger deployed at Pipedream, with its
	// sealed signing key, and writes event. If a live one already exists for
	// the same connection, event and config it answers that one instead, with
	// false, and writes nothing.
	CreatePipedreamTrigger(ctx context.Context, trigger domain.Trigger, sealedKey []byte, event domain.Event) (domain.Trigger, bool, error)
	// PipedreamTrigger reads a trigger deployed at Pipedream by apps' own id,
	// in any organization and whatever its status, with its sealed signing
	// key. ErrNotFound for an id that is not a Pipedream trigger.
	PipedreamTrigger(ctx context.Context, id string) (domain.Trigger, []byte, error)
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

// Pipedream is the part of Pipedream Connect apps uses, for the apps neither
// Oryxa nor Composio serves. Its types are apps' own rather than a client
// package's, so the client can change without this layer noticing. A nil
// Pipedream means the deployment has none: no app is routed to it, and a
// connect an override sends to it is refused as not connectable.
//
// externalUserID is the user Pipedream files a member's accounts under, one
// per member per organization, as Composio's user is.
//
// An implementation answers an error that errors.Is domain.ErrNotFound for
// something Pipedream does not have, so deleting what is already gone can be
// told from a failure.
type Pipedream interface {
	// CreateConnectToken starts a member's connect: the short-lived token and
	// the link their browser opens to finish it.
	CreateConnectToken(ctx context.Context, request PipedreamTokenRequest) (PipedreamConnectToken, error)
	// ListAccounts lists a user's accounts, of one app when app is not "".
	ListAccounts(ctx context.Context, externalUserID, app string) ([]PipedreamAccount, error)
	GetAccount(ctx context.Context, id string) (PipedreamAccount, error)
	DeleteAccount(ctx context.Context, id string) error
	// DeleteUser deletes a user and every account and trigger of theirs.
	DeleteUser(ctx context.Context, externalUserID string) error
	// ListApps searches Pipedream's apps; an empty query lists them.
	ListApps(ctx context.Context, query string) ([]PipedreamApp, error)
	ListActions(ctx context.Context, app string) ([]PipedreamComponent, error)
	RunAction(ctx context.Context, request PipedreamRunRequest) (PipedreamRunResult, error)
	ListTriggers(ctx context.Context, app string) ([]PipedreamComponent, error)
	DeployTrigger(ctx context.Context, request PipedreamDeployRequest) (PipedreamDeployedTrigger, error)
	DeleteTrigger(ctx context.Context, externalUserID, id string) error
	// Proxy makes a request to the app as the account, with Pipedream adding
	// the account's credential.
	Proxy(ctx context.Context, request PipedreamProxyRequest) (PipedreamProxyResponse, error)
}

// Sealer seals and opens one secret at a time: secrets.Envelope, bound to
// service oryxa-apps.
type Sealer interface {
	Seal(ctx context.Context, binding secrets.Binding, plaintext []byte) (secrets.SealedData, error)
	Open(ctx context.Context, binding secrets.Binding, sealed secrets.SealedData) ([]byte, error)
}

// PipedreamTokenRequest is what a connect token is made for.
type PipedreamTokenRequest struct {
	ExternalUserID string
	// App is the app the link opens on.
	App string
	// SuccessRedirectURI and ErrorRedirectURI are where the member's browser
	// goes once they finish.
	SuccessRedirectURI string
	ErrorRedirectURI   string
	// WebhookURI is told when the account was made or failed.
	WebhookURI string
	// ExpiresIn is how long the token lives; zero is Pipedream's default.
	ExpiresIn time.Duration
}

type PipedreamConnectToken struct {
	Token          string
	ConnectLinkURL string
	ExpiresAt      time.Time
}

type PipedreamAccount struct {
	ID             string
	ExternalUserID string
	App            string
	// Name identifies the account to the member, such as their email.
	Name string
	// Healthy is false once Pipedream can no longer renew the credential;
	// Dead says it has given up on the account for good.
	Healthy   bool
	Dead      bool
	CreatedAt time.Time
	UpdatedAt time.Time
}

type PipedreamApp struct {
	// Slug is Pipedream's name_slug, which is what apps calls a toolkit.
	Slug        string
	Name        string
	Description string
	Logo        string
	AuthType    string
	Categories  []string
}

// PipedreamComponent is an action or a trigger, as Pipedream registers it.
type PipedreamComponent struct {
	// Key is the component's own name, such as slack-send-message.
	Key         string
	Name        string
	Description string
	Version     string
	// Props are the component's props, as Pipedream describes them.
	Props json.RawMessage
	// ReadOnlyHint and DestructiveHint are the component's own annotations,
	// nil when it declares none. They are kept as declared, not defaulted,
	// because only an explicit read-only mark lets an action run unconfirmed.
	ReadOnlyHint    *bool
	DestructiveHint *bool
}

type PipedreamRunRequest struct {
	ExternalUserID  string
	ComponentKey    string
	ConfiguredProps map[string]any
}

// PipedreamActionError is what RunAction answers when the action ran and
// failed, in its own code or at the app, as opposed to Pipedream itself
// failing. Its message is the app's, which a caller needs to try again
// differently. RunAction answers an error wrapping domain.ErrRateLimited when
// Pipedream itself is rate limiting.
type PipedreamActionError struct {
	Message string
	// Origin is where Pipedream attributes the failure, such as upstream_api.
	Origin string
	// UpstreamStatus is what the app's API answered on the action's last
	// call, when there was one; 429 means the app is rate limiting.
	UpstreamStatus int
}

func (err *PipedreamActionError) Error() string {
	return "the pipedream action failed (" + err.Origin + "): " + err.Message
}

// PipedreamRunResult is what an action exported and returned.
type PipedreamRunResult struct {
	Exports json.RawMessage
	Return  json.RawMessage
}

type PipedreamDeployRequest struct {
	ExternalUserID  string
	ComponentKey    string
	ConfiguredProps map[string]any
	// WebhookURL is where the deployed trigger delivers its events.
	WebhookURL string
	// Timer is how often a polling trigger polls; zero keeps its own.
	Timer time.Duration
}

type PipedreamDeployedTrigger struct {
	ID string
	// SigningKey signs what the trigger delivers. It is a secret: apps keeps
	// it sealed and never logs it.
	SigningKey []byte
}

type PipedreamProxyRequest struct {
	ExternalUserID string
	AccountID      string
	Method         string
	URL            string
	Header         http.Header
	Body           []byte
}

type PipedreamProxyResponse struct {
	Status int
	Header http.Header
	Body   []byte
}
