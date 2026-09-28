// Package signin is Oryxa's own sign-in and token system: how a member
// connects an app through Oryxa rather than Composio, and everything that
// happens to the credential afterwards.
//
// It signs a member in with OAuth 2.0 (a code, with PKCE) or takes a key, a
// password or a client's credentials; proves every credential with its
// manifest's test call before keeping it; seals each secret value on its own
// through internal/secrets, bound to service oryxa-apps and the resource it
// belongs to; places the credential on an integration's requests from the
// innermost transport, for the credential's own host only; refreshes OAuth
// tokens under a lease, so one refresh runs at a time per connection across
// every replica; revokes at the app on disconnect; checks idle connections
// daily; and keeps each organization's own OAuth clients.
//
// It works by connection ID. Apps' application layer creates the pending
// connection with broker oryxa, calls in here, marks the connection active,
// failed or expired from what signin answers, and writes the events. signin
// writes no events and never changes a connection's status.
package signin

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base32"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"

	"github.com/Shubham04-oss/oryxa-go/apps/internal/domain"
	"github.com/Shubham04-oss/oryxa-go/apps/internal/integration"
	"github.com/Shubham04-oss/oryxa-go/internal/observability"
	"github.com/Shubham04-oss/oryxa-go/internal/safehttp"
	"github.com/Shubham04-oss/oryxa-go/internal/secrets"
)

// SealService is the service every value signin seals is bound to. The
// vault key's policy grants apps its key on this encryption context alone.
const SealService = "oryxa-apps"

// FlowLifetime is how long a member has, from StartOAuth, to come back
// through the callback.
const FlowLifetime = 10 * time.Minute

const (
	// refreshBefore is how long before an access token expires it is
	// renewed, and how long before then the cache lets go of it.
	refreshBefore = 2 * time.Minute
	// cacheMaxAge bounds how long a credential that never expires stays
	// unsealed in memory.
	cacheMaxAge = time.Hour
	// checkInterval is how long a healthy connection waits for its next
	// check, give or take a tenth.
	checkInterval = 24 * time.Hour
	// recentUse is how long a successful call counts as the connection's
	// check.
	recentUse = 12 * time.Hour
	// minBackoff and maxBackoff bound the wait after a check or a revocation
	// the app did not answer: 5 minutes, doubling up to 6 hours.
	minBackoff = 5 * time.Minute
	maxBackoff = 6 * time.Hour
	// claimFor is how long a claimed check or revocation is left to the
	// replica that claimed it before another may take it.
	claimFor = 10 * time.Minute
	// maxRevokeAttempts is how many times a revocation the app did not
	// answer is tried before the credential is forgotten anyway.
	maxRevokeAttempts = 8
	// markUseEvery is how often, per process, a connection's successful use
	// is written down.
	markUseEvery = 30 * time.Minute
	// callTimeout bounds one call to an app's token, revoke or test
	// endpoint.
	callTimeout = 20 * time.Second
	// completeTimeout bounds finishing a callback once its flow is spent:
	// the code exchange, the test call, sealing and saving.
	completeTimeout = time.Minute
	// maxRenewRounds is a fuse on waiting for renewals: a credential that
	// keeps moving on without ever becoming usable stops being waited for.
	maxRenewRounds = 200
	// maxAnswerBytes bounds what is read of an app's answer.
	maxAnswerBytes = 1 << 20
)

// timings are the waits a refresh is made of. Tests shorten them.
type timings struct {
	// lease is how long a refresh holds a credential's lease.
	lease time.Duration
	// refresh bounds a refresh: the app's answer, sealing it and saving it.
	refresh time.Duration
	// leaseWait is how long a caller waits for another's refresh.
	leaseWait time.Duration
	// leasePoll is how often a waiting caller looks again.
	leasePoll time.Duration
}

var defaultTimings = timings{
	lease:     time.Minute,
	refresh:   40 * time.Second,
	leaseWait: 10 * time.Second,
	leasePoll: 200 * time.Millisecond,
}

