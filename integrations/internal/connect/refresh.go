package connect

import (
	"context"
	"errors"
	"net/url"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/Shubham04-oss/oryxa-go/integrations/internal/orgapps"
	"github.com/Shubham04-oss/oryxa-go/integrations/manifest"
	"github.com/Shubham04-oss/oryxa-go/internal/secrets"
)

// loaded is a connection and its tokens as the refresh and revoke read them.
type loaded struct {
	id, org, app, appID, status string
	failures                    int
	cooldownUntil               *time.Time
	access, refresh             secrets.Secret
	expiresAt                   *time.Time
	hasRefresh                  bool
}

// load reads connection id inside tx, which holds its lock, and opens its tokens.
func (s *Service) load(ctx context.Context, tx pgx.Tx, id string) (loaded, error) {
	c := loaded{id: id}
	err := tx.QueryRow(ctx, `SELECT org_id, app, org_app_id, status, failures, cooldown_until
		FROM integrations.connections WHERE id = $1`, id).
		Scan(&c.org, &c.app, &c.appID, &c.status, &c.failures, &c.cooldownUntil)
	if errors.Is(err, pgx.ErrNoRows) {
		return loaded{}, ErrNotFound
	}
	if err != nil {
		return loaded{}, err
	}
	rows, err := tx.Query(ctx, `SELECT kind, sealed, expires_at FROM integrations.credentials WHERE connection_id = $1`, id)
	if err != nil {
		return loaded{}, err
	}
	type stored struct {
		kind    string
		sealed  secrets.SealedData
		expires *time.Time
	}
	var all []stored
	for rows.Next() {
		var r stored
		if err := rows.Scan(&r.kind, &r.sealed, &r.expires); err != nil {
			rows.Close()
			return loaded{}, err
		}
		all = append(all, r)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return loaded{}, err
	}
	for _, r := range all {
		opened, err := s.sealer.Open(ctx, credentialWhere(c.org, id, r.kind), r.sealed)
		if err != nil {
			return loaded{}, err
		}
		switch r.kind {
		case accessKind:
			c.access, c.expiresAt = opened, r.expires
		case refreshKind:
			c.refresh, c.hasRefresh = opened, true
		}
	}
	return c, nil
}

// lock begins a transaction that holds the connection's lock until it ends.
// Refresh and revoke take it, so a refresh never rotates a token that is
// being revoked, and two refreshes never both ask the app.
func (s *Service) lock(ctx context.Context, id string) (pgx.Tx, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, "connection:"+id); err != nil {
		tx.Rollback(ctx) //nolint:errcheck // the lock error is the one to report
		return nil, err
	}
	return tx, nil
}

func (c loaded) due(now time.Time) bool {
	return c.expiresAt != nil && !now.Add(refreshAhead).Before(*c.expiresAt)
}

func (c loaded) usable() error {
	switch c.status {
	case "revoked":
		return ErrRevoked
	case "needs_reauth":
		return ErrNeedsReauth
	}
	return nil
}

