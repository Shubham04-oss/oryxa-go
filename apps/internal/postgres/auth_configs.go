package postgres

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"

	"github.com/Shubham04-oss/oryxa-go/apps/internal/application"
	"github.com/Shubham04-oss/oryxa-go/apps/internal/domain"
)

const authConfigColumns = `toolkit, auth_config_id, auth_scheme, composio_managed, created_at`

// AuthConfig reads the auth config recorded for a toolkit: the one auth
// config every member connecting it is sent through.
func (store *Store) AuthConfig(ctx context.Context, toolkit string) (application.AuthConfig, error) {
	config, err := scanAuthConfig(store.pool.QueryRow(ctx, `
		SELECT `+authConfigColumns+`
		FROM apps_auth_configs
		WHERE toolkit = $1`, toolkit))
	if errors.Is(err, pgx.ErrNoRows) {
		return application.AuthConfig{}, domain.ErrNotFound
	}
	if err != nil {
		return application.AuthConfig{}, err
	}
	return config, nil
}

// SaveAuthConfig records a toolkit's auth config unless one already is. The
// insert and the read are separate statements rather than one transaction:
// ON CONFLICT DO NOTHING already makes the insert itself race-safe, so
// whichever of two concurrent callers loses the race simply reads back what
// the winner wrote, and both answer the same config.
func (store *Store) SaveAuthConfig(ctx context.Context, config application.AuthConfig) (application.AuthConfig, error) {
	if _, err := store.pool.Exec(ctx, `
		INSERT INTO apps_auth_configs (toolkit, auth_config_id, auth_scheme, composio_managed, created_at)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (toolkit) DO NOTHING`,
		config.Toolkit, config.AuthConfigID, config.AuthScheme, config.ComposioManaged, config.CreatedAt,
	); err != nil {
		return application.AuthConfig{}, mapWriteError("save auth config", err)
	}
	return store.AuthConfig(ctx, config.Toolkit)
}

func scanAuthConfig(row pgx.Row) (application.AuthConfig, error) {
	var config application.AuthConfig
	if err := row.Scan(&config.Toolkit, &config.AuthConfigID, &config.AuthScheme, &config.ComposioManaged,
		&config.CreatedAt); err != nil {
		return application.AuthConfig{}, err
	}
	config.CreatedAt = config.CreatedAt.UTC()
	return config, nil
}