// Why a sign-in failed: a Failure's Reason. The first three are the
// ConnectionFailed reasons the bus carries.
const (
	// ReasonDenied is the member refusing at the app, or the app refusing
	// the code it sent back.
	ReasonDenied = "denied_at_provider"
	// ReasonWrongAccount is a callback that is not the flow's member's.
	ReasonWrongAccount = "wrong_account"
	// ReasonInvalidCredentials is a field that breaks its rule, or a
	// credential the app refused in its test call.
	ReasonInvalidCredentials = "invalid_credentials"
	// ReasonUnavailable is an app, or a store, that did not answer. Nothing
	// was kept.
	ReasonUnavailable = "unavailable"
)

// Why a connection needs reconnecting: a Rejected's Reason, and
// ConnectionExpired's reason on the bus.
const (
	// ExpiredRefreshRejected is an OAuth refresh the app refused
	// (invalid_grant), or an OAuth token that expired with nothing to renew
	// it.
	ExpiredRefreshRejected = "refresh_rejected"
	// ExpiredCredentialRejected is a credential the app refused: a key or a
	// token its test call answered 401 to, or a client whose credentials no
	// longer mint a token.
	ExpiredCredentialRejected = "credential_rejected"
)

var (
	// ErrBadState means a callback's state is missing, or is not the one its
	// cookie holds. Nothing was spent.
	ErrBadState = fmt.Errorf("signin: the callback's state does not match its cookie: %w", domain.ErrForbidden)
	// ErrFlowNotFound means no sign-in is waiting on a callback's state: it
	// was spent, it expired, or it never was.
	ErrFlowNotFound = fmt.Errorf("signin: the sign-in is spent, expired or unknown: %w", domain.ErrNotFound)
	// ErrNoCredential means a connection holds no credential.
	ErrNoCredential = fmt.Errorf("signin: the connection holds no credential: %w", domain.ErrNotFound)
	// ErrNotActive means a connection is not active, so nothing may be sent
	// with its credential.
	ErrNotActive = fmt.Errorf("signin: the connection is not active: %w", domain.ErrNotConnected)
	// ErrNotPending means a sign-in finished for a connection that no longer
	// waits for one: removed, failed, or already holding a credential. What
	// the app issued was revoked rather than kept.
	ErrNotPending = fmt.Errorf("signin: the connection no longer waits for this sign-in: %w", domain.ErrConflict)
	// ErrNoClient means there is no OAuth client to sign in through: the
	// organization has none of its own and Oryxa has no default, or the
	// organization refused Oryxa's.
	ErrNoClient = fmt.Errorf("signin: no OAuth client to sign in through: %w", domain.ErrNotConnectable)
	// ErrUnknownToolkit means no integration is registered for a toolkit.
	ErrUnknownToolkit = fmt.Errorf("signin: no integration is registered for the toolkit: %w", domain.ErrNotFound)
	// ErrUnsupportedKind means the toolkit does not sign in that way, or not
	// through the call it was asked of.
	ErrUnsupportedKind = fmt.Errorf("signin: the toolkit does not sign in that way: %w", domain.ErrInvalid)
	// ErrReadOnly means a read tool's session was asked to write. Nothing
	// was sent.
	ErrReadOnly = fmt.Errorf("signin: a read tool may not write: %w", domain.ErrNotReadOnly)
	// ErrUnavailable means an app did not answer, or answered in a way that
	// says to try again later.
	ErrUnavailable = fmt.Errorf("signin: the app did not answer: %w", domain.ErrUnavailable)
)

// Failure is a sign-in that ended without its credential being kept. Reason
// says why, as one of the Reason constants. Field names the field to blame
// when one is, and Rule what it must be. Neither ever quotes a value, so a
// Failure is safe to log and to answer.
type Failure struct {
	Reason string
	Field  string
	Rule   string
	cause  error
}

func (f *Failure) Error() string {
	message := "signin: " + f.Reason
	if f.Field != "" {
		message += ": " + f.Field + " " + f.Rule
	} else if f.Rule != "" {
		message += ": " + f.Rule
	}
	if f.cause != nil {
		message += ": " + f.cause.Error()
	}
	return message
}

