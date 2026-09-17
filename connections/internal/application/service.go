// Package application runs connections' use cases: apps, the connect flow,
// and connection management. Auth and teams decide who may act. Secrets are
// opened only to hand them to the provider that issued them.
package application

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/url"
	"strings"
	"time"
	"unicode"

	connectionseventsv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/connections/v1"

	"github.com/Shubham04-oss/oryxa-go/connections/internal/authclient"
	"github.com/Shubham04-oss/oryxa-go/connections/internal/domain"
	"github.com/Shubham04-oss/oryxa-go/connections/internal/providers"
	"github.com/Shubham04-oss/oryxa-go/connections/internal/secrets"
)

// DefaultHeartbeatInterval is how long a new connection waits for its first
// health check.
const DefaultHeartbeatInterval = 24 * time.Hour

const maxClientValueLength = 1024

type Store interface {
	CreateApp(context.Context, domain.App, domain.Event) error
	App(context.Context, string, string) (domain.App, error)
	Apps(context.Context, string) ([]domain.App, error)
	UpdateApp(context.Context, domain.App, int64, domain.ReconnectReason, domain.Event, func(domain.Connection) (domain.Event, error)) error
	DeleteApp(context.Context, string, string, time.Time, domain.Event, func(domain.Connection) (domain.Event, error)) error

	CreateSession(context.Context, domain.Session) error
	SessionByState(context.Context, string) (domain.Session, error)
	SessionByHandle(context.Context, string) (domain.Session, error)
	RecordCallback(context.Context, string, string, *domain.SealedData, string, time.Time) error
	ClaimSession(context.Context, string, time.Time) error
	BurnSession(context.Context, string, time.Time) error

	Connection(context.Context, string, string) (domain.Connection, error)
	Connections(context.Context, string, domain.ConnectionQuery) ([]domain.Connection, error)
	CompleteConnect(context.Context, string, domain.Connection, domain.Event, time.Time) (domain.Connection, error)
	CompleteReconnect(context.Context, string, domain.Connection, int64, domain.Event) (domain.Connection, error)
	DisconnectConnection(context.Context, string, string, domain.DisconnectReason, time.Time, func(domain.Connection) (domain.Event, error)) (bool, error)

	// AnyApp returns an app even after it was deleted, for its connections'
	// cleanup.
	AnyApp(ctx context.Context, organizationID, appID string) (domain.App, error)
	DeleteOrganizationApps(ctx context.Context, organizationID string, at time.Time, appEvent func(domain.App) (domain.Event, error)) (int, error)
	CountOpenSessions(ctx context.Context, organizationID, memberID string, now time.Time) (int, error)
	DeleteExpiredSessions(ctx context.Context, before time.Time, limit int) (int, error)
	// DeletePublishedOutbox removes events that already reached the bus, so
	// the table does not grow once per change for ever.
	DeletePublishedOutbox(ctx context.Context, before time.Time, limit int) (int, error)

	CreateConnection(ctx context.Context, connection domain.Connection, event domain.Event, nextCheck time.Time) (domain.Connection, error)
	ReplaceCredentials(ctx context.Context, connection domain.Connection, expectedVersion int64, event domain.Event) (domain.Connection, error)
	UpdateConnection(ctx context.Context, organizationID, connectionID string, change domain.ConnectionChange, at time.Time) (domain.Connection, error)
	DisconnectConnections(ctx context.Context, organizationID string, owner domain.OwnerFilter, reason domain.DisconnectReason, at time.Time, connectionEvent func(domain.Connection) (domain.Event, error)) (int, error)
	MarkNeedsReconnect(ctx context.Context, organizationID, connectionID string, expectedVersion int64, reason domain.ReconnectReason, at time.Time, connectionEvent func(domain.Connection) (domain.Event, error)) (bool, error)
	MarkUsed(ctx context.Context, organizationID, connectionID string, at time.Time) error

	AcquireLease(ctx context.Context, organizationID, connectionID string, expectedVersion int64, token string, now, until time.Time) (bool, error)
	SaveRenewedCredentials(ctx context.Context, organizationID, connectionID, token string, expectedVersion int64, credentials *domain.SealedData, scopes []string, at time.Time) (int64, error)
	ReleaseLease(ctx context.Context, organizationID, connectionID, token string) error

	AddHold(ctx context.Context, organizationID, connectionID, holder string, at time.Time) error
	ReleaseHold(ctx context.Context, organizationID, connectionID, holder string) error
	CountHolds(ctx context.Context, organizationID, connectionID string) (int, error)

	ReportConnection(ctx context.Context, organizationID, connectionID string, at time.Time) error
	ClaimHealthChecks(ctx context.Context, now time.Time, limit int, token string, until time.Time) ([]domain.HealthCheck, error)
	RecordHealthCheck(ctx context.Context, check domain.HealthCheck, token string, result domain.HealthResult, event *domain.Event) error
	ClaimCredentialDeletions(ctx context.Context, now time.Time, limit int, token string, until time.Time) ([]domain.Connection, error)
	DeleteCredentials(ctx context.Context, organizationID, connectionID, token string, at time.Time, event domain.Event) error
	// SharesProviderGrant reports whether another connection of the same app
	// and provider account still holds credentials.
	SharesProviderGrant(ctx context.Context, organizationID, appID, providerAccountID, exceptConnectionID string) (bool, error)
	// WithProviderGrantLock runs fn while no other caller holds the same app
	// and provider account's lock.
	WithProviderGrantLock(ctx context.Context, organizationID, appID, providerAccountID string, fn func(context.Context) error) error
}

