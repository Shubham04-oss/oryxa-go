package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/Shubham04-oss/oryxa-go/connections/internal/domain"
)

const sessionColumns = `state_hash, COALESCE(handle_hash, ''), organization_id, member_id, app_id, intent,
	COALESCE(connection_id, ''), scope, COALESCE(team_id, ''), usage, return_uri, redirect_uri, requested_scopes,
	encrypted_verifier, encrypted_result, provider_error, created_at, expires_at, callback_at, claimed_at, completed_at`

func scanSession(row pgx.Row) (domain.Session, error) {
	var session domain.Session
	var verifier, result []byte
	if err := row.Scan(&session.StateHash, &session.HandleHash, &session.OrganizationID, &session.MemberID, &session.AppID,
		&session.Intent, &session.ConnectionID, &session.Scope, &session.TeamID, &session.Usage, &session.ReturnURI,
		&session.RedirectURI, &session.RequestedScopes, &verifier, &result, &session.ProviderError, &session.CreatedAt,
		&session.ExpiresAt, &session.CallbackAt, &session.ClaimedAt, &session.CompletedAt); err != nil {
		return domain.Session{}, err
	}
	var err error
	if session.EncryptedVerifier, err = sealedFrom(verifier); err != nil {
		return domain.Session{}, err
	}
	if session.EncryptedResult, err = sealedFrom(result); err != nil {
		return domain.Session{}, err
	}
	return session, nil
}

func (store *Store) CreateSession(ctx context.Context, session domain.Session) error {
	verifier, err := sealedValue(session.EncryptedVerifier)
	if err != nil {
		return err
	}
	usage := session.Usage
	if usage == "" {
		usage = domain.UsageMembers
	}
	if _, err := store.pool.Exec(ctx, `
		INSERT INTO connections_sessions (
			state_hash, organization_id, member_id, app_id, intent, connection_id, scope, team_id, usage,
			return_uri, redirect_uri, requested_scopes, encrypted_verifier, created_at, expires_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15)`,
		session.StateHash, session.OrganizationID, session.MemberID, session.AppID, session.Intent,
		nullable(session.ConnectionID), session.Scope, nullable(session.TeamID), usage, session.ReturnURI,
		session.RedirectURI, stringSlice(session.RequestedScopes), verifier, session.CreatedAt, session.ExpiresAt); err != nil {
		return mapWriteError("create session", err)
	}
	return nil
}

func (store *Store) SessionByState(ctx context.Context, stateHash string) (domain.Session, error) {
	return store.session(ctx, `state_hash = $1`, stateHash)
}

func (store *Store) SessionByHandle(ctx context.Context, handleHash string) (domain.Session, error) {
	return store.session(ctx, `handle_hash = $1`, handleHash)
}

func (store *Store) session(ctx context.Context, where string, value string) (domain.Session, error) {
	session, err := scanSession(store.pool.QueryRow(ctx, `SELECT `+sessionColumns+` FROM connections_sessions WHERE `+where, value))
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Session{}, domain.ErrNotFound
	}
	if err != nil {
		return domain.Session{}, fmt.Errorf("get session: %w", err)
	}
	return session, nil
}

// RecordCallback stores what the provider sent back: either the encrypted
// code and the hashed handle the app completes with, or a provider error. A
// session takes exactly one callback.
func (store *Store) RecordCallback(ctx context.Context, stateHash, handleHash string, result *domain.SealedData, providerError string, at time.Time) error {
	encoded, err := sealedValue(result)
	if err != nil {
		return err
	}
	tag, err := store.pool.Exec(ctx, `
		UPDATE connections_sessions
		SET handle_hash = $2, encrypted_result = $3, provider_error = $4, callback_at = $5
		WHERE state_hash = $1 AND callback_at IS NULL`,
		stateHash, nullable(handleHash), encoded, providerError, at)
	if err != nil {
		return mapWriteError("record callback", err)
	}
	if tag.RowsAffected() == 0 {
		return domain.ErrNotFound
	}
	return nil
}

// ClaimSession takes a session for one completion before its code is
// exchanged. A session another completion already took, or one that is used
// up, refused or expired, answers domain.ErrSessionExpired.
func (store *Store) ClaimSession(ctx context.Context, stateHash string, at time.Time) error {
	tag, err := store.pool.Exec(ctx, `
		UPDATE connections_sessions SET claimed_at = $2
		WHERE state_hash = $1 AND handle_hash IS NOT NULL AND provider_error = ''
		  AND claimed_at IS NULL AND completed_at IS NULL AND expires_at > $2`, stateHash, at)
	if err != nil {
		return fmt.Errorf("claim session: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return domain.ErrSessionExpired
	}
	return nil
}

// BurnSession uses a session up without creating anything, after a failed
// exchange or identity check.
func (store *Store) BurnSession(ctx context.Context, stateHash string, at time.Time) error {
	if _, err := store.pool.Exec(ctx, `
		UPDATE connections_sessions SET completed_at = $2
		WHERE state_hash = $1 AND completed_at IS NULL`, stateHash, at); err != nil {
		return fmt.Errorf("burn session: %w", err)
	}
	return nil
}

// CountOpenSessions counts a member's authorization sessions that have not
// expired or completed, to bound how many they may hold at once.
func (store *Store) CountOpenSessions(ctx context.Context, organizationID, memberID string, now time.Time) (int, error) {
	var count int
	if err := store.pool.QueryRow(ctx, `
		SELECT count(*) FROM connections_sessions
		WHERE organization_id = $1 AND member_id = $2 AND expires_at > $3 AND completed_at IS NULL`,
		organizationID, memberID, now).Scan(&count); err != nil {
		return 0, fmt.Errorf("count open sessions: %w", err)
	}
	return count, nil
}

// DeleteExpiredSessions removes up to limit sessions whose expiry has
// passed, so the table does not grow without bound.
func (store *Store) DeleteExpiredSessions(ctx context.Context, before time.Time, limit int) (int, error) {
	tag, err := store.pool.Exec(ctx, `
		DELETE FROM connections_sessions
		WHERE state_hash IN (
			SELECT state_hash FROM connections_sessions WHERE expires_at < $1 LIMIT $2
		)`, before, limit)
	if err != nil {
		return 0, fmt.Errorf("delete expired sessions: %w", err)
	}
	return int(tag.RowsAffected()), nil
}

// consumeSession finishes a claimed session in the transaction that saves its
// connection.
func consumeSession(ctx context.Context, tx pgx.Tx, stateHash string, at time.Time) error {
	tag, err := tx.Exec(ctx, `
		UPDATE connections_sessions SET completed_at = $2
		WHERE state_hash = $1 AND claimed_at IS NOT NULL AND completed_at IS NULL`, stateHash, at)
	if err != nil {
		return fmt.Errorf("consume session: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return domain.ErrSessionExpired
	}
	return nil
}
