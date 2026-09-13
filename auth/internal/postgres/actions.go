package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/Shubham04-oss/oryxa-go/auth/internal/domain"
)

func (store *Store) CreateActionToken(ctx context.Context, action domain.ActionToken, event domain.Event) error {
	tx, err := store.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin action-token transaction: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	_, err = tx.Exec(ctx, `
		UPDATE auth_action_tokens
		SET status = 'expired', processing_until = NULL
		WHERE lower(email) = lower($1) AND purpose = $2
		  AND (status = 'pending' OR (status = 'processing' AND processing_until <= $3))`,
		action.Email, action.Purpose, action.CreatedAt)
	if err != nil {
		return fmt.Errorf("expire prior action tokens: %w", err)
	}
	_, err = tx.Exec(ctx, `
		INSERT INTO auth_action_tokens
		    (id, user_id, email, purpose, token_hash, status, expires_at, created_at)
		VALUES ($1, $2, lower($3), $4, $5, $6, $7, $8)`,
		action.ID,
		action.UserID,
		action.Email,
		action.Purpose,
		action.TokenHash,
		action.Status,
		action.ExpiresAt,
		action.CreatedAt,
	)
	if err != nil {
		return mapWriteError("insert action token", err)
	}
	if err := insertEvents(ctx, tx, []domain.Event{event}); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit action-token transaction: %w", err)
	}
	return nil
}

func (store *Store) ClaimActionToken(ctx context.Context, purpose domain.ActionPurpose, tokenHash string, claimedAt time.Time) (domain.ActionToken, error) {
	if _, err := store.pool.Exec(ctx, `
		UPDATE auth_action_tokens
		SET status = 'expired', processing_until = NULL
		WHERE purpose = $1 AND token_hash = $2
		  AND status IN ('pending', 'processing') AND expires_at <= $3`, purpose, tokenHash, claimedAt); err != nil {
		return domain.ActionToken{}, fmt.Errorf("expire action token: %w", err)
	}
	var action domain.ActionToken
	err := store.pool.QueryRow(ctx, `
		UPDATE auth_action_tokens
		SET status = 'processing', processing_until = $3 + interval '5 minutes'
		WHERE purpose = $1 AND token_hash = $2 AND expires_at > $3
		  AND (status = 'pending' OR (status = 'processing' AND processing_until <= $3))
		RETURNING id, user_id, email, purpose, token_hash, status, expires_at, created_at, processing_until`, purpose, tokenHash, claimedAt).
		Scan(
			&action.ID,
			&action.UserID,
			&action.Email,
			&action.Purpose,
			&action.TokenHash,
			&action.Status,
			&action.ExpiresAt,
			&action.CreatedAt,
			&action.ProcessingUntil,
		)
	if errors.Is(err, pgx.ErrNoRows) {
		var status domain.ActionStatus
		lookupErr := store.pool.QueryRow(ctx, `
			SELECT status FROM auth_action_tokens WHERE purpose = $1 AND token_hash = $2`, purpose, tokenHash).Scan(&status)
		if errors.Is(lookupErr, pgx.ErrNoRows) {
			return domain.ActionToken{}, domain.ErrNotFound
		}
		if lookupErr != nil {
			return domain.ActionToken{}, fmt.Errorf("inspect action token: %w", lookupErr)
		}
		if status == domain.ActionExpired {
			return domain.ActionToken{}, domain.ErrExpired
		}
		return domain.ActionToken{}, domain.ErrConflict
	}
	if err != nil {
		return domain.ActionToken{}, fmt.Errorf("claim action token: %w", err)
	}
	return action, nil
}

func (store *Store) ReleaseActionToken(ctx context.Context, purpose domain.ActionPurpose, tokenHash string) error {
	_, err := store.pool.Exec(ctx, `
		UPDATE auth_action_tokens
		SET status = 'pending', processing_until = NULL
		WHERE purpose = $1 AND token_hash = $2 AND status = 'processing'`, purpose, tokenHash)
	if err != nil {
		return fmt.Errorf("release action token: %w", err)
	}
	return nil
}

func (store *Store) CompleteActionToken(ctx context.Context, purpose domain.ActionPurpose, tokenHash string, completedAt time.Time, event domain.Event) error {
	tx, err := store.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin action completion: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var status domain.ActionStatus
	err = tx.QueryRow(ctx, `
		SELECT status
		FROM auth_action_tokens
		WHERE purpose = $1 AND token_hash = $2
		FOR UPDATE`, purpose, tokenHash).Scan(&status)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("lock action token: %w", err)
	}
	if status != domain.ActionProcessing {
		return domain.ErrConflict
	}
	_, err = tx.Exec(ctx, `
		UPDATE auth_action_tokens
		SET status = 'completed', processing_until = NULL, completed_at = $3
		WHERE purpose = $1 AND token_hash = $2`, purpose, tokenHash, completedAt)
	if err != nil {
		return fmt.Errorf("complete action token: %w", err)
	}
	if err := insertEvents(ctx, tx, []domain.Event{event}); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit action completion: %w", err)
	}
	return nil
}