// Refresh renews the connection's access token after stale was refused, and
// answers the new one (it implements egress.Connections). Callers are single
// flight: they queue on the connection's lock, and one that finds the token
// already moved on gets that token without asking the app again. An empty
// stale renews only a token that is expiring. A rotated refresh token is
// saved in the same transaction as the access token.
//
// invalid_grant marks the connection needs_reauth. Other failures back off:
// the connection cools down so it can't storm the app, and after five in a
// row it is degraded.
func (s *Service) Refresh(ctx context.Context, connection, kind string, stale secrets.Secret) (secrets.Secret, error) {
	if kind != accessKind && kind != "" {
		return "", ErrUnsupportedKind
	}
	// Once the app is asked, its answer is saved even if the caller has gone:
	// a rotated refresh token that is never saved is lost.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), refreshTimeout)
	defer cancel()
	// The client is read before the lock: a refresher that waits holds a
	// pooled connection, and the one that holds the lock must not need a
	// second, or a pool full of waiters would never finish.
	appID, client, err := s.clientOf(ctx, connection)
	if err != nil {
		return "", err
	}
	tx, err := s.lock(ctx, connection)
	if err != nil {
		return "", err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // a rollback after commit is a no-op
	c, err := s.load(ctx, tx, connection)
	if err != nil {
		return "", err
	}
	if err := c.usable(); err != nil {
		return "", err
	}
	now := s.now()
	// Signed in again meanwhile, through another app: its token is fresh.
	if c.appID != appID && c.access != "" {
		return c.access, nil
	}
	if c.access != "" && ((stale != "" && c.access != stale) || (stale == "" && !c.due(now))) {
		return c.access, nil
	}
	if c.cooldownUntil != nil && now.Before(*c.cooldownUntil) {
		return "", ErrCooldown
	}
	m, ok := s.config.Manifest(c.app)
	if !ok || m.Auth.OAuth2 == nil {
		return "", ErrNoConnector
	}
	if !c.hasRefresh {
		return "", s.needsReauth(ctx, tx, c, errors.New("there is no refresh token"))
	}
	issued, err := s.token(ctx, m, client, url.Values{"grant_type": {"refresh_token"}, "refresh_token": {c.refresh.Reveal()}})
	if err != nil {
		if errors.Is(err, errGrantRefused) {
			return "", s.needsReauth(ctx, tx, c, err)
		}
		return "", s.refreshFailed(ctx, tx, c, err)
	}
	if err := s.saveToken(ctx, tx, c.org, c.id, accessKind, issued.access, issued.expiresAt); err != nil {
		return "", err
	}
	if issued.refresh != "" && issued.refresh != c.refresh {
		if err := s.saveToken(ctx, tx, c.org, c.id, refreshKind, issued.refresh, nil); err != nil {
			return "", err
		}
	}
	if _, err := tx.Exec(ctx, `UPDATE integrations.connections SET status = 'ok', failures = 0, cooldown_until = NULL,
		last_ok_at = $2, updated_at = $2 WHERE id = $1`, c.id, now); err != nil {
		return "", err
	}
	return issued.access, tx.Commit(ctx)
}

func (s *Service) needsReauth(ctx context.Context, tx pgx.Tx, c loaded, cause error) error {
	s.config.Logger.WarnContext(ctx, "a connection needs to be signed in again", "connection_id", c.id, "app", c.app, "error", cause)
	if _, err := tx.Exec(ctx, `UPDATE integrations.connections SET status = 'needs_reauth', updated_at = $2 WHERE id = $1`, c.id, s.now()); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}
	return ErrNeedsReauth
}

// refreshFailed counts a failed refresh and sets the cooldown: a minute per
// failure in a row, at most half an hour. The fifth makes the connection degraded.
func (s *Service) refreshFailed(ctx context.Context, tx pgx.Tx, c loaded, cause error) error {
	failures := c.failures + 1
	status := "ok"
	if failures >= degradedAfter {
		status = "degraded"
	}
	cooldown := min(time.Duration(failures)*time.Minute, maxCooldown)
	s.config.Logger.WarnContext(ctx, "refresh an app token", "connection_id", c.id, "app", c.app, "failures", failures, "error", cause)
	now := s.now()
	if _, err := tx.Exec(ctx, `UPDATE integrations.connections SET failures = $2, status = $3, cooldown_until = $4, updated_at = $5
		WHERE id = $1`, c.id, failures, status, now.Add(cooldown), now); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}
	return cause
}

// clientOf answers the app a connection is pinned to and its client credentials.
func (s *Service) clientOf(ctx context.Context, connection string) (string, orgapps.ClientCredentials, error) {
	var appID string
	err := s.pool.QueryRow(ctx, `SELECT org_app_id FROM integrations.connections WHERE id = $1`, connection).Scan(&appID)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", orgapps.ClientCredentials{}, ErrNotFound
	}
	if err != nil {
		return "", orgapps.ClientCredentials{}, err
	}
	client, err := s.apps.Credentials(ctx, appID)
	return appID, client, err
}

// Token answers the connection's access token (it implements
// egress.Connections), refreshing it first when it expires within ten
// minutes. A token that has not expired is used even when the refresh fails.
func (s *Service) Token(ctx context.Context, connection, kind string) (secrets.Secret, error) {
	if kind != accessKind && kind != "" {
		return "", ErrUnsupportedKind
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return "", err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // read only
	c, err := s.load(ctx, tx, connection)
	if err != nil {
		return "", err
	}
	if err := c.usable(); err != nil {
		return "", err
	}
	if c.access == "" {
		return "", ErrNeedsReauth
	}
	if !c.due(s.now()) {
		return c.access, nil
	}
	fresh, err := s.Refresh(ctx, connection, accessKind, c.access)
	if err != nil && (errors.Is(err, ErrNeedsReauth) || c.expiresAt == nil || !s.now().Before(*c.expiresAt)) {
		return "", err
	}
	if err != nil {
		return c.access, nil
	}
	return fresh, nil
}

// Manifest answers the manifest of the app the connection is with (it
// implements egress.Connections).
func (s *Service) Manifest(ctx context.Context, connection string) (*manifest.Manifest, error) {
	var app string
	err := s.pool.QueryRow(ctx, `SELECT app FROM integrations.connections WHERE id = $1`, connection).Scan(&app)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	m, ok := s.config.Manifest(app)
	if !ok {
		return nil, ErrNoConnector
	}
	return m, nil
}
