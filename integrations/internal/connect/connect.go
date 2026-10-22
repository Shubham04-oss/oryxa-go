// Package connect signs members in to apps over OAuth 2 and keeps the tokens
// alive (design §9): it starts a sign-in with a hashed, single-use state and
// PKCE, finishes it at the callback, refreshes tokens under a per-connection
// lock, and revokes them at the app when a connection is removed.
//
// Ported from apps/internal/signin (oauth.go, refresh.go, revoke.go, health.go).
// Not yet here: several token kinds per sign-in (Slack's bot and user), the
// refresh job (a connection refreshes when it is used); see the follow-up
// issues. Its connection events go to the outbox in the transaction that
// changes the connection (events.go).
package connect

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"
	"unicode"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	eventsv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/integrations/v1"

	"github.com/Shubham04-oss/oryxa-go/integrations/internal/orgapps"
	"github.com/Shubham04-oss/oryxa-go/integrations/internal/vault"
	"github.com/Shubham04-oss/oryxa-go/integrations/manifest"
	"github.com/Shubham04-oss/oryxa-go/internal/safehttp"
	"github.com/Shubham04-oss/oryxa-go/internal/secrets"
)

var (
	// ErrInvalid means the request is not acceptable; the error says which field.
	ErrInvalid = errors.New("connect: invalid request")
	// ErrNoConnector means the app has no manifest that signs in with OAuth 2.
	ErrNoConnector = errors.New("connect: the app does not sign in with OAuth")
	// ErrBadState means the state is unknown, already used or expired.
	ErrBadState = errors.New("connect: the sign-in is unknown, used or expired")
	// ErrNotFound means there is no such connection.
	ErrNotFound = errors.New("connect: no such connection")
	// ErrNeedsReauth means the app refused the grant, or none is left to
	// refresh with: the member must sign in again.
	ErrNeedsReauth = errors.New("connect: the connection needs to be signed in again")
	// ErrRevoked means the connection was removed.
	ErrRevoked = errors.New("connect: the connection was removed")
	// ErrCooldown means a recent refresh failed and the app is not asked again yet.
	ErrCooldown = errors.New("connect: the connection is cooling down after a failed refresh")
	// ErrUnsupportedKind means a token kind other than the access token was asked for.
	ErrUnsupportedKind = errors.New("connect: only the access token is kept")
)

// Why a sign-in failed, as the callback tells the browser.
const (
	ReasonDenied      = "denied"
	ReasonUnavailable = "unavailable"
	ReasonInvalid     = "invalid_credentials"
)

// Failure is why a sign-in that had a state ended without a connection.
type Failure struct {
	Reason string
	cause  error
}

func (f *Failure) Error() string {
	if f.cause == nil {
		return "connect: sign-in failed: " + f.Reason
	}
	return "connect: sign-in failed: " + f.Reason + ": " + f.cause.Error()
}

func (f *Failure) Unwrap() error { return f.cause }

const (
	// StateLifetime is how long a member has to finish a sign-in.
	StateLifetime = 10 * time.Minute
	// refreshAhead is how long before expiry a token is refreshed.
	refreshAhead = 10 * time.Minute
	// degradedAfter is how many failed refreshes in a row make a connection degraded.
	degradedAfter = 5
	maxCooldown   = 30 * time.Minute
	// completeTimeout bounds a callback once its state is spent: it is the
	// sign-in's one chance, so it finishes even if the browser goes away.
	completeTimeout = 45 * time.Second
	refreshTimeout  = 30 * time.Second
	maxID           = 128
	accessKind      = "access"
	refreshKind     = "refresh"
)

// Config is what the service needs besides the database.
type Config struct {
	// RedirectURI is this service's callback, as registered with each app.
	RedirectURI string
	// ReturnOrigins are the origins (scheme and host) a sign-in may send the
	// browser back to. A return_to elsewhere is refused.
	ReturnOrigins []string
	// Manifest answers an app's manifest.
	Manifest func(app string) (*manifest.Manifest, bool)
	// Client calls the apps' token and revoke endpoints. Nil means a safehttp
	// client that is https only and follows no redirect.
	Client *http.Client
	Logger *slog.Logger
	// Now is the clock; nil means time.Now.
	Now func() time.Time
}

