package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/Shubham04-oss/oryxa-go/integrations/internal/domain"
)

const appWebhookColumns = `app_id, organization_id, provider, route, encrypted_secret, updated_by, created_at, updated_at`

// PutAppWebhook sets an app's webhook secret. An app that already has one
// keeps its route, so the URL set on the provider's app stays valid.
func (store *Store) PutAppWebhook(ctx context.Context, webhook domain.AppWebhook) (domain.AppWebhook, error) {
	secret, err := sealedValue(&webhook.EncryptedSecret)
	if err != nil {
		return domain.AppWebhook{}, err
	}
	return scanAppWebhook(store.pool.QueryRow(ctx, `
		INSERT INTO integrations_app_webhooks (app_id, organization_id, provider, route, encrypted_secret, updated_by, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $7)
		ON CONFLICT (app_id) DO UPDATE
		SET encrypted_secret = EXCLUDED.encrypted_secret, updated_by = EXCLUDED.updated_by, updated_at = EXCLUDED.updated_at
		WHERE integrations_app_webhooks.organization_id = EXCLUDED.organization_id
		  AND integrations_app_webhooks.provider = EXCLUDED.provider
		RETURNING `+appWebhookColumns,
		webhook.AppID, webhook.OrganizationID, webhook.Provider, webhook.Route, secret, webhook.UpdatedBy, webhook.UpdatedAt))
}

func (store *Store) AppWebhook(ctx context.Context, organizationID, appID string) (domain.AppWebhook, error) {
	return scanAppWebhook(store.pool.QueryRow(ctx, `
		SELECT `+appWebhookColumns+` FROM integrations_app_webhooks
		WHERE organization_id = $1 AND app_id = $2`, organizationID, appID))
}

// AppWebhookByRoute finds the app webhook a webhook URL names.
func (store *Store) AppWebhookByRoute(ctx context.Context, route string) (domain.AppWebhook, error) {
	return scanAppWebhook(store.pool.QueryRow(ctx, `
		SELECT `+appWebhookColumns+` FROM integrations_app_webhooks WHERE route = $1`, route))
}

// DeleteAppWebhooks removes an organization's app webhooks, or one app's.
func (store *Store) DeleteAppWebhooks(ctx context.Context, organizationID, appID string) error {
	if _, err := store.pool.Exec(ctx, `
		DELETE FROM integrations_app_webhooks
		WHERE organization_id = $1 AND ($2 = '' OR app_id = $2)`, organizationID, appID); err != nil {
		return fmt.Errorf("delete app webhooks: %w", err)
	}
	return nil
}

func scanAppWebhook(row pgx.Row) (domain.AppWebhook, error) {
	var webhook domain.AppWebhook
	var secret []byte
	err := row.Scan(&webhook.AppID, &webhook.OrganizationID, &webhook.Provider, &webhook.Route, &secret,
		&webhook.UpdatedBy, &webhook.CreatedAt, &webhook.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		// Also an existing app webhook of another organization or provider,
		// which an upsert must not overwrite.
		return domain.AppWebhook{}, domain.ErrNotFound
	}
	if err != nil {
		return domain.AppWebhook{}, mapWriteError("read app webhook", err)
	}
	sealed, err := sealedFrom(secret)
	if err != nil {
		return domain.AppWebhook{}, err
	}
	if sealed != nil {
		webhook.EncryptedSecret = *sealed
	}
	return webhook, nil
}
