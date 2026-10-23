// Package tokens keeps the latest ZedToken SpiceDB returned for each
// organization's relationships, in access.spicedb_tokens. The projector
// writes it after every change; the shadow check reads it to ask SpiceDB for
// an answer at least as fresh as the last change it knows of.
package tokens

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"time"

	v1 "github.com/authzed/authzed-go/proto/authzed/api/v1"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Shubham04-oss/oryxa-go/internal/postgreskit"
)

//go:embed migrations/*.sql
var migrations embed.FS

// migrationsAdvisoryLock follows access's 7263511200.
const migrationsAdvisoryLock int64 = 7263511300

// Migrate installs the table.
func Migrate(ctx context.Context, database *postgreskit.Database) error {
	return database.Migrate(ctx, "access-spicedb-tokens", migrations, "migrations/*.sql", migrationsAdvisoryLock)
}

// Store is where tokens live.
type Store interface {
	// Save records token as org's latest.
	Save(ctx context.Context, org string, token *v1.ZedToken) error
	// SaveAll records token for each org that has not saved a newer one
	// since since: the reconcile's last write covers every org it read.
	SaveAll(ctx context.Context, orgs []string, token *v1.ZedToken, since time.Time) error
	// Latest is org's token, or nil when none was saved.
	Latest(ctx context.Context, org string) (*v1.ZedToken, error)
}

// Postgres is Store in access.spicedb_tokens.
type Postgres struct {
	pool *pgxpool.Pool
	now  func() time.Time
}

func New(pool *pgxpool.Pool) *Postgres { return &Postgres{pool: pool, now: time.Now} }

func (store *Postgres) Save(ctx context.Context, org string, token *v1.ZedToken) error {
	if org == "" || token.GetToken() == "" {
		return nil
	}
	_, err := store.pool.Exec(ctx, `INSERT INTO access.spicedb_tokens (organization_id, token, updated_at)
		VALUES ($1, $2, $3)
		ON CONFLICT (organization_id) DO UPDATE SET token = EXCLUDED.token, updated_at = EXCLUDED.updated_at`,
		org, token.GetToken(), store.now())
	if err != nil {
		return fmt.Errorf("save ZedToken: %w", err)
	}
	return nil
}

func (store *Postgres) SaveAll(ctx context.Context, orgs []string, token *v1.ZedToken, since time.Time) error {
	if len(orgs) == 0 || token.GetToken() == "" {
		return nil
	}
	_, err := store.pool.Exec(ctx, `INSERT INTO access.spicedb_tokens (organization_id, token, updated_at)
		SELECT org, $2, $3 FROM unnest($1::text[]) AS org
		ON CONFLICT (organization_id) DO UPDATE SET token = EXCLUDED.token, updated_at = EXCLUDED.updated_at
		WHERE access.spicedb_tokens.updated_at < $4`,
		orgs, token.GetToken(), store.now(), since)
	if err != nil {
		return fmt.Errorf("save ZedTokens: %w", err)
	}
	return nil
}

func (store *Postgres) Latest(ctx context.Context, org string) (*v1.ZedToken, error) {
	var token string
	err := store.pool.QueryRow(ctx, `SELECT token FROM access.spicedb_tokens WHERE organization_id = $1`, org).Scan(&token)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read ZedToken: %w", err)
	}
	return &v1.ZedToken{Token: token}, nil
}