// Service connects members to apps.
type Service struct {
	pool   *pgxpool.Pool
	sealer vault.Sealer
	apps   *orgapps.Service
	config Config
}

// New returns a Service. The apps service chooses the app a connect goes
// through and opens its secret.
func New(pool *pgxpool.Pool, sealer vault.Sealer, apps *orgapps.Service, config Config) *Service {
	if config.Client == nil {
		config.Client = safehttp.NewClient(safehttp.HTTPSOnly(), safehttp.MaxRedirects(0), safehttp.Timeout(20*time.Second))
	}
	if config.Logger == nil {
		config.Logger = slog.New(slog.DiscardHandler)
	}
	if config.Now == nil {
		config.Now = time.Now
	}
	return &Service{pool: pool, sealer: sealer, apps: apps, config: config}
}

func (s *Service) now() time.Time { return s.config.Now().UTC().Truncate(time.Microsecond) }

// StartRequest is a member (or an admin, for an org-level connection)
// starting a sign-in.
type StartRequest struct {
	Org      string
	Member   string
	App      string
	Level    string // personal | org
	ReturnTo string
}

// Start records a sign-in and answers the app's authorization URL. The app is
// the org's own if it has one, else Oryxa's (design §9.1). The state is 32
// random bytes of which only the SHA-256 is kept.
func (s *Service) Start(ctx context.Context, request StartRequest) (string, error) {
	for field, value := range map[string]string{"org": request.Org, "member": request.Member, "app": request.App} {
		if err := checkID(field, value); err != nil {
			return "", err
		}
	}
	if request.Level != "personal" && request.Level != "org" {
		return "", invalid("level", "must be personal or org")
	}
	if request.ReturnTo != "" && !s.returnAllowed(request.ReturnTo) {
		return "", invalid("return_to", "is not an allowed page")
	}
	m, ok := s.config.Manifest(request.App)
	if !ok || m.Auth.OAuth2 == nil || strings.ContainsAny(m.Auth.OAuth2.AuthorizationURL, "{}") {
		return "", fmt.Errorf("%w: %s", ErrNoConnector, request.App)
	}
	if !slices.Contains(m.ConnectionLevels, request.Level) {
		return "", invalid("level", request.App+" does not take it")
	}
	chosen, err := s.apps.Choose(ctx, request.Org, request.App)
	if err != nil {
		return "", err
	}

	state, err := randomToken(32)
	if err != nil {
		return "", err
	}
	hash := hashState(state)
	query := url.Values{}
	// What the manifest adds first, so it can never replace what the flow needs.
	for name, value := range m.Auth.OAuth2.AuthorizationParams {
		query.Set(name, value)
	}
	query.Set("response_type", "code")
	query.Set("client_id", chosen.ClientID)
	query.Set("redirect_uri", s.config.RedirectURI)
	query.Set("state", state)
	if scopes := scopesOf(m.Auth.OAuth2); len(scopes) > 0 {
		separator := m.Auth.OAuth2.ScopeSeparator
		if separator == "" {
			separator = " "
		}
		query.Set("scope", strings.Join(scopes, separator))
	}
	var sealedVerifier *secrets.SealedData
	if m.Auth.OAuth2.PKCE {
		verifier, err := randomToken(32)
		if err != nil {
			return "", err
		}
		challenge := sha256.Sum256([]byte(verifier))
		query.Set("code_challenge", base64.RawURLEncoding.EncodeToString(challenge[:]))
		query.Set("code_challenge_method", "S256")
		sealed, err := s.sealer.Seal(ctx, verifierWhere(request.Org, hash), []byte(verifier))
		if err != nil {
			return "", err
		}
		sealedVerifier = &sealed
	}
	if _, err := s.pool.Exec(ctx, `INSERT INTO integrations.oauth_states
		(state_hash, org_id, member_id, app, org_app_id, pkce_verifier, return_to, level, expires_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)`,
		hash, request.Org, request.Member, request.App, chosen.ID, sealedVerifier, request.ReturnTo, request.Level,
		s.now().Add(StateLifetime)); err != nil {
		return "", err
	}
	authorize, err := url.Parse(m.Auth.OAuth2.AuthorizationURL)
	if err != nil {
		return "", fmt.Errorf("%w: %s: %w", ErrNoConnector, request.App, err)
	}
	merged := authorize.Query()
	for name, values := range query {
		merged[name] = values
	}
	authorize.RawQuery = merged.Encode()
	return authorize.String(), nil
}

