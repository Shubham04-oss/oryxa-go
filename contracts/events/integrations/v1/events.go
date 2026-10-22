// Package v1 holds the event contracts the integrations service publishes on
// EventBridge. Every Oryxa publisher puts the same envelope around one of
// these details: event_id, version, occurred_at and data.
//
// A connection's events carry who it belongs to and its lifecycle state,
// never a token, a scope's grant or the app account's data.
//
// Events arrive in no particular order and may arrive twice. A consumer keeps
// the newest by Connection.UpdatedAt and ignores an older one; removed is
// final, so nothing after it brings a connection back.
package v1

import "time"

// Source identifies the integrations service on the bus.
const Source = "oryxa.integrations"

const (
	// ConnectionCreatedDetailType is published when a member (or an admin,
	// for an org-level connection) finishes signing in to an app.
	ConnectionCreatedDetailType = "integrations.connection.created.v1"
	// ConnectionUpdatedDetailType is published when a connection's status
	// changes: it needs a new sign-in, degrades, fails or recovers.
	ConnectionUpdatedDetailType = "integrations.connection.updated.v1"
	// ConnectionRemovedDetailType is published once a connection is revoked,
	// by its member, an admin, or because its member left the organization.
	ConnectionRemovedDetailType = "integrations.connection.removed.v1"
)

// Connection levels, as Connection.Level.
const (
	LevelPersonal = "personal"
	LevelOrg      = "org"
)

// Connection statuses, as Connection.Status.
const (
	StatusOK          = "ok"
	StatusNeedsReauth = "needs_reauth"
	StatusDegraded    = "degraded"
	StatusError       = "error"
	StatusRevoked     = "revoked"
)

// Connection is the detail of every connection event.
type Connection struct {
	ID             string `json:"connection_id"`
	OrganizationID string `json:"organization_id"`
	// MemberID is empty for an org-level connection.
	MemberID string `json:"member_id,omitempty"`
	Level    string `json:"level"`
	// App is the connector's manifest id, such as "github".
	App string `json:"app"`
	// Mode is how it signed in: "byo" (the org's own app), "oryxa" (Oryxa's
	// app) or "user_key" (a key the member pasted).
	Mode   string `json:"mode"`
	Status string `json:"status"`
	// UpdatedAt orders a connection's events; see the package comment.
	UpdatedAt time.Time `json:"updated_at"`
}
