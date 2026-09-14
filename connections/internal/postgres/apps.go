package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/Shubham04-oss/oryxa-go/connections/internal/domain"
)

const appColumns = `id, organization_id, provider, credential_type, client_id, encrypted_secret, settings, scopes, status, revision, created_by, created_at, updated_at`

func scanApp(row pgx.Row) (domain.App, error) {
	var app domain.App
	var secret, settings []byte
	if err := row.Scan(&app.ID, &app.OrganizationID, &app.Provider, &app.CredentialType, &app.ClientID, &secret, &settings,
		&app.Scopes, &app.Status, &app.Revision, &app.CreatedBy, &app.CreatedAt, &app.UpdatedAt); err != nil {
		return domain.App{}, err
	}
	sealed, err := sealedFrom(secret)
	if err != nil {
		return domain.App{}, err
	}
	app.EncryptedSecret = sealed
	if app.Settings, err = settingsFrom(settings); err != nil {
		return domain.App{}, err
	}
	return app, nil
}

func (store *Store) CreateApp(ctx context.Context, app domain.App, event domain.Event) error {
	secret, err := sealedValue(app.EncryptedSecret)
	if err != nil {
		return err
	}
	settings, err := settingsValue(app.Settings)
	if err != nil {
		return err
	}
	return store.inTx(ctx, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `
			INSERT INTO connections_apps (
				id, organization_id, provider, credential_type, client_id, encrypted_secret, settings,
				scopes, status, revision, created_by, created_at, updated_at
			) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)`,
			app.ID, app.OrganizationID, app.Provider, app.CredentialType, app.ClientID, secret, settings,
			stringSlice(app.Scopes), app.Status, app.Revision, app.CreatedBy, app.CreatedAt, app.UpdatedAt); err != nil {
			return mapWriteError("create app", err)
		}
		return insertEvents(ctx, tx, event)
	})
}

// App returns an app that has not been deleted.
func (store *Store) App(ctx context.Context, organizationID, appID string) (domain.App, error) {
	app, err := scanApp(store.pool.QueryRow(ctx, `
		SELECT `+appColumns+`
		FROM connections_apps
		WHERE organization_id = $1 AND id = $2 AND deleted_at IS NULL`, organizationID, appID))
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.App{}, domain.ErrNotFound
	}
	if err != nil {
		return domain.App{}, fmt.Errorf("get app: %w", err)
	}
	return app, nil
}

// AnyApp returns an app in any state, including a deleted one, for cleaning
// up what it left behind.
func (store *Store) AnyApp(ctx context.Context, organizationID, appID string) (domain.App, error) {
	app, err := scanApp(store.pool.QueryRow(ctx, `
		SELECT `+appColumns+`
		FROM connections_apps
		WHERE organization_id = $1 AND id = $2`, organizationID, appID))
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.App{}, domain.ErrNotFound
	}
	if err != nil {
		return domain.App{}, fmt.Errorf("get app: %w", err)
	}
	return app, nil
}

func (store *Store) Apps(ctx context.Context, organizationID string) ([]domain.App, error) {
	rows, err := store.pool.Query(ctx, `
		SELECT `+appColumns+`
		FROM connections_apps
		WHERE organization_id = $1 AND deleted_at IS NULL
		ORDER BY created_at, id`, organizationID)
	if err != nil {
		return nil, fmt.Errorf("list apps: %w", err)
	}
	defer rows.Close()
	apps := []domain.App{}
	for rows.Next() {
		app, err := scanApp(rows)
		if err != nil {
			return nil, fmt.Errorf("scan app: %w", err)
		}
		apps = append(apps, app)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list apps: %w", err)
	}
	return apps, nil
}