// scopesOf is every scope of the token kinds a sign-in must yield.
func scopesOf(o *manifest.OAuth2) []string {
	var scopes []string
	for _, kind := range o.TokenKinds {
		if kind.Optional {
			continue
		}
		for _, scope := range kind.Scopes {
			if !slices.Contains(scopes, scope) {
				scopes = append(scopes, scope)
			}
		}
	}
	return scopes
}

// Callback is what the member's browser brought back.
type Callback struct {
	State string
	Code  string
	// Error is the app's error parameter (access_denied) when it sent one.
	Error string
}

// Completion is how a callback ended. Once the state is found, ReturnTo names
// the page to send the browser to, even when the error is not nil.
type Completion struct {
	ConnectionID string
	App          string
	ReturnTo     string
}

// Complete spends the state, exchanges the code, tests the connection, and
// keeps the sealed tokens. A state that is unknown, used or expired is
// ErrBadState and sends the browser nowhere. Anything else that fails is a
// *Failure, and the tokens the app issued are revoked, best effort.
func (s *Service) Complete(ctx context.Context, callback Callback) (Completion, error) {
	if callback.State == "" {
		return Completion{}, ErrBadState
	}
	var (
		org, member, app, appID, returnTo, level string
		sealedVerifier                           *secrets.SealedData
	)
	hash := hashState(callback.State)
	now := s.now()
	err := s.pool.QueryRow(ctx, `UPDATE integrations.oauth_states SET used_at = $2
		WHERE state_hash = $1 AND used_at IS NULL AND expires_at > $2
		RETURNING org_id, member_id, app, org_app_id, pkce_verifier, return_to, level`, hash, now).
		Scan(&org, &member, &app, &appID, &sealedVerifier, &returnTo, &level)
	if errors.Is(err, pgx.ErrNoRows) {
		return Completion{}, ErrBadState
	}
	if err != nil {
		return Completion{}, err
	}
	// The state is spent, so this is the sign-in's one chance.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), completeTimeout)
	defer cancel()
	completion := Completion{App: app}
	// Checked again: the callback must never send a browser anywhere it has not just checked.
	if s.returnAllowed(returnTo) {
		completion.ReturnTo = returnTo
	}
	fail := func(reason string, cause error) (Completion, error) {
		s.config.Logger.WarnContext(ctx, "app sign-in failed", "app", app, "org", org, "reason", reason, "error", cause)
		return completion, &Failure{Reason: reason, cause: cause}
	}

	if callback.Error != "" || callback.Code == "" {
		return fail(ReasonDenied, errors.New("the app sent back no code"))
	}
	m, ok := s.config.Manifest(app)
	if !ok || m.Auth.OAuth2 == nil {
		return fail(ReasonUnavailable, ErrNoConnector)
	}
	client, err := s.apps.Credentials(ctx, appID)
	if err != nil {
		return fail(ReasonUnavailable, err)
	}
	form := url.Values{"grant_type": {"authorization_code"}, "code": {callback.Code}, "redirect_uri": {s.config.RedirectURI}}
	if sealedVerifier != nil {
		verifier, err := s.sealer.Open(ctx, verifierWhere(org, hash), *sealedVerifier)
		if err != nil {
			return fail(ReasonUnavailable, err)
		}
		form.Set("code_verifier", verifier.Reveal())
	}
	issued, err := s.token(ctx, m, client, form)
	if err != nil {
		if errors.Is(err, errUnavailable) {
			return fail(ReasonUnavailable, err)
		}
		return fail(ReasonDenied, err)
	}
	discard := func(reason string, cause error) (Completion, error) {
		s.revokeAtApp(ctx, m, client, issued)
		return fail(reason, cause)
	}
	if err := s.verify(ctx, m, issued.access); err != nil {
		if errors.Is(err, errUnavailable) {
			return discard(ReasonUnavailable, err)
		}
		return discard(ReasonInvalid, err)
	}
	id, err := s.keep(ctx, org, member, app, appID, level, issued, m)
	if err != nil {
		return discard(ReasonUnavailable, err)
	}
	completion.ConnectionID = id
	s.config.Logger.InfoContext(ctx, "app signed in", "connection_id", id, "app", app)
	return completion, nil
}

