package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/Shubham04-oss/oryxa-go/auth/internal/domain"
)

const agentGrantColumns = `user_id, encrypted_refresh_token, scope, created_at, refreshed_at`

// GetAgentGrant returns a member's agent grant, or domain.ErrNotFound when
// the member has not granted agent access.
func (store *Store) GetAgentGrant(ctx context.Context, userID string) (domain.AgentGrant, error) {
	return scanAgentGrant(store.pool.QueryRow(ctx, `
		SELECT `+agentGrantColumns+`
		FROM auth_agent_grants WHERE user_id = $1`, userID))
}

// UpsertAgentGrant records a fresh grant: a newly sealed refresh token, its
// scope, and timestamps. A grant the member already had is replaced whole,
// as it is when they authorize again.
func (store *Store) UpsertAgentGrant(ctx context.Context, grant domain.AgentGrant) error {
	sealed, err := json.Marshal(grant.EncryptedRefreshToken)
	if err != nil {
		return fmt.Errorf("encode sealed agent refresh token: %w", err)
	}
	_, err = store.pool.Exec(ctx, `
		INSERT INTO auth_agent_grants (user_id, encrypted_refresh_token, scope, created_at, refreshed_at)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (user_id) DO UPDATE
		SET encrypted_refresh_token = EXCLUDED.encrypted_refresh_token,
		    scope = EXCLUDED.scope,
		    created_at = EXCLUDED.created_at,
		    refreshed_at = EXCLUDED.refreshed_at`,
		grant.UserID, sealed, grant.Scope, grant.CreatedAt, grant.RefreshedAt)
	if err != nil {
		return mapWriteError("upsert agent grant", err)
	}
	return nil
}

// ReplaceAgentGrantToken swaps in a refresh token Cognito rotated in place of
// the one it was given, without touching scope or created_at. It answers
// domain.ErrNotFound if the member's grant was deleted meanwhile.
func (store *Store) ReplaceAgentGrantToken(ctx context.Context, userID string, sealed domain.SealedData, refreshedAt time.Time) error {
	encoded, err := json.Marshal(sealed)
	if err != nil {
		return fmt.Errorf("encode sealed agent refresh token: %w", err)
	}
	result, err := store.pool.Exec(ctx, `
		UPDATE auth_agent_grants
		SET encrypted_refresh_token = $2, refreshed_at = $3
		WHERE user_id = $1`, userID, encoded, refreshedAt)
	if err != nil {
		return fmt.Errorf("replace agent grant token: %w", err)
	}
	if result.RowsAffected() == 0 {
		return domain.ErrNotFound
	}
	return nil
}

// DeleteAgentGrant removes a member's grant. Deleting a grant that does not
// exist is not an error, so revoking is idempotent.
func (store *Store) DeleteAgentGrant(ctx context.Context, userID string) error {
	if _, err := store.pool.Exec(ctx, `DELETE FROM auth_agent_grants WHERE user_id = $1`, userID); err != nil {
		return fmt.Errorf("delete agent grant: %w", err)
	}
	return nil
}

func scanAgentGrant(row pgx.Row) (domain.AgentGrant, error) {
	var grant domain.AgentGrant
	var sealed []byte
	err := row.Scan(&grant.UserID, &sealed, &grant.Scope, &grant.CreatedAt, &grant.RefreshedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.AgentGrant{}, domain.ErrNotFound
	}
	if err != nil {
		return domain.AgentGrant{}, fmt.Errorf("get agent grant: %w", err)
	}
	if err := json.Unmarshal(sealed, &grant.EncryptedRefreshToken); err != nil {
		return domain.AgentGrant{}, fmt.Errorf("decode sealed agent refresh token: %w", err)
	}
	return grant, nil
}
