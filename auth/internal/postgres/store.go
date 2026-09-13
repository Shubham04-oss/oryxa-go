package postgres

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"slices"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Shubham04-oss/oryxa-go/auth/internal/domain"
)

//go:embed migrations/*.sql
var migrations embed.FS

type Store struct {
	pool *pgxpool.Pool
}

func Open(ctx context.Context, databaseURL string) (*Store, error) {
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		return nil, fmt.Errorf("configure postgres: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("connect postgres: %w", err)
	}
	return &Store{pool: pool}, nil
}

func (store *Store) Close() {
	store.pool.Close()
}

// Migrate applies every migration in name order. Each is written to be safe to
// run again.
func (store *Store) Migrate(ctx context.Context) error {
	names, err := fs.Glob(migrations, "migrations/*.sql")
	if err != nil {
		return fmt.Errorf("list auth migrations: %w", err)
	}
	slices.Sort(names)
	for _, name := range names {
		contents, err := migrations.ReadFile(name)
		if err != nil {
			return fmt.Errorf("read auth migration %s: %w", name, err)
		}
		if _, err := store.pool.Exec(ctx, string(contents)); err != nil {
			return fmt.Errorf("apply auth migration %s: %w", name, err)
		}
	}
	return nil
}

func (store *Store) SaveLoginFlow(ctx context.Context, flow domain.LoginFlow) error {
	_, err := store.pool.Exec(ctx, `
		WITH cleared AS (
			DELETE FROM auth_login_flows WHERE expires_at <= now()
		)
		INSERT INTO auth_login_flows (state_hash, code_verifier, nonce, return_to, expires_at)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (state_hash) DO UPDATE
		SET code_verifier = EXCLUDED.code_verifier,
		    nonce = EXCLUDED.nonce,
		    return_to = EXCLUDED.return_to,
		    expires_at = EXCLUDED.expires_at`,
		flow.StateHash, flow.CodeVerifier, flow.Nonce, flow.ReturnTo, flow.ExpiresAt)
	if err != nil {
		return fmt.Errorf("save login flow: %w", err)
	}
	return nil
}

func (store *Store) ConsumeLoginFlow(ctx context.Context, stateHash string) (domain.LoginFlow, error) {
	var flow domain.LoginFlow
	err := store.pool.QueryRow(ctx, `
		DELETE FROM auth_login_flows
		WHERE state_hash = $1 AND expires_at > now()
		RETURNING state_hash, code_verifier, nonce, return_to, expires_at`, stateHash).
		Scan(&flow.StateHash, &flow.CodeVerifier, &flow.Nonce, &flow.ReturnTo, &flow.ExpiresAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.LoginFlow{}, domain.ErrNotFound
	}
	if err != nil {
		return domain.LoginFlow{}, fmt.Errorf("consume login flow: %w", err)
	}
	return flow, nil
}

func (store *Store) UpsertFederatedUser(ctx context.Context, user domain.User, registered, signedIn domain.Event) (bool, error) {
	tx, err := store.pool.Begin(ctx)
	if err != nil {
		return false, fmt.Errorf("begin user transaction: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var insertedID string
	err = tx.QueryRow(ctx, `
		INSERT INTO auth_users (id, email, display_name, avatar_url, created_at, updated_at)
		VALUES ($1, lower($2), $3, $4, $5, $5)
		ON CONFLICT (id) DO NOTHING
		RETURNING id`, user.ID, user.Email, user.DisplayName, user.AvatarURL, user.CreatedAt).
		Scan(&insertedID)
	created := err == nil
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return false, mapWriteError("insert user", err)
	}
	if !created {
		_, err = tx.Exec(ctx, `
			UPDATE auth_users
			SET email = lower($2), display_name = $3, avatar_url = $4, updated_at = $5
			WHERE id = $1`, user.ID, user.Email, user.DisplayName, user.AvatarURL, user.UpdatedAt)
		if err != nil {
			return false, mapWriteError("update user", err)
		}
	}

	events := []domain.Event{signedIn}
	if created {
		events = append([]domain.Event{registered}, events...)
	}
	if err := insertEvents(ctx, tx, events); err != nil {
		return false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return false, fmt.Errorf("commit user transaction: %w", err)
	}
	return created, nil
}

func (store *Store) UserByID(ctx context.Context, userID string) (domain.User, error) {
	var user domain.User
	err := store.pool.QueryRow(ctx, `
		SELECT id, email, display_name, avatar_url, created_at, updated_at
		FROM auth_users WHERE id = $1`, userID).
		Scan(&user.ID, &user.Email, &user.DisplayName, &user.AvatarURL, &user.CreatedAt, &user.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.User{}, domain.ErrNotFound
	}
	if err != nil {
		return domain.User{}, fmt.Errorf("get user: %w", err)
	}
	return user, nil
}

func insertEvents(ctx context.Context, tx pgx.Tx, events []domain.Event) error {
	for _, event := range events {
		_, err := tx.Exec(ctx, `
			INSERT INTO auth_outbox (id, detail_type, detail, occurred_at)
			VALUES ($1, $2, $3, $4)`, event.ID, event.DetailType, event.Data, event.OccurredAt)
		if err != nil {
			return fmt.Errorf("insert outbox event: %w", err)
		}
	}
	return nil
}

func mapWriteError(operation string, err error) error {
	var postgresError *pgconn.PgError
	if errors.As(err, &postgresError) && (postgresError.Code == "23505" || postgresError.Code == "23503") {
		return fmt.Errorf("%s: %w", operation, domain.ErrConflict)
	}
	return fmt.Errorf("%s: %w", operation, err)
}

func normalizeEmail(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}
