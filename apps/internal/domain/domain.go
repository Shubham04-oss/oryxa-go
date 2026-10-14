// Package domain is what apps is about: a member's connections to
// third-party apps, the triggers subscribed on them, and the tools run in
// them, independent of Composio's own shapes and of how any of it is stored.
package domain

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
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
	// ErrChooseConnection guards legacy data with several active connections.
	// New connections enforce one current account per member and app; this
	// sentinel stays for safe failure and a future explicit assignment model.
	ErrChooseConnection = errors.New("several connections could be meant")
	// ErrNeedsReconnect means the connection exists but can no longer act:
	// it expired, or Composio refuses it.
	ErrNeedsReconnect = errors.New("the connection needs reconnecting")
	// ErrNotReadOnly means a call that may only read named a tool Composio
	// does not mark as read-only.
	ErrNotReadOnly = errors.New("the tool is not read-only")
	// ErrNotConnectable means the app cannot be connected with what this
	// project holds, such as an OAuth app Composio has no app of its own for,
	// or a toolkit Oryxa's own sign-in is not switched on for.
	ErrNotConnectable = errors.New("the app cannot be connected here")
	// ErrInvalidCredentials means a key, a password or a field a member or an
	// admin typed broke its rule, or the app refused it when it was tried.
	// InvalidCredentials names the field; nothing ever quotes its value.
	ErrInvalidCredentials = errors.New("invalid credentials")
)

// InvalidCredentials is ErrInvalidCredentials about what a member or an admin
// typed. Field names the field to blame, when one is, and Rule says what it
// must be. Neither ever quotes a value, so it is safe to log and to answer.
type InvalidCredentials struct {
	Field string
	Rule  string
}

func (e *InvalidCredentials) Error() string {
	switch {
	case e.Field != "" && e.Rule != "":
		return e.Field + " " + e.Rule
	case e.Field != "":
		return e.Field + " was refused"
	case e.Rule == "":
		return "the app refused the credential"
	case strings.HasPrefix(e.Rule, "the "):
		return e.Rule
	default:
		return "the credential " + e.Rule
	}
}

// Unwrap answers both kinds it is: ErrInvalidCredentials, and ErrInvalid for
// a caller that only knows that one.
func (e *InvalidCredentials) Unwrap() []error { return []error{ErrInvalidCredentials, ErrInvalid} }

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
	// AgentID and AgentRole name which of the member's agents it is (agt_…,
	// and primary, specialist or organization), when Agent is set.
	AgentID   string
	AgentRole string
	// AgentGrant is what a specialist's token may reach (I3); nil is the
	// default grant, every app, read-only.
	AgentGrant *AgentGrant
}

// AgentGrant is what a member lets a specialist reach in their apps, as
// agents' runprofile.Grant names it and auth carries it on the token.
type AgentGrant struct {
	// Apps are the toolkits it may reach, or "*" for every one.
	Apps []string `json:"apps"`
	// Tools, when any, are the only tools it may run, with WriteTools.
	Tools []string `json:"tools"`
	// WriteApps and WriteTools are where it may change things.
	WriteApps  []string `json:"write_apps"`
	WriteTools []string `json:"write_tools"`
}

// MayReach refuses a specialist an app its grant does not name. Every other
// caller reaches every app its member does.
func (m OrgMembership) MayReach(toolkit string) error {
	if !m.specialist() {
		return nil
	}
	grant := m.grant()
	if !slices.Contains(grant.Apps, "*") && !slices.ContainsFunc(grant.Apps, equalFold(toolkit)) {
		return fmt.Errorf("this agent has not been given %s; its owner can grant it in the agent's settings: %w", toolkit, ErrForbidden)
	}
	return nil
}

// MayRun refuses a specialist a tool its grant does not allow: one outside
// its apps or tools, or a write it was not given.
func (m OrgMembership) MayRun(toolkit, tool string, readOnly bool) error {
	if !m.specialist() {
		return nil
	}
	if err := m.MayReach(toolkit); err != nil {
		return err
	}
	grant := m.grant()
	if len(grant.Tools) > 0 && !slices.ContainsFunc(grant.Tools, equalFold(tool)) && !slices.ContainsFunc(grant.WriteTools, equalFold(tool)) {
		return fmt.Errorf("this agent has not been given %s; its owner can grant it in the agent's settings: %w", tool, ErrForbidden)
	}
	if !readOnly && !slices.ContainsFunc(grant.WriteApps, equalFold(toolkit)) && !slices.ContainsFunc(grant.WriteTools, equalFold(tool)) {
		return fmt.Errorf("%s changes things, and this agent may only read %s; its owner can grant it writes in the agent's settings: %w", tool, toolkit, ErrForbidden)
	}
	return nil
}

func (m OrgMembership) specialist() bool { return m.Agent && m.AgentRole == "specialist" }

func (m OrgMembership) grant() AgentGrant {
	if m.AgentGrant == nil {
		return AgentGrant{Apps: []string{"*"}}
	}
	return *m.AgentGrant
}