// UpdateApp saves an app whose revision is still expectedRevision. When
// reconnect names a reason, every active connection of the app moves to
// needs_reconnect in the same transaction, with one event each.
func (store *Store) UpdateApp(ctx context.Context, app domain.App, expectedRevision int64, reconnect domain.ReconnectReason,
	event domain.Event, connectionEvent func(domain.Connection) (domain.Event, error)) error {
	secret, err := sealedValue(app.EncryptedSecret)
	if err != nil {
		return err
	}
	settings, err := settingsValue(app.Settings)
	if err != nil {
		return err
	}
	return store.inTx(ctx, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `
			UPDATE connections_apps
			SET client_id = $3, encrypted_secret = $4, settings = $5, scopes = $6, status = $7, revision = $8, updated_at = $9
			WHERE organization_id = $1 AND id = $2 AND revision = $10 AND deleted_at IS NULL`,
			app.OrganizationID, app.ID, app.ClientID, secret, settings, stringSlice(app.Scopes), app.Status,
			app.Revision, app.UpdatedAt, expectedRevision)
		if err != nil {
			return mapWriteError("update app", err)
		}
		if tag.RowsAffected() == 0 {
			return domain.ErrConflict
		}
		if reconnect != "" {
			if _, err := changeConnections(ctx, tx, `
				UPDATE connections
				SET status = 'needs_reconnect', status_reason = $3, status_changed_at = $4, updated_at = $4, version = version + 1
				WHERE organization_id = $1 AND app_id = $2 AND status = 'active'
				RETURNING `+connectionColumns(""),
				[]any{app.OrganizationID, app.ID, string(reconnect), app.UpdatedAt}, connectionEvent); err != nil {
				return err
			}
		}
		return insertEvents(ctx, tx, event)
	})
}

// DeleteApp takes an app out of use and disconnects its connections, with
// one event each. Their credentials stay until holds are released.
func (store *Store) DeleteApp(ctx context.Context, organizationID, appID string, at time.Time, event domain.Event,
	connectionEvent func(domain.Connection) (domain.Event, error)) error {
	return store.inTx(ctx, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `
			UPDATE connections_apps SET status = 'disabled', deleted_at = $3, updated_at = $3
			WHERE organization_id = $1 AND id = $2 AND deleted_at IS NULL`, organizationID, appID, at)
		if err != nil {
			return mapWriteError("delete app", err)
		}
		if tag.RowsAffected() == 0 {
			return domain.ErrNotFound
		}
		if _, err := changeConnections(ctx, tx, `
			UPDATE connections
			SET status = 'disconnected', status_reason = 'app_deleted', is_default = false,
			    status_changed_at = $3, updated_at = $3, version = version + 1
			WHERE organization_id = $1 AND app_id = $2 AND status <> 'disconnected'
			RETURNING `+connectionColumns(""),
			[]any{organizationID, appID, at}, connectionEvent); err != nil {
			return err
		}
		return insertEvents(ctx, tx, event)
	})
}

// DeleteOrganizationApps takes every one of an organization's apps out of
// use, for an organization that is itself being deleted. It leaves the
// connections themselves alone; the caller disconnects them separately.
func (store *Store) DeleteOrganizationApps(ctx context.Context, organizationID string, at time.Time, appEvent func(domain.App) (domain.Event, error)) (int, error) {
	var deleted []domain.App
	err := store.inTx(ctx, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			UPDATE connections_apps
			SET status = 'disabled', deleted_at = $2, updated_at = $2, revision = revision + 1
			WHERE organization_id = $1 AND deleted_at IS NULL
			RETURNING `+appColumns, organizationID, at)
		if err != nil {
			return mapWriteError("delete organization apps", err)
		}
		for rows.Next() {
			app, err := scanApp(rows)
			if err != nil {
				rows.Close()
				return fmt.Errorf("scan deleted app: %w", err)
			}
			deleted = append(deleted, app)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return mapWriteError("delete organization apps", err)
		}
		for _, app := range deleted {
			event, err := appEvent(app)
			if err != nil {
				return err
			}
			if err := insertEvents(ctx, tx, event); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	return len(deleted), nil
}

// changeConnections runs an UPDATE ... RETURNING over connections, records
// one event for each changed row, and returns the changed rows.
func changeConnections(ctx context.Context, tx pgx.Tx, sql string, args []any, connectionEvent func(domain.Connection) (domain.Event, error)) ([]domain.Connection, error) {
	rows, err := tx.Query(ctx, sql, args...)
	if err != nil {
		return nil, mapWriteError("change connections", err)
	}
	var changed []domain.Connection
	for rows.Next() {
		connection, err := scanConnection(rows)
		if err != nil {
			rows.Close()
			return nil, fmt.Errorf("scan changed connection: %w", err)
		}
		changed = append(changed, connection)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, mapWriteError("change connections", err)
	}
	for _, connection := range changed {
		event, err := connectionEvent(connection)
		if err != nil {
			return nil, err
		}
		if err := insertEvents(ctx, tx, event); err != nil {
			return nil, err
		}
	}
	return changed, nil
}
