package hooks

import (
	"context"
	"crypto/rand"
	"encoding/base32"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/Shubham04-oss/oryxa-go/apps/internal/domain"
	"github.com/Shubham04-oss/oryxa-go/apps/internal/integration"
	"github.com/Shubham04-oss/oryxa-go/internal/safehttp"
	"github.com/Shubham04-oss/oryxa-go/internal/secrets"
)

// Config is what hooks is told about the deployment.
type Config struct {
	// PublicBaseURL is where apps reach this process, such as
	// https://api.oryxa.in. An endpoint's URL is built on it, and it is what
	// a jwt_push audience of {endpoint_url} means.
	PublicBaseURL string
	// DeploymentSecrets are each toolkit's deployment webhook secrets, from
	// Secrets Manager: the current one, and during a rotation the previous
	// one as well. A deployment endpoint verifies with them.
	DeploymentSecrets map[string][]secrets.Secret
	// Values are the configuration a manifest names as {config:name}, such as
	// pubsub_push_account, the account Gmail's Pub/Sub pushes are signed for.
	Values map[string]string
	// SurfaceDeadline is how long a surface's delivery may use the database
	// before the ingress answers 5xx; DefaultSurfaceDeadline without one.
	SurfaceDeadline time.Duration
}

// Dependencies are what hooks borrows from apps.
type Dependencies struct {
	Store Store
	// Sealer is apps' envelope, bound to the service oryxa-apps.
	Sealer Sealer
	// Integrations is the registry of Oryxa's own integrations.
	Integrations  Integrations
	Subscriptions Subscriptions
	// Surfaces are the toolkits that are no integration and take their own
	// deliveries; see Surface. Without any, every toolkit is an integration's.
	Surfaces Surfaces
	// Tokens verifies jwt_push tokens. Without one, an OIDCVerifier fetches
	// issuers' keys through internal/safehttp.
	Tokens TokenVerifier
	Logger *slog.Logger
	Clock  func() time.Time
	// Random makes ids and secrets; crypto/rand without one.
	Random io.Reader
}

// Service makes and keeps webhook endpoints, and serves the ingress their
// deliveries arrive at.
type Service struct {
	store         Store
	sealer        Sealer
	integrations  Integrations
	subscriptions Subscriptions
	surfaces      Surfaces
	tokens        TokenVerifier
	logger        *slog.Logger
	now           func() time.Time
	random        io.Reader

	baseURL           string
	deploymentSecrets map[string][][]byte
	values            map[string]string
	surfaceDeadline   time.Duration

	limiter  *limiter
	refusals *limiter
	opened   openedCache
}

