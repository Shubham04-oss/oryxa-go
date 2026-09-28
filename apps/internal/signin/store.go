package signin

import (
	"context"
	"time"

	"github.com/Shubham04-oss/oryxa-go/apps/internal/integration"
	"github.com/Shubham04-oss/oryxa-go/internal/secrets"
)

// Store is where signin keeps its state: apps_signin_flows, apps_credentials
// and apps_oauth_clients, read beside apps_connections. apps' Postgres store
// implements it.
//
// It never changes a connection. It reads a connection's organization,
// member, toolkit and status, and refuses to keep a credential for a
// connection in the wrong state.
type Store interface {
	// InsertSigninFlow records an OAuth sign-in in flight.
	InsertSigninFlow(ctx context.Context, flow Flow) error
	// ConsumeSigninFlow spends the flow whose state hashes to stateHash and
	// answers it, in one statement, so two callbacks racing on one state get
	// one flow between them. A flow already spent, expired at now, or never
	// recorded is ErrFlowNotFound.
	ConsumeSigninFlow(ctx context.Context, stateHash string, now time.Time) (Flow, error)

	// Credential reads a connection's credential, with the connection's
	// organization, member, toolkit and status. ErrNoCredential when it
	// holds none.
	Credential(ctx context.Context, connectionID string) (Credential, error)
	// InsertCredential keeps a finished OAuth sign-in's credential, at
	// version 1. It refuses with domain.ErrConflict unless the connection is
	// pending in credential.OrganizationID and holds no credential yet.
	InsertCredential(ctx context.Context, credential Credential) error
	// ReplaceCredential keeps a key, a password or a client's credentials:
	// as the connection's first, or over one of the same kind, whose version
	// it moves on. It clears the lease and resets the check schedule. It
	// refuses with domain.ErrConflict unless the connection is pending or
	// active in credential.OrganizationID, and any credential it holds is of
	// the same kind. It answers the version kept.
	ReplaceCredential(ctx context.Context, credential Credential) (int64, error)
	// AcquireCredentialLease takes the refresh lease on a credential still
	// at version, until until, unless another holds it past now. It does not
	// move the version: the lease guards a refresh in flight, not the
	// credential.
	AcquireCredentialLease(ctx context.Context, connectionID string, version int64, token string, now, until time.Time) (bool, error)
	// SaveRefreshedCredential saves a refresh under the lease that allowed
	// it, and releases the lease, only while the credential is still at
	// version and token holds its lease. It answers the new version, or
	// domain.ErrConflict when the credential moved on meanwhile.
	SaveRefreshedCredential(ctx context.Context, connectionID, token string, version int64, refreshed Refreshed) (int64, error)
	// ReleaseCredentialLease clears the lease if token still holds it.
	ReleaseCredentialLease(ctx context.Context, connectionID, token string) error
	// DeleteCredential forgets a connection's credential. It answers false
	// when there was none.
	DeleteCredential(ctx context.Context, connectionID string) (bool, error)
	// MarkCredentialUsed records that a connection's credential just worked:
	// its next check waits until at least notBefore, and its failed checks
	// are forgotten.
	MarkCredentialUsed(ctx context.Context, connectionID string, notBefore time.Time) error
	// ClaimCredentialChecks claims up to limit credentials of active
	// connections whose check is due at now, oldest first, by moving their
	// next check to claimUntil, so no other replica takes them meanwhile.
	ClaimCredentialChecks(ctx context.Context, now, claimUntil time.Time, limit int) ([]Credential, error)
	// ClaimRevocations claims, the same way, up to limit credentials still
	// held for connections that have ended (removed or failed), whose next
	// attempt is due at now.
	ClaimRevocations(ctx context.Context, now, claimUntil time.Time, limit int) ([]Credential, error)
	// RecordCredentialCheck schedules a credential's next check or
	// revocation attempt, and records how many have failed in a row.
	RecordCredentialCheck(ctx context.Context, connectionID string, next time.Time, failures int) error
	// GrantShared says whether a live (pending or active) connection other
	// than connectionID, in any organization, holds a credential issued
	// through an OAuth client with clientID, for the account labelled label.
	GrantShared(ctx context.Context, connectionID, clientID, label string) (bool, error)

	// LiveOAuthClient reads an organization's live OAuth client for a
	// registration; organizationID "" is Oryxa's default. ErrNoClient when
	// there is none.
	LiveOAuthClient(ctx context.Context, organizationID, client string) (OAuthClient, error)
	// OAuthClient reads an OAuth client by id, live or removed: a replaced
	// client still refreshes and revokes its connections' tokens.
	// ErrNoClient when there is none.
	OAuthClient(ctx context.Context, id string) (OAuthClient, error)
	// InsertOAuthClient makes client its organization's live one for its
	// registration, retiring the one live before it, in one transaction.
	// domain.ErrConflict when another write made one live meanwhile.
	InsertOAuthClient(ctx context.Context, client OAuthClient) error
	// UpdateOAuthClient rewrites a live client's secret, settings and
	// sealed fields in place. It answers false when it is no longer live.
	UpdateOAuthClient(ctx context.Context, client OAuthClient) (bool, error)
	// RemoveOAuthClient retires an organization's live client for a
	// registration, keeping the row for the connections made through it.
	// It answers false when none was live.
	RemoveOAuthClient(ctx context.Context, organizationID, client string, now time.Time) (bool, error)
}

