// Package v1 contains the stable version-one events emitted by apps: a
// member's connections to third-party apps coming and going, the triggers
// those apps deliver, and the tools run in them.
//
// Apps connects apps through Composio, which holds every credential. A
// connection belongs to one member in one organization, and everything an
// event describes happened as that member. Events never carry a token, a
// secret or a credential, and never a tool's arguments or results, which can
// hold anything the member's app does. Delivery is at least once, as
// everywhere on the bus: deduplicate on the envelope's event_id.
package v1

import (
	"encoding/json"
	"time"
)

const (
	Source = "oryxa.apps"

	ConnectionInitiatedDetailType = "apps.connection.initiated.v1"
	ConnectionConnectedDetailType = "apps.connection.connected.v1"
	ConnectionFailedDetailType    = "apps.connection.failed.v1"
	ConnectionExpiredDetailType   = "apps.connection.expired.v1"
	ConnectionRemovedDetailType   = "apps.connection.removed.v1"
	TriggerSubscribedDetailType   = "apps.trigger.subscribed.v1"
	TriggerUnsubscribedDetailType = "apps.trigger.unsubscribed.v1"
	TriggerReceivedDetailType     = "apps.trigger.received.v1"
	TriggerDisabledDetailType     = "apps.trigger.disabled.v1"
	ActionCompletedDetailType     = "apps.action.completed.v1"
	ActionFailedDetailType        = "apps.action.failed.v1"
)

// Why a connection never became usable. A connection fails before it is ever
// active, so nothing could have been read or run through it.
const (
	// FailedDeniedAtProvider is the member refusing at the app's own page, or
	// the app refusing them.
	FailedDeniedAtProvider = "denied_at_provider"
	// FailedWrongAccount is a callback naming an account that is not the
	// member's or not the app the connection was started for. Its state is
	// spent, so the same trick cannot be tried against it twice.
	FailedWrongAccount = "wrong_account"
	// FailedAbandoned is a member who started connecting and never came back
	// before the link expired.
	FailedAbandoned = "abandoned"
)

// MaxTriggerDataBytes is the most of a trigger's payload an event carries.
// EventBridge takes events of at most 256 KiB, so a larger payload is left
// out and flagged.
const MaxTriggerDataBytes = 128 << 10

// Why a connection was removed.
const (
	RemovedByMember            = "removed_by_member"
	RemovedByAdmin             = "removed_by_admin"
	RemovedOrganizationDeleted = "organization_deleted"
	RemovedMemberLeft          = "member_left"
	RemovedAtComposio          = "removed_at_composio"
)

// ConnectionInitiated is a member starting to connect an app: the link has
// been made and they have been sent to it. Nothing can act through the
// connection yet, and it may never become usable — every initiated
// connection ends as either ConnectionConnected or ConnectionFailed.
type ConnectionInitiated struct {
	ConnectionID   string    `json:"connection_id"`
	OrganizationID string    `json:"organization_id"`
	MemberID       string    `json:"member_id"`
	Toolkit        string    `json:"toolkit"`
	InitiatedAt    time.Time `json:"initiated_at"`
}

// ConnectionConnected is a member's connection that has just become usable.
type ConnectionConnected struct {
	ConnectionID   string    `json:"connection_id"`
	OrganizationID string    `json:"organization_id"`
	MemberID       string    `json:"member_id"`
	Toolkit        string    `json:"toolkit"`
	ConnectedAt    time.Time `json:"connected_at"`
}

// ConnectionFailed is a connection that never became usable, and why. It was
// never active, so nothing was ever read or run through it. Reason is one of
// the Failed constants — never the app's own wording, which can carry
// anything.
type ConnectionFailed struct {
	ConnectionID   string    `json:"connection_id"`
	OrganizationID string    `json:"organization_id"`
	MemberID       string    `json:"member_id"`
	Toolkit        string    `json:"toolkit"`
	Reason         string    `json:"reason"`
	FailedAt       time.Time `json:"failed_at"`
}

// ConnectionExpired is a connection whose credential can no longer be
// renewed. The member has to connect the app again; until then nothing can
// run as it.
type ConnectionExpired struct {
	ConnectionID   string    `json:"connection_id"`
	OrganizationID string    `json:"organization_id"`
	MemberID       string    `json:"member_id"`
	Toolkit        string    `json:"toolkit"`
	Reason         string    `json:"reason,omitempty"`
	ExpiredAt      time.Time `json:"expired_at"`
}