func equalFold(want string) func(string) bool {
	return func(value string) bool { return strings.EqualFold(value, want) }
}

// Brokers: who holds a connection's credential.
const (
	// BrokerComposio is Composio's hosted flow: Composio holds the credential
	// and runs the calls. It is every connection made before Oryxa's own
	// sign-in, and the default.
	BrokerComposio = "composio"
	// BrokerOryxa is Oryxa's own sign-in: apps keeps the credential, sealed,
	// and a toolkit takes this route only once APPS_ORYXA_TOOLKITS names it.
	BrokerOryxa = "oryxa"
	// BrokerPipedream is Pipedream Connect: Pipedream holds the credential and
	// runs the calls, for the toolkits neither Oryxa nor Composio serves.
	BrokerPipedream = "pipedream"
)

// CheckBroker normalizes and checks a broker; empty is Composio.
func CheckBroker(broker string) (string, error) {
	switch broker = strings.ToLower(strings.TrimSpace(broker)); broker {
	case "", BrokerComposio:
		return BrokerComposio, nil
	case BrokerOryxa, BrokerPipedream:
		return broker, nil
	}
	return "", errors.Join(ErrInvalid, errors.New("a broker is composio, oryxa or pipedream"))
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
// Composio holds the credential, or for an Oryxa connection apps keeps it
// sealed beside this; this holds whose it is.
type Connection struct {
	ID             string
	OrganizationID string
	MemberID       string
	Toolkit        string
	// Broker is who holds the credential: BrokerComposio, BrokerOryxa or
	// BrokerPipedream. An empty Broker is Composio's.
	Broker string
	// CredentialKind is the kind of credential an Oryxa connection holds
	// (oauth2_code, api_token, basic, oauth2_client_credentials), once it
	// holds one; empty otherwise.
	CredentialKind string
	AuthConfigID   string
	// ComposioUserID is the user Composio files the account under, one per
	// member per organization, so a member's connections in one organization
	// can never be reached from another.
	ComposioUserID string
	// ComposioAccountID is Composio's connected account, known once a link
	// was created.
	ComposioAccountID string
	// PipedreamAccountID is Pipedream's account, known once a Pipedream
	// connection finished connecting.
	PipedreamAccountID string
	Status             ConnectionStatus
	StatusReason       string
	// Label identifies the connected account to the member, such as
	// github_red-castle.
	Label string
	// ReturnTo is where the member's browser goes once they finish.
	ReturnTo string
	// AutoSubscribe says this connection should be subscribed to everything
	// its app can deliver, once it is active. AutoSubscribedAt is when that
	// finished; unset on an active connection that wants it means it is
	// still owed - or that auto-subscribe has given up after
	// AutoSubscribeAttempts failed passes, which AutoSubscribeAttempts
	// reaching the service's limit is what says, since a warning was also
	// logged when it happened.
	AutoSubscribe         bool
	AutoSubscribedAt      *time.Time
	AutoSubscribeAttempts int
	CreatedAt             time.Time
	UpdatedAt             time.Time
	ConnectedAt           *time.Time
	RemovedAt             *time.Time
}

// Oryxa says the connection was made through Oryxa's own sign-in, so none of
// Composio's paths may act through it.
func (connection Connection) Oryxa() bool { return connection.Broker == BrokerOryxa }

// Composio says Composio holds the connection's credential, which is the only
// case in which a Composio path may act through it. Guarding Composio's paths
// on this rather than on !Oryxa() keeps every other broker out of them.
func (connection Connection) Composio() bool {
	return connection.Broker == "" || connection.Broker == BrokerComposio
}

// Pipedream says Pipedream Connect holds the connection's credential.
func (connection Connection) Pipedream() bool { return connection.Broker == BrokerPipedream }

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
// differs per organization, so nothing crosses between them. Pipedream files
// the member's accounts under the same id, as its external user.
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
	// PipedreamTriggerID is the deployed Pipedream trigger behind a trigger
	// on a Pipedream connection.
	PipedreamTriggerID string
	Status             TriggerStatus
	StatusReason       string
	// Kind is what the catalog said when this was subscribed: webhook, which
	// the app pushes, or poll, which arrives on Composio's own interval.
	Kind string
	// ManualSetup says the app itself still needs a webhook URL registered by
	// hand, so this subscription delivers nothing until a member does that.
	ManualSetup bool
	// DeliveryCount and LastDeliveryAt are how a member, or an operator, can
	// tell a live trigger from one that has never delivered anything.
	DeliveryCount  int64
	LastDeliveryAt *time.Time
	CreatedAt      time.Time
	UpdatedAt      time.Time
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
	AgentID        string
	AgentRole      string
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
	// TraceContext is the outbox row's stored trace, carried through from
	// outbox.Event so Publisher can link its producer span to the write that
	// created this event. Code building an Event to record leaves it unset:
	// the write path captures the current trace itself (see insertEvents),
	// rather than trusting a caller to have filled this in.
	TraceContext string
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