// keep saves the connection and its sealed tokens. A member who signs in
// again to the same app at the same level renews the connection they have,
// pinned to the app just used.
func (s *Service) keep(ctx context.Context, org, member, app, appID, level string, issued grant, m *manifest.Manifest) (string, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return "", err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // a rollback after commit is a no-op
	memberArg := &member
	if level == "org" {
		memberArg = nil
	}
	now := s.now()
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`,
		"connect:"+org+"/"+member+"/"+app+"/"+level); err != nil {
		return "", err
	}
	scopes := issued.scopes
	if scopes == nil {
		scopes = scopesOf(m.Auth.OAuth2)
	}
	var id, previous string
	err = tx.QueryRow(ctx, `SELECT id, status FROM integrations.connections
		WHERE org_id = $1 AND member_id IS NOT DISTINCT FROM $2 AND app = $3 AND level = $4 AND status <> 'revoked'
		FOR UPDATE`, org, memberArg, app, level).Scan(&id, &previous)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		if id, err = newID("cn_"); err != nil {
			return "", err
		}
		mode := "oryxa"
		if err := tx.QueryRow(ctx, `SELECT CASE WHEN org_id IS NULL THEN 'oryxa' ELSE 'byo' END FROM integrations.org_apps WHERE id = $1`, appID).Scan(&mode); err != nil {
			return "", err
		}
		_, err = tx.Exec(ctx, `INSERT INTO integrations.connections
			(id, org_id, member_id, level, app, org_app_id, mode, scopes, status, last_ok_at, created_at, updated_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, 'ok', $9, $9, $9)`, id, org, memberArg, level, app, appID, mode, scopes, now)
	case err == nil:
		_, err = tx.Exec(ctx, `UPDATE integrations.connections SET org_app_id = $2, scopes = $3, status = 'ok',
			failures = 0, cooldown_until = NULL, last_ok_at = $4, updated_at = $4 WHERE id = $1`, id, appID, scopes, now)
	}
	if err != nil {
		return "", err
	}
	if err := s.saveToken(ctx, tx, org, id, accessKind, issued.access, issued.expiresAt); err != nil {
		return "", err
	}
	if issued.refresh != "" {
		if err := s.saveToken(ctx, tx, org, id, refreshKind, issued.refresh, nil); err != nil {
			return "", err
		}
	} else if _, err := tx.Exec(ctx, `DELETE FROM integrations.credentials WHERE connection_id = $1 AND kind = $2`, id, refreshKind); err != nil {
		return "", err
	}
	// A renewal of a healthy connection changes nothing a consumer sees.
	switch {
	case previous == "":
		err = s.emit(ctx, tx, id, eventsv1.ConnectionCreatedDetailType)
	case previous != "ok":
		err = s.emit(ctx, tx, id, eventsv1.ConnectionUpdatedDetailType)
	}
	if err != nil {
		return "", err
	}
	return id, tx.Commit(ctx)
}

func (s *Service) returnAllowed(returnTo string) bool {
	if returnTo == "" {
		return false
	}
	u, err := url.Parse(returnTo)
	if err != nil || u.Host == "" || u.User != nil || (u.Scheme != "https" && u.Scheme != "http") {
		return false
	}
	return slices.Contains(s.config.ReturnOrigins, u.Scheme+"://"+u.Host)
}

func credentialWhere(org, connection, kind string) vault.Where {
	return vault.Where{Table: "credentials", Org: org, Row: connection + "/" + kind}
}

func verifierWhere(org, stateHash string) vault.Where {
	return vault.Where{Table: "oauth_states", Org: org, Row: stateHash + "/pkce_verifier"}
}

func hashState(state string) string {
	sum := sha256.Sum256([]byte(state))
	return hex.EncodeToString(sum[:])
}

func randomToken(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func newID(prefix string) (string, error) {
	var b [12]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return prefix + hex.EncodeToString(b[:]), nil
}

func checkID(field, value string) error {
	if value == "" || len(value) > maxID || strings.ContainsFunc(value, func(r rune) bool {
		return r == '/' || unicode.IsSpace(r) || unicode.IsControl(r)
	}) {
		return invalid(field, "must be one word without '/' of at most 128 characters")
	}
	return nil
}

func invalid(field, rule string) error {
	return fmt.Errorf("%w: %s %s", ErrInvalid, field, rule)
}