// ConnectionRemoved is a connection that is gone for good, and its
// credential revoked.
type ConnectionRemoved struct {
	ConnectionID   string    `json:"connection_id"`
	OrganizationID string    `json:"organization_id"`
	MemberID       string    `json:"member_id"`
	Toolkit        string    `json:"toolkit"`
	Reason         string    `json:"reason"`
	RemovedAt      time.Time `json:"removed_at"`
}

// TriggerSubscribed is a member subscribing one of their connections to an
// event its app delivers. Kind says whether the app pushes the event or
// Composio polls for it, and ManualSetup that the app's own webhook still
// has to be registered by hand — until which nothing is delivered at all.
type TriggerSubscribed struct {
	TriggerID      string    `json:"trigger_id"`
	ConnectionID   string    `json:"connection_id"`
	OrganizationID string    `json:"organization_id"`
	MemberID       string    `json:"member_id"`
	Toolkit        string    `json:"toolkit"`
	Trigger        string    `json:"trigger"`
	Kind           string    `json:"kind,omitempty"`
	ManualSetup    bool      `json:"manual_setup,omitempty"`
	SubscribedAt   time.Time `json:"subscribed_at"`
}

// TriggerUnsubscribed is a trigger a member or an org admin took away. A
// trigger that stopped on its own is TriggerDisabled instead, and one that
// went with its connection is covered by ConnectionRemoved.
type TriggerUnsubscribed struct {
	TriggerID      string `json:"trigger_id"`
	ConnectionID   string `json:"connection_id"`
	OrganizationID string `json:"organization_id"`
	MemberID       string `json:"member_id"`
	Toolkit        string `json:"toolkit"`
	Trigger        string `json:"trigger"`
	// ByAdmin says an org admin removed another member's trigger.
	ByAdmin        bool      `json:"by_admin,omitempty"`
	UnsubscribedAt time.Time `json:"unsubscribed_at"`
}

// TriggerReceived is one event a member's app delivered to a trigger they
// subscribed to. Data is the app's own payload, in the trigger's shape; a
// payload over MaxTriggerDataBytes is left out and DataOmitted set.
type TriggerReceived struct {
	TriggerID      string          `json:"trigger_id"`
	ConnectionID   string          `json:"connection_id"`
	OrganizationID string          `json:"organization_id"`
	MemberID       string          `json:"member_id"`
	Toolkit        string          `json:"toolkit"`
	Trigger        string          `json:"trigger"`
	Data           json.RawMessage `json:"data,omitempty"`
	DataOmitted    bool            `json:"data_omitted,omitempty"`
	ReceivedAt     time.Time       `json:"received_at"`
}

// TriggerDisabled is a trigger that stopped delivering, such as because its
// connection expired.
type TriggerDisabled struct {
	TriggerID      string    `json:"trigger_id"`
	ConnectionID   string    `json:"connection_id"`
	OrganizationID string    `json:"organization_id"`
	MemberID       string    `json:"member_id"`
	Toolkit        string    `json:"toolkit"`
	Trigger        string    `json:"trigger"`
	Reason         string    `json:"reason,omitempty"`
	DisabledAt     time.Time `json:"disabled_at"`
}

// ActionCompleted is a tool that ran and succeeded in a member's app. Agent
// says an agent ran it for the member.
type ActionCompleted struct {
	ActionID       string    `json:"action_id"`
	ConnectionID   string    `json:"connection_id"`
	OrganizationID string    `json:"organization_id"`
	MemberID       string    `json:"member_id"`
	Toolkit        string    `json:"toolkit"`
	Tool           string    `json:"tool"`
	ReadOnly       bool      `json:"read_only"`
	Agent          bool      `json:"agent"`
	FinishedAt     time.Time `json:"finished_at"`
}

// ActionFailed is a tool that ran and failed, or that the app refused. Error
// is the reason in a caller's terms, never the app's own response.
type ActionFailed struct {
	ActionID       string    `json:"action_id"`
	ConnectionID   string    `json:"connection_id"`
	OrganizationID string    `json:"organization_id"`
	MemberID       string    `json:"member_id"`
	Toolkit        string    `json:"toolkit"`
	Tool           string    `json:"tool"`
	ReadOnly       bool      `json:"read_only"`
	Agent          bool      `json:"agent"`
	Error          string    `json:"error"`
	FinishedAt     time.Time `json:"finished_at"`
}
