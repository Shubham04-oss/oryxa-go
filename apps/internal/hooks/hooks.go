// Package hooks is apps' one webhook ingress for Oryxa's own integrations,
// and the endpoints their apps deliver to.
//
// An app pushes to POST /v1/apps/hooks/{endpointID}. The id is 120 random
// bits and the only thing in the URL. Each delivery is checked by the scheme
// its toolkit's manifest declared, which is implemented once, here, with a
// constant-time comparison. Nothing in a delivery is used or logged until it
// has passed. Only then does the toolkit's integration parse it
// (Pusher.Parse). Each event it yields reaches only the member whose
// connection it belongs to, as apps.trigger.received.v1, written in the same
// transaction as the mark that dedupes the delivery.
//
// A toolkit that is no integration, such as Oryxa's own Slack bot, is a Surface:
// it shares all of this, and takes its verified deliveries itself, at static
// URLs, instead of through Pusher.Parse (see surface.go).
//
// An endpoint's secret depends on whose endpoint it is. The core generates it
// for a connection's subscription, an admin enters it for an organization's
// own app, and the deployment's comes from configuration. A secret kept in
// the database is sealed with internal/secrets and bound to its endpoint.
package hooks

import (
	"errors"
	"fmt"
	"regexp"
	"time"

	"github.com/Shubham04-oss/oryxa-go/apps/internal/domain"
	"github.com/Shubham04-oss/oryxa-go/apps/internal/integration"
	"github.com/Shubham04-oss/oryxa-go/internal/secrets"
)

const (
	// PathPrefix is an endpoint's path without its id.
	PathPrefix = "/v1/apps/hooks/"
	// Pattern is the route the ingress serves, for a mux that matches method
	// and path, as apps' does. The handler reads the id from the endpointID
	// path value.
	Pattern = "POST " + PathPrefix + "{endpointID}"

	// MaxBodyBytes bounds a delivery. An event carries at most 128 KiB of an
	// app's payload, so a larger body is not read whole.
	MaxBodyBytes = 1 << 20

	// RatePerSecond is how many deliveries one endpoint takes a second, per
	// replica, before it answers 429.
	RatePerSecond = 50

	// RefusalsPerSecond is how many deliveries that fail verification one
	// client IP may send one endpoint a second, per replica, before its requests are
	// answered 429 without being checked. An app's own deliveries are never
	// refused, so this bounds forgers' work (a JWKS refetch for an unknown
	// key id, say) without touching anyone else's.
	RefusalsPerSecond = 20

	// RotationOverlap is how long an endpoint's previous secret still
	// verifies after a rotation, while the app switches to the new one.
	RotationOverlap = 24 * time.Hour

	// GraphValidationWindow is how long after it is made an endpoint answers
	// Microsoft Graph's unsigned validation request. It answers it once.
	GraphValidationWindow = 10 * time.Minute
)

// idPrefix begins every endpoint id: aph_ and 24 base32 characters, which
// are 120 random bits.
const idPrefix = "aph_"

var idPattern = regexp.MustCompile(`^aph_[a-z2-7]{24}$`)

var (
	// ErrUnverified is a delivery whose signature or token does not prove it
	// came from the app. Nothing in it is used or logged.
	ErrUnverified = errors.New("the delivery could not be verified")
	// ErrAlreadyRevealed means a captured verification token was already
	// shown to an admin. It is shown once.
	ErrAlreadyRevealed = fmt.Errorf("the verification token was already shown once: %w", domain.ErrConflict)
	// ErrNotCaptured means the app has not sent its verification token yet.
	ErrNotCaptured = fmt.Errorf("the app has not sent its verification token yet: %w", domain.ErrConflict)

	// errUnavailable is something hooks relies on failing, KMS or an
	// issuer's keys, rather than anything a caller or a delivery did. A
	// delivery that meets it is answered 503, so the app retries it.
	errUnavailable = fmt.Errorf("a service hooks depends on did not answer: %w", domain.ErrUnavailable)
)

// Status is whether an endpoint is the current one for its subscription.
type Status string

const (
	// StatusActive is an endpoint in use.
	StatusActive Status = "active"
	// StatusReplaced is an endpoint a renewal made a successor for. It still
	// verifies deliveries until it expires or is deleted, because the app's
	// old subscription delivers until it is stopped.
	StatusReplaced Status = "replaced"
)

// Endpoint is one URL an app delivers to, and how its deliveries are
// verified.
type Endpoint struct {
	// ID is 120 random bits, and the only thing in the URL.
	ID      string
	Toolkit string
	// Scope is whose endpoint it is: integration.HookScopeConnection,
	// HookScopeOrganization or HookScopeDeployment.
	Scope string
	// OrganizationID is the organization a connection or organization
	// endpoint belongs to; "" for the deployment's.
	OrganizationID string
	// ConnectionID is the connection a connection endpoint's subscription was
	// made with.
	ConnectionID string
	// Scheme is the toolkit manifest's webhook section as it was when the
	// endpoint was made. It verifies the endpoint's deliveries, so a later
	// manifest never changes how an existing endpoint checks them.
	Scheme integration.Webhook
	// RemoteID is the app's own id for the subscription delivering here.
	RemoteID string
	// ExpiresAt is when the app's subscription lapses, if it does. From then
	// on the endpoint answers as an unknown one would.
	ExpiresAt *time.Time
	Status    Status
	// ReplacedBy is the endpoint a renewal made to take over from this one.
	ReplacedBy string
	// ValidatedAt is when the app first proved the endpoint with a
	// handshake.
	ValidatedAt *time.Time
	CreatedAt   time.Time
	UpdatedAt   time.Time
	// Secret is the endpoint's own secret, sealed, for the store to keep.
	// Only hooks opens it, and it is never encoded into an answer.
	Secret Sealed `json:"-"`
}

// Sealed is an endpoint's own secret, sealed with internal/secrets and bound
// to the endpoint, so it cannot be moved to another. A deployment endpoint
// has none, because its secret is in configuration, unless a
// notion_verification handshake captured one.
type Sealed struct {
	Current *secrets.SealedData
	// Version counts the endpoint's secrets, so a rotation cannot overwrite a
	// secret it did not read.
	Version int
	// Previous still verifies until PreviousUntil, while the app moves to
	// Current after a rotation.
	Previous      *secrets.SealedData
	PreviousUntil *time.Time
	// RevealedAt is when a captured verification token was shown to an
	// admin. That happens once.
	RevealedAt *time.Time
}

// expired says the app's subscription has lapsed, so the endpoint takes
// nothing more.
func (endpoint Endpoint) expired(now time.Time) bool {
	return endpoint.ExpiresAt != nil && !now.Before(*endpoint.ExpiresAt)
}

// capturesSecret says the endpoint's secret is the one its app sends in a
// notion_verification handshake, rather than one Oryxa chose.
func (endpoint Endpoint) capturesSecret() bool {
	return endpoint.Scheme.Handshake == integration.HandshakeNotionVerification
}