// Unwrap answers the domain error a Failure is, for callers that map errors
// by kind, and its cause.
func (f *Failure) Unwrap() []error {
	kind := domain.ErrInvalid
	switch f.Reason {
	case ReasonUnavailable:
		kind = domain.ErrUnavailable
	case ReasonWrongAccount:
		kind = domain.ErrForbidden
	}
	if f.cause == nil {
		return []error{kind}
	}
	return []error{kind, f.cause}
}

// invalidField is the Failure a field that breaks its rule gets.
func invalidField(field, rule string) *Failure {
	return &Failure{Reason: ReasonInvalidCredentials, Field: field, Rule: rule}
}

// Rejected is a connection's credential the app has refused for good: its
// refresh token, or the credential itself. The connection needs
// reconnecting, and Reason is ConnectionExpired's reason. It is
// integration.ErrNeedsReconnect, and domain.ErrNeedsReconnect, to errors.Is.
type Rejected struct {
	Reason string
	cause  error
}

func (r *Rejected) Error() string {
	message := "signin: the connection needs reconnecting (" + r.Reason + ")"
	if r.cause != nil {
		message += ": " + r.cause.Error()
	}
	return message
}

func (r *Rejected) Unwrap() []error {
	errs := []error{integration.ErrNeedsReconnect, domain.ErrNeedsReconnect}
	if r.cause != nil {
		errs = append(errs, r.cause)
	}
	return errs
}

// ReconnectReason answers why err says a connection needs reconnecting, as
// ConnectionExpired's reason, or "" when it does not say so.
func ReconnectReason(err error) string {
	var rejected *Rejected
	if errors.As(err, &rejected) {
		return rejected.Reason
	}
	return ""
}

// Config is what signin needs to know about where it runs.
type Config struct {
	// RedirectURI is the one callback every app sends a member back to,
	// {public base URL}/v1/apps/oauth/callback. It is registered with each
	// OAuth app, stored with each flow and sent back unchanged.
	RedirectURI string
	// ReturnAllowed says whether a member's browser may be sent to a page
	// once they finish: apps' returnAllowed. StartOAuth checks return_to
	// with it, and CompleteOAuth checks it again. Without it, only an empty
	// return_to is accepted.
	ReturnAllowed func(returnTo string) bool
}

// Dependencies are what signin borrows.
type Dependencies struct {
	// Store is apps' Postgres store. Required.
	Store Store
	// Sealer seals and opens every secret value: a secrets.Envelope whose
	// Service is SealService. Required.
	Sealer Sealer
	// Integrations are the manifests, by toolkit: apps' integration
	// registry. Required.
	Integrations Manifests
	// HTTP is the client every call to an app starts from. Without one,
	// signin uses safehttp.NewClient(safehttp.HTTPSOnly()): the public
	// internet only, over HTTPS only. Its Transport is the innermost layer
	// and its CheckRedirect decides redirects; signin places credentials
	// between that transport and observability.Transport.
	HTTP   *http.Client
	Logger *slog.Logger
	Clock  func() time.Time
}

// Service is the sign-in and token system.
type Service struct {
	store     Store
	sealer    Sealer
	manifests Manifests
	config    Config
	logger    *slog.Logger
	now       func() time.Time

	// base is the innermost transport; checkRedirect and timeout are the
	// base client's.
	base          http.RoundTripper
	checkRedirect func(*http.Request, []*http.Request) error
	timeout       time.Duration
	// endpoints calls token and revocation endpoints: traced, and never
	// following a redirect, since those requests carry a client's secret.
	endpoints *http.Client

	cache     *tokenCache
	refreshes singleflight.Group
	// used is when each connection's use was last written down, by this
	// process.
	used    sync.Map
	timings timings
}

