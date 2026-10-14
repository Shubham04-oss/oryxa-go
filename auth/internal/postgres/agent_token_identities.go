package postgres

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/Shubham04-oss/oryxa-go/auth/internal/domain"
)

// BindAgentToken records which agent the token with tokenHash acts as, until
// it expires, and drops bindings whose tokens already have.
func (store *Store) BindAgentToken(ctx context.Context, tokenHash, userID string, identity domain.AgentIdentity, expiresAt time.Time) error {
	return pgx.BeginFunc(ctx, store.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `DELETE FROM auth_agent_token_identities WHERE expires_at<=now()`); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `
			INSERT INTO auth_agent_token_identities (token_hash, user_id, agent_id, role, expires_at)
			VALUES ($1, $2, $3, $4, $5)
			ON CONFLICT (token_hash) DO NOTHING`, tokenHash, userID, identity.Agent, identity.Role, expiresAt)
		if err != nil {
			return mapWriteError("bind agent token", err)
		}
		return nil
	})
}

// AgentTokenIdentity answers which agent the member userID's token with
// tokenHash acts as, or domain.ErrNotFound when it has no live binding.
func (store *Store) AgentTokenIdentity(ctx context.Context, tokenHash, userID string) (domain.AgentIdentity, error) {
	var identity domain.AgentIdentity
	err := store.pool.QueryRow(ctx, `
		SELECT agent_id, role FROM auth_agent_token_identities
		WHERE token_hash = $1 AND user_id = $2 AND expires_at > now()`, tokenHash, userID).Scan(&identity.Agent, &identity.Role)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.AgentIdentity{}, domain.ErrNotFound
	}
	return identity, err
}