// Directory asks auth who the caller is in an organization.
type Directory interface {
	Membership(context.Context, string, authclient.Credentials) (domain.OrgMembership, error)
}

// Teams asks teams what the caller may do with a team.
type Teams interface {
	Team(ctx context.Context, organizationID, teamID string, credentials authclient.Credentials) (domain.TeamAccess, error)
}

type Sealer interface {
	Seal(context.Context, secrets.Binding, []byte) (domain.SealedData, error)
	Open(context.Context, secrets.Binding, domain.SealedData) ([]byte, error)
}

// ProviderClient calls providers. providers.Client is the real one.
type ProviderClient interface {
	AuthorizationURL(providers.OAuth2, providers.AuthorizationRequest) (string, error)
	Exchange(context.Context, providers.OAuth2, providers.ExchangeRequest) (providers.Grant, error)
	Refresh(context.Context, providers.OAuth2, providers.RefreshRequest) (providers.Grant, error)
	Identity(context.Context, providers.OAuth2, providers.Grant) (domain.ProviderAccount, error)
	KeyIdentity(context.Context, providers.Provider, domain.CredentialType, domain.CredentialInput) (domain.ProviderAccount, error)
	Mint(context.Context, providers.Provider, providers.MintRequest) (providers.Grant, error)
	AdminConsentURL(providers.Provider, providers.AdminConsentRequest) (string, error)
	InstallationURL(providers.Provider, providers.InstallationRequest) (string, error)
	UserInstallations(context.Context, providers.Provider, providers.Grant) ([]string, error)
	Installation(context.Context, providers.Provider, providers.GitHubApp, string) (domain.ProviderAccount, error)
	InstallationToken(context.Context, providers.Provider, providers.GitHubApp, string) (providers.Grant, error)
	Revoke(context.Context, providers.Provider, providers.RevokeRequest) (bool, error)
}

type Dependencies struct {
	Store     Store
	Directory Directory
	// Teams is optional. Without it, team connections cannot be made.
	Teams          Teams
	Sealer         Sealer
	ProviderClient ProviderClient
	Providers      providers.Registry
	Logger         *slog.Logger
	Now            func() time.Time
	Random         io.Reader
}

type Config struct {
	// PublicBaseURL is where providers send the browser back to.
	PublicBaseURL string
	// AllowedOrigins bounds the return URLs a connect flow may finish at.
	AllowedOrigins    []string
	HeartbeatInterval time.Duration
}

type Service struct {
	store             Store
	directory         Directory
	teams             Teams
	sealer            Sealer
	oauth             ProviderClient
	providers         providers.Registry
	logger            *slog.Logger
	now               func() time.Time
	random            io.Reader
	publicBaseURL     string
	allowedOrigins    map[string]struct{}
	heartbeatInterval time.Duration
}

func New(dependencies Dependencies, config Config) *Service {
	service := &Service{
		store:             dependencies.Store,
		directory:         dependencies.Directory,
		teams:             dependencies.Teams,
		sealer:            dependencies.Sealer,
		oauth:             dependencies.ProviderClient,
		providers:         dependencies.Providers,
		logger:            dependencies.Logger,
		now:               dependencies.Now,
		random:            dependencies.Random,
		publicBaseURL:     strings.TrimRight(config.PublicBaseURL, "/"),
		allowedOrigins:    map[string]struct{}{},
		heartbeatInterval: config.HeartbeatInterval,
	}
	if service.logger == nil {
		service.logger = slog.New(slog.DiscardHandler)
	}
	if service.now == nil {
		service.now = time.Now
	}
	if service.random == nil {
		service.random = rand.Reader
	}
	if service.heartbeatInterval <= 0 {
		service.heartbeatInterval = DefaultHeartbeatInterval
	}
	for _, origin := range config.AllowedOrigins {
		if parsed, err := url.Parse(origin); err == nil && parsed.Scheme != "" && parsed.Host != "" {
			service.allowedOrigins[strings.ToLower(parsed.Scheme+"://"+parsed.Host)] = struct{}{}
		}
	}
	return service
}