// New builds the service. It checks what it is given and touches nothing.
func New(dependencies Dependencies, config Config) (*Service, error) {
	if dependencies.Store == nil || dependencies.Sealer == nil || dependencies.Integrations == nil {
		return nil, errors.New("signin: a store, a sealer and the integrations are required")
	}
	if err := checkSealer(dependencies.Sealer); err != nil {
		return nil, err
	}
	if err := checkRedirectURI(config.RedirectURI); err != nil {
		return nil, err
	}
	logger := dependencies.Logger
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	clock := dependencies.Clock
	if clock == nil {
		clock = time.Now
	}
	base := dependencies.HTTP
	if base == nil {
		base = safehttp.NewClient(safehttp.HTTPSOnly())
	}
	transport := base.Transport
	if transport == nil {
		return nil, errors.New("signin: the HTTP client needs a Transport of its own")
	}
	return &Service{
		store:         dependencies.Store,
		sealer:        dependencies.Sealer,
		manifests:     dependencies.Integrations,
		config:        config,
		logger:        logger,
		now:           clock,
		base:          transport,
		checkRedirect: base.CheckRedirect,
		timeout:       base.Timeout,
		endpoints: &http.Client{
			// otelhttp records neither headers nor bodies, which is where
			// these requests carry the client's secret.
			Transport: observability.Transport(transport),
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
			Timeout: callTimeout,
		},
		cache:   newTokenCache(),
		timings: defaultTimings,
	}, nil
}

// checkSealer refuses an envelope bound to another service: what it sealed
// would not open under apps' key policy, and what it opened would not be
// apps'.
func checkSealer(sealer Sealer) error {
	var service string
	switch envelope := sealer.(type) {
	case secrets.Envelope:
		service = envelope.Service
	case *secrets.Envelope:
		service = envelope.Service
	default:
		return nil
	}
	if service != SealService {
		return fmt.Errorf("signin: the envelope must be bound to %s", SealService)
	}
	return nil
}

// checkRedirectURI accepts an absolute https URL, or http for a loopback
// host, as local development registers.
func checkRedirectURI(raw string) error {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Host == "" || parsed.User != nil || parsed.Fragment != "" || parsed.RawQuery != "" {
		return errors.New("signin: the redirect URI must be an absolute URL with no credentials, query or fragment")
	}
	switch parsed.Scheme {
	case "https":
		return nil
	case "http":
		host := parsed.Hostname()
		if ip := net.ParseIP(host); host == "localhost" || (ip != nil && ip.IsLoopback()) {
			return nil
		}
	}
	return errors.New("signin: the redirect URI must be https, or http on a loopback host")
}

// returnAllowed checks a return page with the configured rule.
func (s *Service) returnAllowed(returnTo string) bool {
	return returnTo == "" || (s.config.ReturnAllowed != nil && s.config.ReturnAllowed(returnTo))
}

// method answers a toolkit's manifest and its method of kind, or its first
// when kind is "".
func (s *Service) method(toolkit, kind string) (integration.Manifest, integration.Method, error) {
	registered, ok := s.manifests.Lookup(toolkit)
	if !ok {
		return integration.Manifest{}, integration.Method{}, ErrUnknownToolkit
	}
	manifest := registered.Manifest()
	if len(manifest.Auth.Methods) == 0 {
		return integration.Manifest{}, integration.Method{}, ErrUnsupportedKind
	}
	if kind == "" {
		return manifest, manifest.Auth.Methods[0], nil
	}
	for _, method := range manifest.Auth.Methods {
		if method.Kind == kind {
			return manifest, method, nil
		}
	}
	return integration.Manifest{}, integration.Method{}, ErrUnsupportedKind
}

// newState is a callback state and the hash kept of it. Only the member's
// browser and the app ever see the state itself.
func newState() (string, string, error) {
	state, err := randomToken(32)
	if err != nil {
		return "", "", err
	}
	return state, hashState(state), nil
}

func hashState(state string) string {
	sum := sha256.Sum256([]byte(state))
	return hex.EncodeToString(sum[:])
}

func randomToken(size int) (string, error) {
	value := make([]byte, size)
	if _, err := rand.Read(value); err != nil {
		return "", fmt.Errorf("signin: generate a random token: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(value), nil
}

var idEncoding = base32.StdEncoding.WithPadding(base32.NoPadding)

// newID is a stored row's id, spelled as apps spells its others: a prefix
// and 120 random bits.
func newID(prefix string) (string, error) {
	value := make([]byte, 15)
	if _, err := rand.Read(value); err != nil {
		return "", fmt.Errorf("signin: generate an id: %w", err)
	}
	return prefix + "_" + strings.ToLower(idEncoding.EncodeToString(value)), nil
}