// Sealer seals and opens one value at a time: secrets.Envelope.
type Sealer interface {
	Seal(ctx context.Context, binding secrets.Binding, plaintext []byte) (secrets.SealedData, error)
	Open(ctx context.Context, binding secrets.Binding, sealed secrets.SealedData) ([]byte, error)
}

// Manifests is where signin reads each toolkit's manifest: apps'
// integration registry.
type Manifests interface {
	Lookup(toolkit string) (integration.Integration, bool)
	Toolkits() []string
}

// Flow is one OAuth sign-in in flight, from StartOAuth to its callback.
type Flow struct {
	// StateHash is the SHA-256 of the state, hex; the state itself is only
	// ever in the member's browser and the app's redirect.
	StateHash      string
	OrganizationID string
	MemberID       string
	ConnectionID   string
	Toolkit        string
	// OAuthClientID is the apps_oauth_clients row the authorization request
	// named, so the code is exchanged by the client it was issued to.
	OAuthClientID string
	// CodeVerifier is PKCE's, or "" when the manifest turns PKCE off.
	CodeVerifier string
	RedirectURI  string
	ReturnTo     string
	// Settings are what the member typed for the method's non-secret
	// fields, and Sealed their secret ones, sealed for the credential they
	// will join.
	Settings  map[string]string
	Sealed    map[string]secrets.SealedData
	ExpiresAt time.Time
}

// Credential is a connection's credential as stored, with what signin reads
// of its connection.
type Credential struct {
	ConnectionID string
	// OrganizationID, MemberID, Toolkit and Status are the connection's.
	OrganizationID string
	MemberID       string
	Toolkit        string
	Status         string
	Kind           string
	// OAuthClientID is the apps_oauth_clients row the credential was issued
	// through, or "" when there is none.
	OAuthClientID string
	// Sealed holds each secret value on its own, by name: the OAuth tokens,
	// a kind's secret standard fields, and the method's secret fields.
	Sealed map[string]secrets.SealedData
	// Settings are the non-secret values: fields by name, and the account's
	// label and ref under integration.SettingAccountLabel and
	// SettingAccountRef.
	Settings map[string]string
	Scopes   []string
	// ExpiresAt is when the access token expires, or nil when it does not.
	ExpiresAt *time.Time
	Version   int64
	// NextCheckAt is when the credential is next checked, or, once its
	// connection has ended, when its revocation is next tried.
	NextCheckAt   time.Time
	CheckFailures int
	UpdatedAt     time.Time
}

// Refreshed is what a refresh saves over a credential.
type Refreshed struct {
	Sealed    map[string]secrets.SealedData
	Settings  map[string]string
	Scopes    []string
	ExpiresAt *time.Time
	UpdatedAt time.Time
}

// OAuth client statuses.
const (
	ClientActive  = "active"
	ClientRemoved = "removed"
)

// OAuthClient is an OAuth client registration's credentials as stored: an
// organization's own, or Oryxa's default when OrganizationID is "".
type OAuthClient struct {
	ID             string
	OrganizationID string
	// Client is the registration, shared across toolkits: google.
	Client string
	// ClientID is "" on a row that only refuses Oryxa's default.
	ClientID     string
	SealedSecret *secrets.SealedData
	// Settings are the client-level fields that are not secret, and
	// SettingRefuseDefault.
	Settings     map[string]string
	SealedFields map[string]secrets.SealedData
	Status       string
	CreatedBy    string
	CreatedAt    time.Time
	UpdatedAt    time.Time
}