// clock is the service's time at the precision PostgreSQL keeps.
func (s *Service) clock() time.Time {
	return s.now().UTC().Truncate(time.Microsecond)
}

// caller asks auth who is calling. An organization the caller is not in is
// concealed as not found.
func (s *Service) caller(ctx context.Context, credentials authclient.Credentials, organizationID string) (domain.OrgMembership, error) {
	if strings.TrimSpace(organizationID) == "" {
		return domain.OrgMembership{}, domain.ErrNotFound
	}
	membership, err := s.directory.Membership(ctx, organizationID, credentials)
	if err != nil {
		return domain.OrgMembership{}, conceal(err)
	}
	if membership.OrganizationID != organizationID || membership.UserID == "" || !membership.Role.Valid() {
		return domain.OrgMembership{}, domain.ErrForbidden
	}
	return membership, nil
}

func (s *Service) admin(ctx context.Context, credentials authclient.Credentials, organizationID string) (domain.OrgMembership, error) {
	membership, err := s.callerForWrite(ctx, credentials, organizationID)
	if err != nil {
		return domain.OrgMembership{}, err
	}
	if err := domain.MayManageApps(domain.Caller{Membership: membership}); err != nil {
		return domain.OrgMembership{}, err
	}
	return membership, nil
}

// callerForWrite is the one place the public API resolves a caller's
// membership for a route that changes something. An agent's membership may
// only read: every public route but GET calls this instead of caller, so
// resolving membership and enforcing the rule cannot drift apart, and a new
// write route gets the rule for free by following the same pattern. The
// internal API is unaffected: it resolves a forwarded member with caller
// directly, never through here.
func (s *Service) callerForWrite(ctx context.Context, credentials authclient.Credentials, organizationID string) (domain.OrgMembership, error) {
	membership, err := s.caller(ctx, credentials, organizationID)
	if err != nil {
		return domain.OrgMembership{}, err
	}
	if membership.Agent {
		return domain.OrgMembership{}, domain.ErrAgentForbidden
	}
	return membership, nil
}

// callerAt adds what teams says about a team to the caller, when the scope is
// a team.
func (s *Service) callerAt(ctx context.Context, credentials authclient.Credentials, membership domain.OrgMembership, scope domain.Scope, teamID string) (domain.Caller, error) {
	caller := domain.Caller{Membership: membership}
	if scope != domain.ScopeTeam {
		return caller, nil
	}
	access, err := s.teamAccess(ctx, credentials, membership.OrganizationID, teamID)
	if err != nil {
		return domain.Caller{}, err
	}
	caller.Team = access
	return caller, nil
}

func (s *Service) teamAccess(ctx context.Context, credentials authclient.Credentials, organizationID, teamID string) (domain.TeamAccess, error) {
	if s.teams == nil {
		return domain.TeamAccess{}, fmt.Errorf("team connections need TEAMS_URL: %w", domain.ErrInvalid)
	}
	access, err := s.teams.Team(ctx, organizationID, teamID, credentials)
	if err != nil {
		return domain.TeamAccess{}, conceal(err)
	}
	if access.TeamID != teamID || !access.CanSee() {
		return domain.TeamAccess{}, domain.ErrNotFound
	}
	return access, nil
}

func conceal(err error) error {
	if errors.Is(err, domain.ErrNotFound) {
		return domain.ErrNotFound
	}
	return err
}

func (s *Service) event(detailType string, data any, at time.Time) (domain.Event, error) {
	id, err := s.newID("evt")
	if err != nil {
		return domain.Event{}, err
	}
	detail, err := json.Marshal(data)
	if err != nil {
		return domain.Event{}, fmt.Errorf("encode %s: %w", detailType, err)
	}
	return domain.Event{ID: id, DetailType: detailType, OccurredAt: at, Data: detail}, nil
}

