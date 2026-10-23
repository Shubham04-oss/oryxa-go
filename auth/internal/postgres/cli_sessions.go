package postgres

import (
	"context"
	"errors"
	"time"

	"github.com/Shubham04-oss/oryxa-go/auth/internal/clisession"
	"github.com/Shubham04-oss/oryxa-go/auth/internal/domain"
	"github.com/jackc/pgx/v5"
)

func (store *Store) CreateCLIChallenge(ctx context.Context, challenge clisession.Challenge) error {
	return pgx.BeginFunc(ctx, store.pool, func(tx pgx.Tx) error {
		// A global bound complements per-process request limits across replicas.
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(1123581334)`); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `DELETE FROM auth_cli_challenges WHERE expires_at<=now()`); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `DELETE FROM auth_cli_sessions WHERE expires_at<=now()`); err != nil {
			return err
		}
		var pending int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM auth_cli_challenges`).Scan(&pending); err != nil {
			return err
		}
		if pending >= 4096 {
			return domain.ErrRateLimited
		}
		_, err := tx.Exec(ctx, `INSERT INTO auth_cli_challenges(state_hash,nonce,expires_at) VALUES($1,$2,$3)`, clisession.Hash(challenge.State), challenge.Nonce, challenge.ExpiresAt)
		return err
	})
}
func (store *Store) CLIChallenge(ctx context.Context, state string) (clisession.Challenge, error) {
	var result clisession.Challenge
	result.State = state
	err := store.pool.QueryRow(ctx, `SELECT nonce,expires_at FROM auth_cli_challenges WHERE state_hash=$1 AND expires_at>now()`, clisession.Hash(state)).Scan(&result.Nonce, &result.ExpiresAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return clisession.Challenge{}, domain.ErrUnauthorized
	}
	return result, err
}
func (store *Store) AdmitCLISession(ctx context.Context, state, nonce, tokenHash string, expires time.Time) error {
	if !expires.After(time.Now()) {
		return domain.ErrUnauthorized
	}
	return pgx.BeginFunc(ctx, store.pool, func(tx pgx.Tx) error {
		deleted, err := tx.Exec(ctx, `DELETE FROM auth_cli_challenges WHERE state_hash=$1 AND nonce=$2 AND expires_at>now()`, clisession.Hash(state), nonce)
		if err != nil {
			return err
		}
		if deleted.RowsAffected() != 1 {
			return domain.ErrUnauthorized
		}
		// A new challenge cannot reset an already bound token. Each token is admitted once.
		inserted, err := tx.Exec(ctx, `INSERT INTO auth_cli_sessions(token_hash,expires_at) VALUES($1,$2) ON CONFLICT DO NOTHING`, tokenHash, expires)
		if err != nil {
			return err
		}
		if inserted.RowsAffected() != 1 {
			return domain.ErrUnauthorized
		}
		return nil
	})
}
func (store *Store) CLISession(ctx context.Context, tokenHash string) (clisession.Session, error) {
	var result clisession.Session
	err := store.pool.QueryRow(ctx, `SELECT organization_id,expires_at,subject FROM auth_cli_sessions WHERE token_hash=$1 AND expires_at>now()`, tokenHash).Scan(&result.OrganizationID, &result.ExpiresAt, &result.Subject)
	if errors.Is(err, pgx.ErrNoRows) {
		return clisession.Session{}, domain.ErrUnauthorized
	}
	return result, err
}
func (store *Store) BindCLISession(ctx context.Context, tokenHash, organizationID string) error {
	if organizationID == "" || organizationID == clisession.CreatingOrganization {
		return domain.ErrForbidden
	}
	updated, err := store.pool.Exec(ctx, `UPDATE auth_cli_sessions SET organization_id=$2 WHERE token_hash=$1 AND expires_at>now() AND (organization_id='' OR organization_id=$2)`, tokenHash, organizationID)
	if err != nil {
		return err
	}
	if updated.RowsAffected() != 1 {
		return domain.ErrForbidden
	}
	return nil
}
func (store *Store) ReserveCLIOrganization(ctx context.Context, tokenHash string) error {
	updated, err := store.pool.Exec(ctx, `UPDATE auth_cli_sessions SET organization_id=$2 WHERE token_hash=$1 AND expires_at>now() AND organization_id=''`, tokenHash, clisession.CreatingOrganization)
	if err != nil {
		return err
	}
	if updated.RowsAffected() != 1 {
		return domain.ErrForbidden
	}
	return nil
}

// The durable reservation remains if creation completed but the final write
// fails, so a retried request cannot create a second organization with this token.
func (store *Store) FinishCLIOrganization(ctx context.Context, tokenHash, organizationID string) error {
	updated, err := store.pool.Exec(ctx, `UPDATE auth_cli_sessions SET organization_id=$2 WHERE token_hash=$1 AND organization_id=$3`, tokenHash, organizationID, clisession.CreatingOrganization)
	if err != nil {
		return err
	}
	if updated.RowsAffected() != 1 {
		return domain.ErrForbidden
	}
	return nil
}

// PurgeCLISessions bounds retention even while nobody signs in.
func (store *Store) PurgeCLISessions(ctx context.Context) error {
	return pgx.BeginFunc(ctx, store.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `DELETE FROM auth_cli_challenges WHERE expires_at<=now()`); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `DELETE FROM auth_cli_devices WHERE expires_at<=now()`); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `DELETE FROM auth_cli_sessions WHERE expires_at<=now()`)
		return err
	})
}
