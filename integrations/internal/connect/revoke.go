package connect

import (
	"context"
	"net/http"
	"net/url"

	eventsv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/integrations/v1"

	"github.com/Shubham04-oss/oryxa-go/integrations/internal/orgapps"
	"github.com/Shubham04-oss/oryxa-go/integrations/manifest"
)

// Revoke removes a connection: it revokes the token at the app (RFC 7009,
// the refresh token when there is one, since that ends the whole grant),
// forgets the sealed tokens and marks the connection revoked. The app not
// answering never blocks the removal; ProviderRevoked says whether it
// confirmed. It is idempotent.
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
	if _, err := tx.Exec(ctx, `DELETE FROM integrations.credentials WHERE connection_id = $1`, c.id); err != nil {
		return false, err
	}
	if _, err := tx.Exec(ctx, `UPDATE integrations.connections SET status = 'revoked', updated_at = $2 WHERE id = $1`, c.id, s.now()); err != nil {
		return false, err
	}
	if err := s.emit(ctx, tx, c.id, eventsv1.ConnectionRemovedDetailType); err != nil {
		return false, err
	}
	return providerRevoked, tx.Commit(ctx)
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
