package connect

import (
	"context"
	"errors"
	"net/http"
	"net/url"

	"github.com/jackc/pgx/v5"

	eventsv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/integrations/v1"

	"github.com/Shubham04-oss/oryxa-go/integrations/internal/orgapps"
	"github.com/Shubham04-oss/oryxa-go/integrations/manifest"
)

// Revoke removes a connection: it revokes the token at the app (RFC 7009,
// the refresh token when there is one, since that ends the whole grant),
// forgets the sealed tokens and marks the connection revoked. The app not
// answering never blocks the removal; providerRevoked says whether it
// confirmed, and is kept on the connection. It is idempotent.
func (s *Service) Revoke(ctx context.Context, connection string) (providerRevoked bool, err error) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), refreshTimeout)
	defer cancel()
	// Before the lock, as Refresh does: a waiter holds a pooled connection.
	appID, client, err := s.clientOf(ctx, connection)
	if err != nil {
		return false, err
	}
	tx, err := s.lock(ctx, connection)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // a rollback after commit is a no-op
	c, err := s.load(ctx, tx, connection)
	if err != nil {
		return false, err
	}
	if c.status == "revoked" {
		return false, nil
	}
	// At the app first: a crash after it leaves a connection that still
	// works to remove again, rather than a token left live with nothing to
	// revoke it by.
	if m, ok := s.config.Manifest(c.app); ok && m.Auth.OAuth2 != nil {
		if c.appID == appID {
			providerRevoked = s.revokeAtApp(ctx, m, client, grant{access: c.access, refresh: c.refresh})
		}
	}
	if err := s.remove(ctx, tx, c.id, providerRevoked); err != nil {
		return false, err
	}
	return providerRevoked, tx.Commit(ctx)
}

// remove forgets a connection's sealed tokens, marks it revoked, recording
// whether the app confirmed, and writes its removed event, all in tx.
func (s *Service) remove(ctx context.Context, tx pgx.Tx, connection string, providerRevoked bool) error {
	if _, err := tx.Exec(ctx, `DELETE FROM integrations.credentials WHERE connection_id = $1`, connection); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE integrations.connections SET status = 'revoked', provider_revoked = $2, updated_at = $3
		WHERE id = $1`, connection, providerRevoked, s.now()); err != nil {
		return err
	}
	return s.emit(ctx, tx, connection, eventsv1.ConnectionRemovedDetailType)
}

// revokeHere removes a connection without asking the app, for when Revoke
// could not: the app's client or the connection's tokens cannot be opened.
// The member loses access all the same; the app's grant is left to expire.
// It is idempotent.
func (s *Service) revokeHere(ctx context.Context, connection string) error {
	tx, err := s.lock(ctx, connection)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // a rollback after commit is a no-op
	var status string
	err = tx.QueryRow(ctx, `SELECT status FROM integrations.connections WHERE id = $1`, connection).Scan(&status)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return nil
	case err != nil:
		return err
	case status == "revoked":
		return nil
	}
	if err := s.remove(ctx, tx, connection, false); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// revokeAtApp asks the app to revoke issued, best effort, and says whether it
// confirmed. An app with no revoke_url cannot.
func (s *Service) revokeAtApp(ctx context.Context, m *manifest.Manifest, client orgapps.ClientCredentials, issued grant) bool {
	if m.Auth.OAuth2 == nil || m.Auth.OAuth2.RevokeURL == "" {
		return false
	}
	token, hint := issued.access, "access_token"
	if issued.refresh != "" {
		token, hint = issued.refresh, "refresh_token"
	}
	if token == "" {
		return false
	}
	status, _, err := s.post(ctx, m.Auth.OAuth2.RevokeURL, url.Values{
		"token": {token.Reveal()}, "token_type_hint": {hint},
		"client_id": {client.ClientID}, "client_secret": {client.Secret.Reveal()},
	})
	if err != nil || status != http.StatusOK {
		s.config.Logger.WarnContext(ctx, "revoke a token at the app", "app", m.ID, "status", status, "error", err)
		return false
	}
	return true
}