// New assembles hooks. It checks its configuration and touches nothing.
func New(dependencies Dependencies, config Config) (*Service, error) {
	switch {
	case dependencies.Store == nil:
		return nil, errors.New("hooks needs a store")
	case dependencies.Sealer == nil:
		return nil, errors.New("hooks needs a sealer for endpoint secrets")
	case dependencies.Integrations == nil:
		return nil, errors.New("hooks needs the integrations registry")
	case dependencies.Subscriptions == nil:
		return nil, errors.New("hooks needs to find trigger subscriptions")
	}
	base := strings.TrimRight(strings.TrimSpace(config.PublicBaseURL), "/")
	parsed, err := url.Parse(base)
	if err != nil || (parsed.Scheme != "https" && parsed.Scheme != "http") || parsed.Host == "" ||
		parsed.RawQuery != "" || parsed.Fragment != "" || parsed.User != nil {
		return nil, fmt.Errorf("hooks: the public base URL %q must be an absolute http or https URL", config.PublicBaseURL)
	}
	service := &Service{
		store:             dependencies.Store,
		sealer:            dependencies.Sealer,
		integrations:      dependencies.Integrations,
		subscriptions:     dependencies.Subscriptions,
		surfaces:          dependencies.Surfaces,
		tokens:            dependencies.Tokens,
		logger:            dependencies.Logger,
		now:               dependencies.Clock,
		random:            dependencies.Random,
		baseURL:           base,
		deploymentSecrets: map[string][][]byte{},
		values:            maps.Clone(config.Values),
		surfaceDeadline:   config.SurfaceDeadline,
	}
	if service.surfaceDeadline <= 0 {
		service.surfaceDeadline = DefaultSurfaceDeadline
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
	if service.values == nil {
		service.values = map[string]string{}
	}
	if service.tokens == nil {
		client := safehttp.NewClient(safehttp.HTTPSOnly(), safehttp.Timeout(10*time.Second))
		service.tokens = NewOIDCVerifier(client, func() time.Time { return service.now() })
	}
	for toolkit, configured := range config.DeploymentSecrets {
		for _, secret := range configured {
			if value := strings.TrimSpace(secret.Reveal()); value != "" {
				service.deploymentSecrets[toolkit] = append(service.deploymentSecrets[toolkit], []byte(value))
			}
		}
	}
	service.limiter = newLimiter(RatePerSecond, time.Second, func() time.Time { return service.now() })
	service.refusals = newLimiter(RefusalsPerSecond, time.Second, func() time.Time { return service.now() })
	return service, nil
}

// Handler is the ingress, for apps to mount at Pattern.
func (s *Service) Handler() http.Handler { return ingress{service: s} }

// URL is where an endpoint receives: the only address an app is given.
func (s *Service) URL(endpointID string) string { return s.baseURL + PathPrefix + endpointID }

// CreateInput is an endpoint to make.
type CreateInput struct {
	Toolkit string
	// Scope is whose endpoint it is. A connection endpoint serves one
	// subscription made with the connection. An organization endpoint serves
	// an organization's own app, whose secret its admin enters; that is how
	// an organization brings its own Slack app to a toolkit whose manifest
	// has a deployment endpoint. A deployment endpoint serves Oryxa's own
	// app for every organization; see DeploymentEndpoint.
	Scope          string
	OrganizationID string
	ConnectionID   string
	// Secret is what an organization's admin entered. Every other endpoint's
	// secret is generated, configured, or sent by the app itself, and none is
	// taken here.
	Secret secrets.Secret
	// ExpiresAt is when the app's subscription lapses, when it is already
	// known; SetRemote records it otherwise.
	ExpiresAt *time.Time
}

// Created is an endpoint just made, and what the app is given for it.
type Created struct {
	Endpoint Endpoint
	URL      string
	// Hook is what Pusher.Subscribe registers with the app. Its Secret is set
	// only for a connection endpoint, whose secret was generated here; this
	// is the one time it is seen in plaintext, and Hook redacts it wherever
	// it is printed or logged.
	Hook integration.Hook
}

// Create makes an endpoint for a toolkit, freezing its manifest's webhook
// section as the endpoint's scheme.
func (s *Service) Create(ctx context.Context, input CreateInput) (Created, error) {
	toolkit, err := domain.CheckToolkit(input.Toolkit)
	if err != nil {
		return Created{}, err
	}
	input.Toolkit = toolkit
	scheme, err := s.scheme(toolkit)
	if err != nil {
		return Created{}, err
	}
	if err := checkScope(scheme, input); err != nil {
		return Created{}, err
	}
	secret, err := s.secretFor(scheme, input)
	if err != nil {
		return Created{}, err
	}
	endpoint, err := s.newEndpoint(ctx, input, scheme, secret)
	if err != nil {
		return Created{}, err
	}
	if err := s.store.CreateHookEndpoint(ctx, endpoint); err != nil {
		return Created{}, err
	}
	s.logger.InfoContext(ctx, "webhook endpoint created",
		"endpoint_id", endpoint.ID, "toolkit", endpoint.Toolkit, "scope", endpoint.Scope)
	return s.created(endpoint, secret), nil
}

// DeploymentEndpoint answers the deployment's endpoint for a toolkit, and its
// URL, making it the first time. The URL goes into the app's own settings
// (Slack's request URL, a Pub/Sub push subscription), so it stays the same
// from then on.
func (s *Service) DeploymentEndpoint(ctx context.Context, toolkit string) (Endpoint, string, error) {
	toolkit, err := domain.CheckToolkit(toolkit)
	if err != nil {
		return Endpoint{}, "", err
	}
	endpoint, err := s.store.ActiveHookEndpoint(ctx, integration.HookScopeDeployment, "", toolkit)
	if err == nil {
		return endpoint, s.URL(endpoint.ID), nil
	}
	if !errors.Is(err, domain.ErrNotFound) {
		return Endpoint{}, "", err
	}
	created, err := s.Create(ctx, CreateInput{Toolkit: toolkit, Scope: integration.HookScopeDeployment})
	if errors.Is(err, domain.ErrConflict) {
		// Another replica made it first.
		if endpoint, err = s.store.ActiveHookEndpoint(ctx, integration.HookScopeDeployment, "", toolkit); err != nil {
			return Endpoint{}, "", err
		}
		return endpoint, s.URL(endpoint.ID), nil
	}
	if err != nil {
		return Endpoint{}, "", err
	}
	return created.Endpoint, created.URL, nil
}

// OrganizationEndpoint answers an organization's own endpoint for a toolkit.
func (s *Service) OrganizationEndpoint(ctx context.Context, organizationID, toolkit string) (Endpoint, error) {
	if strings.TrimSpace(organizationID) == "" {
		return Endpoint{}, fmt.Errorf("an organization is required: %w", domain.ErrInvalid)
	}
	toolkit, err := domain.CheckToolkit(toolkit)
	if err != nil {
		return Endpoint{}, err
	}
	return s.store.ActiveHookEndpoint(ctx, integration.HookScopeOrganization, organizationID, toolkit)
}

// Endpoint answers one endpoint.
func (s *Service) Endpoint(ctx context.Context, id string) (Endpoint, error) {
	if !idPattern.MatchString(id) {
		return Endpoint{}, fmt.Errorf("webhook endpoint: %w", domain.ErrNotFound)
	}
	return s.store.HookEndpoint(ctx, id)
}

// EndpointsForConnection answers a connection's endpoints, oldest first.
func (s *Service) EndpointsForConnection(ctx context.Context, connectionID string) ([]Endpoint, error) {
	return s.store.HookEndpointsForConnection(ctx, connectionID)
}

// maxRemoteID bounds the app's id for a subscription.
const maxRemoteID = 1024

// SetRemote records the app's id for the subscription delivering to an
// endpoint, once Pusher.Subscribe answered it, and when the subscription
// lapses. An app that extends a subscription in place (Microsoft Graph)
// records the new expiry the same way.
func (s *Service) SetRemote(ctx context.Context, id, remoteID string, expiresAt *time.Time) error {
	if len(remoteID) > maxRemoteID {
		return fmt.Errorf("a subscription id is at most %d characters: %w", maxRemoteID, domain.ErrInvalid)
	}
	if !idPattern.MatchString(id) {
		return fmt.Errorf("webhook endpoint: %w", domain.ErrNotFound)
	}
	return s.store.SetHookEndpointRemote(ctx, id, remoteID, utc(expiresAt), s.now().UTC())
}

// Rotate gives an endpoint a new secret. The previous one still verifies for
// RotationOverlap, while the app moves to the new one. A connection
// endpoint's new secret is generated, and answered in Hook for the caller to
// update the subscription with; an organization endpoint takes the one its
// admin entered. A deployment endpoint's secret is rotated in configuration,
// by listing both secrets until the overlap ends.
func (s *Service) Rotate(ctx context.Context, id string, secret secrets.Secret) (Created, error) {
	endpoint, err := s.Endpoint(ctx, id)
	if err != nil {
		return Created{}, err
	}
	switch {
	case endpoint.Scheme.Verify.Type == integration.VerifyJWTPush:
		return Created{}, fmt.Errorf("a jwt_push endpoint has no secret to rotate: %w", domain.ErrInvalid)
	case endpoint.capturesSecret():
		return Created{}, fmt.Errorf("this app's webhook secret is the one it sent, and it is not rotated here: %w", domain.ErrInvalid)
	case endpoint.Scope == integration.HookScopeDeployment:
		return Created{}, fmt.Errorf("a deployment endpoint's secret is rotated in its configuration: %w", domain.ErrInvalid)
	case endpoint.Secret.Current == nil:
		return Created{}, fmt.Errorf("the endpoint has no secret to rotate: %w", domain.ErrConflict)
	}
	value, err := s.secretFor(endpoint.Scheme, CreateInput{Scope: endpoint.Scope, Secret: secret})
	if err != nil {
		return Created{}, err
	}
	sealed, err := s.seal(ctx, endpoint.ID, value)
	if err != nil {
		return Created{}, err
	}
	now := s.now().UTC()
	until := now.Add(RotationOverlap)
	rotated, err := s.store.RotateHookSecret(ctx, endpoint.ID, endpoint.Secret.Version, sealed, until, now)
	if err != nil {
		return Created{}, err
	}
	if !rotated {
		return Created{}, fmt.Errorf("the endpoint's secret changed meanwhile: %w", domain.ErrConflict)
	}
	s.opened.forget(endpoint.ID)
	endpoint.Secret = Sealed{
		Current: &sealed, Version: endpoint.Secret.Version + 1,
		Previous: endpoint.Secret.Current, PreviousUntil: &until, RevealedAt: endpoint.Secret.RevealedAt,
	}
	endpoint.UpdatedAt = now
	s.logger.InfoContext(ctx, "webhook endpoint secret rotated",
		"endpoint_id", endpoint.ID, "toolkit", endpoint.Toolkit, "previous_until", until)
	return s.created(endpoint, value), nil
}

// Renew makes a fresh endpoint to take over a connection endpoint's
// subscription, and marks the old one replaced.
//
// A renewed subscription gets an endpoint of its own because a Google channel
// numbers its messages from 1 again, and the old endpoint's dedupe would take
// them for repeats. The caller subscribes the new endpoint's Hook, calls
// SetRemote on it, then unsubscribes the old remote id and deletes the old
// endpoint, which verifies deliveries until then. If subscribing fails, the
// caller deletes the new endpoint, which makes the old one active again, and
// Due offers it once more.
func (s *Service) Renew(ctx context.Context, id string) (Created, error) {
	old, err := s.Endpoint(ctx, id)
	if err != nil {
		return Created{}, err
	}
	switch {
	case old.Scope != integration.HookScopeConnection:
		return Created{}, fmt.Errorf("only a connection endpoint's subscription is renewed onto a new endpoint: %w", domain.ErrInvalid)
	case old.Status != StatusActive:
		return Created{}, fmt.Errorf("the endpoint was already renewed: %w", domain.ErrConflict)
	}
	scheme, err := s.scheme(old.Toolkit)
	if err != nil {
		return Created{}, err
	}
	input := CreateInput{
		Toolkit: old.Toolkit, Scope: old.Scope, OrganizationID: old.OrganizationID, ConnectionID: old.ConnectionID,
	}
	if err := checkScope(scheme, input); err != nil {
		return Created{}, err
	}
	secret, err := s.secretFor(scheme, input)
	if err != nil {
		return Created{}, err
	}
	replacement, err := s.newEndpoint(ctx, input, scheme, secret)
	if err != nil {
		return Created{}, err
	}
	if err := s.store.ReplaceHookEndpoint(ctx, old.ID, replacement); err != nil {
		return Created{}, err
	}
	s.logger.InfoContext(ctx, "webhook endpoint renewed",
		"endpoint_id", replacement.ID, "replaces", old.ID, "toolkit", replacement.Toolkit)
	return s.created(replacement, secret), nil
}

// Delete deletes an endpoint and its sealed secret. Anything delivered to it
// afterwards is answered as an unknown endpoint's is.
func (s *Service) Delete(ctx context.Context, id string) error {
	if !idPattern.MatchString(id) {
		return fmt.Errorf("webhook endpoint: %w", domain.ErrNotFound)
	}
	deleted, err := s.store.DeleteHookEndpoint(ctx, id)
	if err != nil {
		return err
	}
	if !deleted {
		return fmt.Errorf("webhook endpoint: %w", domain.ErrNotFound)
	}
	s.opened.forget(id)
	s.logger.InfoContext(ctx, "webhook endpoint deleted", "endpoint_id", id)
	return nil
}

// DeleteForConnection deletes every endpoint of a connection, as removing
// the connection must, and answers them, so their subscriptions can be
// removed at the app with their RemoteID.
func (s *Service) DeleteForConnection(ctx context.Context, connectionID string) ([]Endpoint, error) {
	deleted, err := s.store.DeleteHookEndpointsForConnection(ctx, connectionID)
	if err != nil {
		return nil, err
	}
	for _, endpoint := range deleted {
		s.opened.forget(endpoint.ID)
	}
	if len(deleted) > 0 {
		s.logger.InfoContext(ctx, "webhook endpoints deleted with their connection",
			"connection_id", connectionID, "count", len(deleted))
	}
	return deleted, nil
}

// maxDue bounds one Due answer.
const maxDue = 500

// Due answers active endpoints whose subscriptions lapse within the given
// time, soonest first, for a worker to renew.
func (s *Service) Due(ctx context.Context, within time.Duration, limit int) ([]Endpoint, error) {
	if limit <= 0 || limit > maxDue {
		limit = maxDue
	}
	return s.store.HookEndpointsExpiring(ctx, s.now().UTC().Add(within), limit)
}

// SweepExpired deletes endpoints whose subscriptions have lapsed, and forgets
// previous secrets whose overlap has ended. apps' maintenance runs it.
func (s *Service) SweepExpired(ctx context.Context) (int64, error) {
	deleted, err := s.store.DeleteExpiredHookEndpoints(ctx, s.now().UTC())
	if err != nil {
		return 0, err
	}
	if deleted > 0 {
		s.logger.InfoContext(ctx, "lapsed webhook endpoints deleted", "count", deleted)
	}
	return deleted, nil
}

// RevealVerificationToken shows an admin the token a notion_verification
// endpoint captured, which they paste back into the app to prove the
// endpoint is theirs. It is shown once: every later call is
// ErrAlreadyRevealed, and a token that never arrived is ErrNotCaptured. The
// caller decides who may see it.
func (s *Service) RevealVerificationToken(ctx context.Context, id string) (secrets.Secret, error) {
	endpoint, err := s.Endpoint(ctx, id)
	if err != nil {
		return "", err
	}
	switch {
	case !endpoint.capturesSecret():
		return "", fmt.Errorf("this endpoint's app sends no verification token: %w", domain.ErrInvalid)
	case endpoint.Secret.RevealedAt != nil:
		return "", ErrAlreadyRevealed
	case endpoint.Secret.Current == nil:
		return "", ErrNotCaptured
	}
	plaintext, err := s.open(ctx, endpoint.ID, *endpoint.Secret.Current)
	if err != nil {
		return "", err
	}
	defer secrets.Wipe(plaintext)
	// Only the call that marks it revealed shows it, so two admins asking at
	// once cannot both see it.
	revealed, err := s.store.MarkHookSecretRevealed(ctx, endpoint.ID, s.now().UTC())
	if err != nil {
		return "", err
	}
	if !revealed {
		return "", ErrAlreadyRevealed
	}
	s.logger.InfoContext(ctx, "webhook verification token revealed", "endpoint_id", endpoint.ID, "toolkit", endpoint.Toolkit)
	return secrets.Secret(plaintext), nil
}

// scheme is a toolkit's webhook section as its manifest declares it now,
// copied, so an endpoint's frozen scheme shares nothing with the registry.
func (s *Service) scheme(toolkit string) (integration.Webhook, error) {
	// A surface is no integration and has no manifest; what it verifies with
	// is its own, and its owner tests that it is a scheme the core reads.
	if surface, ok := s.surface(toolkit); ok {
		return copyScheme(toolkit, surface.Webhook())
	}
	registered, ok := s.integrations.Lookup(toolkit)
	if !ok {
		return integration.Webhook{}, fmt.Errorf("%s is not one of Oryxa's own integrations: %w", toolkit, domain.ErrNotFound)
	}
	manifest := registered.Manifest()
	if manifest.Webhook == nil {
		return integration.Webhook{}, fmt.Errorf("%s declares no webhook: %w", toolkit, domain.ErrInvalid)
	}
	if err := manifest.Validate(); err != nil {
		return integration.Webhook{}, fmt.Errorf("%s: %v: %w", toolkit, err, domain.ErrInvalid)
	}
	return copyScheme(toolkit, *manifest.Webhook)
}

// checkScope checks an endpoint's scope against its manifest's, and that it
// names exactly the owner its scope needs.
func checkScope(scheme integration.Webhook, input CreateInput) error {
	switch input.Scope {
	case integration.HookScopeConnection:
		switch {
		case scheme.Scope != integration.HookScopeConnection:
			return fmt.Errorf("%s's webhook is not one per connection: %w", input.Toolkit, domain.ErrInvalid)
		case strings.TrimSpace(input.OrganizationID) == "" || strings.TrimSpace(input.ConnectionID) == "":
			return fmt.Errorf("a connection endpoint names its organization and connection: %w", domain.ErrInvalid)
		}
	case integration.HookScopeOrganization:
		switch {
		case scheme.Scope == integration.HookScopeConnection:
			return fmt.Errorf("%s's webhook is one per connection: %w", input.Toolkit, domain.ErrInvalid)
		case scheme.Verify.Type == integration.VerifyJWTPush:
			return fmt.Errorf("%s's pushes are signed for the deployment, not an organization: %w", input.Toolkit, domain.ErrInvalid)
		case strings.TrimSpace(input.OrganizationID) == "" || input.ConnectionID != "":
			return fmt.Errorf("an organization endpoint names its organization and no connection: %w", domain.ErrInvalid)
		}
	case integration.HookScopeDeployment:
		switch {
		case scheme.Scope != integration.HookScopeDeployment:
			return fmt.Errorf("%s's webhook is not the deployment's: %w", input.Toolkit, domain.ErrInvalid)
		case input.OrganizationID != "" || input.ConnectionID != "":
			return fmt.Errorf("a deployment endpoint belongs to no organization or connection: %w", domain.ErrInvalid)
		}
	default:
		return fmt.Errorf("a webhook endpoint's scope is connection, organization or deployment: %w", domain.ErrInvalid)
	}
	return nil
}

// secretFor is the secret a new endpoint keeps: generated for a
// connection's subscription, and the admin's for an organization's own app.
// It is "" for an endpoint whose secret is configured (the deployment's),
// sent by the app (notion_verification), or not a secret at all (jwt_push).
func (s *Service) secretFor(scheme integration.Webhook, input CreateInput) (string, error) {
	entered := input.Secret.Reveal() != ""
	switch {
	case scheme.Verify.Type == integration.VerifyJWTPush:
		if entered {
			return "", fmt.Errorf("a jwt_push endpoint takes no secret: %w", domain.ErrInvalid)
		}
		return "", nil
	case scheme.Handshake == integration.HandshakeNotionVerification:
		if entered {
			return "", fmt.Errorf("this app sends its webhook secret itself: %w", domain.ErrInvalid)
		}
		return "", nil
	case input.Scope == integration.HookScopeConnection:
		if entered {
			return "", fmt.Errorf("a connection endpoint's secret is generated: %w", domain.ErrInvalid)
		}
		return s.generateSecret()
	case input.Scope == integration.HookScopeOrganization:
		return checkEnteredSecret(input.Secret)
	default:
		if entered {
			return "", fmt.Errorf("a deployment endpoint's secret is configured: %w", domain.ErrInvalid)
		}
		return "", nil
	}
}

// newEndpoint makes an endpoint's row, with its secret sealed to it.
func (s *Service) newEndpoint(ctx context.Context, input CreateInput, scheme integration.Webhook, secret string) (Endpoint, error) {
	id, err := newID(s.random, idPrefix)
	if err != nil {
		return Endpoint{}, err
	}
	now := s.now().UTC()
	endpoint := Endpoint{
		ID: id, Toolkit: input.Toolkit, Scope: input.Scope,
		OrganizationID: strings.TrimSpace(input.OrganizationID), ConnectionID: strings.TrimSpace(input.ConnectionID),
		Scheme: scheme, ExpiresAt: utc(input.ExpiresAt), Status: StatusActive, CreatedAt: now, UpdatedAt: now,
	}
	if secret != "" {
		sealed, err := s.seal(ctx, id, secret)
		if err != nil {
			return Endpoint{}, err
		}
		endpoint.Secret = Sealed{Current: &sealed, Version: 1}
	}
	return endpoint, nil
}

// created answers a made or renewed endpoint, handing its secret on only
// when it is a connection endpoint's, which the app must be given.
func (s *Service) created(endpoint Endpoint, secret string) Created {
	hook := integration.Hook{URL: s.URL(endpoint.ID)}
	if endpoint.Scope == integration.HookScopeConnection {
		hook.Secret = secret
	}
	return Created{Endpoint: endpoint, URL: hook.URL, Hook: hook}
}

var idEncoding = base32.StdEncoding.WithPadding(base32.NoPadding)

// newID is prefix and 120 random bits, as apps' own ids are.
func newID(random io.Reader, prefix string) (string, error) {
	value := make([]byte, 15)
	if _, err := io.ReadFull(random, value); err != nil {
		return "", fmt.Errorf("generate an id: %w", err)
	}
	return prefix + strings.ToLower(idEncoding.EncodeToString(value)), nil
}

func utc(at *time.Time) *time.Time {
	if at == nil {
		return nil
	}
	value := at.UTC()
	return &value
}