func (s *Service) needsReconnectEvent(at time.Time) func(domain.Connection) (domain.Event, error) {
	return func(connection domain.Connection) (domain.Event, error) {
		return s.event(connectionseventsv1.ConnectionNeedsReconnectDetailType, connectionseventsv1.ConnectionNeedsReconnect{
			ConnectionID: connection.ID, OrganizationID: connection.OrganizationID, AppID: connection.AppID,
			Provider: connection.Provider, Scope: string(connection.Scope), TeamID: connection.TeamID, MemberID: connection.MemberID,
			ProviderAccount: eventAccount(connection), Reason: connection.Reason, DetectedAt: at,
		}, at)
	}
}

func (s *Service) disconnectedEvent(actorID string, at time.Time) func(domain.Connection) (domain.Event, error) {
	return func(connection domain.Connection) (domain.Event, error) {
		return s.event(connectionseventsv1.ConnectionDisconnectedDetailType, connectionseventsv1.ConnectionDisconnected{
			ConnectionID: connection.ID, OrganizationID: connection.OrganizationID, AppID: connection.AppID,
			Provider: connection.Provider, Scope: string(connection.Scope), TeamID: connection.TeamID, MemberID: connection.MemberID,
			ProviderAccount: eventAccount(connection), DisconnectedBy: actorID, Reason: connection.Reason, DisconnectedAt: at,
		}, at)
	}
}

func eventAccount(connection domain.Connection) connectionseventsv1.ProviderAccount {
	return connectionseventsv1.ProviderAccount{ID: connection.ProviderAccount.ID, Name: connection.ProviderAccount.Name}
}

func (s *Service) newID(prefix string) (string, error) {
	token, err := s.randomToken(18)
	if err != nil {
		return "", fmt.Errorf("generate %s id: %w", prefix, err)
	}
	return prefix + "_" + token, nil
}

func (s *Service) randomToken(size int) (string, error) {
	value := make([]byte, size)
	if _, err := io.ReadFull(s.random, value); err != nil {
		return "", fmt.Errorf("read random bytes: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(value), nil
}

func hashValue(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

func appSecretBinding(app domain.App) secrets.Binding {
	return secrets.Binding{OrganizationID: app.OrganizationID, ResourceType: secrets.ResourceAppSecret, ResourceID: app.ID}
}

func sessionBinding(session domain.Session) secrets.Binding {
	return secrets.Binding{OrganizationID: session.OrganizationID, ResourceType: secrets.ResourceSession, ResourceID: session.StateHash}
}

func connectionBinding(organizationID, connectionID string) secrets.Binding {
	return secrets.Binding{OrganizationID: organizationID, ResourceType: secrets.ResourceConnection, ResourceID: connectionID}
}

// returnURI checks where a connect flow may send the browser back to: an
// absolute https URL, or http on localhost, on an allowed origin.
func (s *Service) returnURI(raw string) (string, error) {
	value := strings.TrimSpace(raw)
	target, err := url.Parse(value)
	if value == "" || err != nil || !target.IsAbs() || target.Host == "" || target.User != nil || target.Fragment != "" {
		return "", fmt.Errorf("return_uri must be an absolute URL on an allowed origin: %w", domain.ErrInvalid)
	}
	if target.Scheme != "https" && !(target.Scheme == "http" && localhost(target.Hostname())) {
		return "", fmt.Errorf("return_uri must use https: %w", domain.ErrInvalid)
	}
	if _, ok := s.allowedOrigins[strings.ToLower(target.Scheme+"://"+target.Host)]; !ok {
		return "", fmt.Errorf("return_uri must be an absolute URL on an allowed origin: %w", domain.ErrInvalid)
	}
	return target.String(), nil
}

func localhost(host string) bool {
	return host == "localhost" || host == "127.0.0.1" || host == "::1"
}

func withQuery(raw, key, value string) (string, error) {
	target, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("stored return URI is invalid: %w", err)
	}
	query := target.Query()
	query.Set(key, value)
	target.RawQuery = query.Encode()
	return target.String(), nil
}

func clientValue(name, raw string) (string, error) {
	value := strings.TrimSpace(raw)
	if value == "" || len(value) > maxClientValueLength || strings.IndexFunc(value, unicode.IsControl) >= 0 {
		return "", fmt.Errorf("%s is required and must be one line: %w", name, domain.ErrInvalid)
	}
	return value, nil
}

// usesClient reports whether apps of a credential type hold a client ID and
// secret.
func usesClient(credentialType domain.CredentialType) bool {
	return credentialType == domain.CredentialOAuth2 || credentialType == domain.CredentialOAuth2ClientCredentials ||
		credentialType == domain.CredentialInstallation
}
