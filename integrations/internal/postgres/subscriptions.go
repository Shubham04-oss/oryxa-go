package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/Shubham04-oss/oryxa-go/integrations/internal/domain"
)

const subscriptionColumns = `id, organization_id, connection_id, app_id, provider, scope, team_id, member_id,
	provider_account_id, provider_account_name, account_key, target, events, route, registration_id,
	encrypted_secret, status, reason, created_by, created_at, updated_at, expires_at, renew_at,
	stopped_by, stopped_at, cleanup_at, version`

// CreateSubscription inserts a subscription, pending while it registers at the
// provider, or active, with its created event, when there is nothing to
// register.
func (store *Store) CreateSubscription(ctx context.Context, subscription domain.Subscription, event *domain.Event) error {
	target, err := json.Marshal(subscription.Target)
	if err != nil {
		return fmt.Errorf("encode subscription target: %w", err)
	}
	secret, err := sealedValue(subscription.EncryptedSecret)
	if err != nil {
		return err
	}
	return store.inTx(ctx, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `
			INSERT INTO integrations_subscriptions (
				id, organization_id, connection_id, app_id, provider, scope, team_id, member_id,
				provider_account_id, provider_account_name, account_key, target, events, route, registration_id,
				encrypted_secret, status, created_by, created_at, updated_at, expires_at, renew_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19, $20, $21, $22)`,
			subscription.ID, subscription.OrganizationID, subscription.ConnectionID, subscription.AppID, subscription.Provider,
			subscription.Scope, nullable(subscription.TeamID), nullable(subscription.MemberID),
			subscription.ProviderAccount.ID, subscription.ProviderAccount.Name, subscription.AccountKey, target,
			stringSlice(subscription.Events), subscription.Route, subscription.RegistrationID, secret, subscription.Status,
			subscription.CreatedBy, subscription.CreatedAt, subscription.UpdatedAt, subscription.ExpiresAt, subscription.RenewAt,
		); err != nil {
			return mapWriteError("create subscription", err)
		}
		if event != nil {
			return insertEvents(ctx, tx, *event)
		}
		return nil
	})
}

// ActivateSubscription records what a pending subscription registered at the
// provider, makes it active and writes its created event. A subscription that
// was stopped while it registered stays stopped, and its registration is
// queued for removal: the result is then domain.ErrConflict.
//
// secret replaces the subscription's stored signing secret when the provider
// issued one of its own, as ClickUp does by answering a create with the
// webhook's secret. Nil keeps the secret the subscription was saved with.
func (store *Store) ActivateSubscription(ctx context.Context, organizationID, subscriptionID, registrationID string, secret *domain.SealedData, expiresAt, renewAt *time.Time, at time.Time, eventFor func(domain.Subscription) (domain.Event, error)) (domain.Subscription, error) {
	encoded, err := sealedValue(secret)
	if err != nil {
		return domain.Subscription{}, err
	}
	var activated domain.Subscription
	ended := false
	err = store.inTx(ctx, func(tx pgx.Tx) error {
		current, scanErr := scanSubscription(tx.QueryRow(ctx, `
			SELECT `+subscriptionColumns+` FROM integrations_subscriptions
			WHERE organization_id = $1 AND id = $2 FOR UPDATE`, organizationID, subscriptionID))
		if scanErr != nil {
			return scanErr
		}
		switch current.Status {
		case domain.SubscriptionPending:
		case domain.SubscriptionStopped, domain.SubscriptionFailed:
			ended = true
			if _, err := tx.Exec(ctx, `
				UPDATE integrations_subscriptions
				SET registration_id = $3, cleanup_at = $4, updated_at = $4, version = version + 1
				WHERE organization_id = $1 AND id = $2`, organizationID, subscriptionID, registrationID, at); err != nil {
				return mapWriteError("queue registration removal", err)
			}
			return nil
		default:
			return fmt.Errorf("subscription is already %s: %w", current.Status, domain.ErrConflict)
		}
		activated, err = scanSubscription(tx.QueryRow(ctx, `
			UPDATE integrations_subscriptions
			SET status = 'active', registration_id = $3, expires_at = $4, renew_at = $5, updated_at = $6,
			    encrypted_secret = COALESCE($7, encrypted_secret), version = version + 1
			WHERE organization_id = $1 AND id = $2
			RETURNING `+subscriptionColumns, organizationID, subscriptionID, registrationID, expiresAt, renewAt, at, encoded))
		if err != nil {
			return err
		}
		event, err := eventFor(activated)
		if err != nil {
			return err
		}
		return insertEvents(ctx, tx, event)
	})
	if err == nil && ended {
		return domain.Subscription{}, fmt.Errorf("subscription ended while it registered: %w", domain.ErrConflict)
	}
	return activated, err
}

// DeletePendingSubscription removes a subscription whose registration never
// happened. A subscription that is no longer pending is left alone.
func (store *Store) DeletePendingSubscription(ctx context.Context, organizationID, subscriptionID string) error {
	if _, err := store.pool.Exec(ctx, `
		DELETE FROM integrations_subscriptions
		WHERE organization_id = $1 AND id = $2 AND status = 'pending'`, organizationID, subscriptionID); err != nil {
		return fmt.Errorf("delete pending subscription: %w", err)
	}
	return nil
}

func (store *Store) Subscription(ctx context.Context, organizationID, subscriptionID string) (domain.Subscription, error) {
	return scanSubscription(store.pool.QueryRow(ctx, `
		SELECT `+subscriptionColumns+` FROM integrations_subscriptions
		WHERE organization_id = $1 AND id = $2`, organizationID, subscriptionID))
}

// SubscriptionByRoute finds the subscription a webhook URL names.
func (store *Store) SubscriptionByRoute(ctx context.Context, route string) (domain.Subscription, error) {
	return scanSubscription(store.pool.QueryRow(ctx, `
		SELECT `+subscriptionColumns+` FROM integrations_subscriptions WHERE route = $1`, route))
}

func (store *Store) Subscriptions(ctx context.Context, organizationID string, filter domain.SubscriptionFilter) ([]domain.Subscription, error) {
	return store.querySubscriptions(ctx, `
		SELECT `+subscriptionColumns+` FROM integrations_subscriptions
		WHERE organization_id = $1
		  AND ($2 = '' OR connection_id = $2)
		  AND ($3 = '' OR created_by = $3)
		  AND (NOT $4 OR status IN ('pending', 'active'))
		ORDER BY created_at, id`, organizationID, filter.ConnectionID, filter.CreatedBy, filter.Live)
}

// AccountSubscriptions are the active subscriptions an app delivery for an
// account reaches.
func (store *Store) AccountSubscriptions(ctx context.Context, appID, accountKey string) ([]domain.Subscription, error) {
	return store.querySubscriptions(ctx, `
		SELECT `+subscriptionColumns+` FROM integrations_subscriptions
		WHERE app_id = $1 AND account_key = $2 AND status = 'active'
		ORDER BY created_at, id`, appID, accountKey)
}

// StopSubscriptions stops the live subscriptions a selector picks, writes an
// event for each, and queues removal of what each registered at the
// provider.
func (store *Store) StopSubscriptions(ctx context.Context, selector domain.SubscriptionSelector, reason, stoppedBy string, at time.Time, eventFor func(domain.Subscription) (domain.Event, error)) ([]domain.Subscription, error) {
	if selector.OrganizationID == "" {
		return nil, fmt.Errorf("organization is required: %w", domain.ErrInvalid)
	}
	var stopped []domain.Subscription
	err := store.inTx(ctx, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			UPDATE integrations_subscriptions
			SET status = 'stopped', reason = $6, stopped_by = $7, stopped_at = $8, updated_at = $8,
			    renew_at = NULL, cleanup_at = CASE WHEN registration_id <> '' THEN $8::timestamptz END,
			    version = version + 1
			WHERE organization_id = $1
			  AND status IN ('pending', 'active')
			  AND ($2 = '' OR id = $2)
			  AND ($3 = '' OR connection_id = $3)
			  AND ($4 = '' OR created_by = $4)
			  AND ($5 = '' OR team_id = $5)
			RETURNING `+subscriptionColumns,
			selector.OrganizationID, selector.SubscriptionID, selector.ConnectionID, selector.CreatedBy, selector.TeamID,
			reason, stoppedBy, at)
		if err != nil {
			return fmt.Errorf("stop subscriptions: %w", err)
		}
		stopped, err = collectSubscriptions(rows)
		if err != nil {
			return err
		}
		for _, subscription := range stopped {
			event, err := eventFor(subscription)
			if err != nil {
				return err
			}
			if err := insertEvents(ctx, tx, event); err != nil {
				return err
			}
		}
		return nil
	})
	return stopped, err
}

// FailSubscription marks a live subscription failed with its event. cleanup
// queues removal of its registration, for a provider that may still hold it.
// leaseToken, when set, must still hold the subscription's lease.
func (store *Store) FailSubscription(ctx context.Context, organizationID, subscriptionID, leaseToken, reason string, cleanup bool, at time.Time, eventFor func(domain.Subscription) (domain.Event, error)) (domain.Subscription, error) {
	var failed domain.Subscription
	err := store.inTx(ctx, func(tx pgx.Tx) error {
		var err error
		failed, err = scanSubscription(tx.QueryRow(ctx, `
			UPDATE integrations_subscriptions
			SET status = 'failed', reason = $4, updated_at = $5, renew_at = NULL,
			    cleanup_at = CASE WHEN $6 AND registration_id <> '' THEN $5::timestamptz END,
			    lease_token = NULL, lease_until = NULL, version = version + 1
			WHERE organization_id = $1 AND id = $2 AND status IN ('pending', 'active')
			  AND ($3 = '' OR lease_token = $3)
			RETURNING `+subscriptionColumns, organizationID, subscriptionID, leaseToken, reason, at, cleanup))
		if err != nil {
			return err
		}
		event, err := eventFor(failed)
		if err != nil {
			return err
		}
		return insertEvents(ctx, tx, event)
	})
	return failed, err
}

// RecordDelivery remembers that a subscription received a delivery and writes
// its event, unless the subscription already received it. It reports whether
// the delivery was new.
func (store *Store) RecordDelivery(ctx context.Context, subscriptionID, key string, at time.Time, event domain.Event) (bool, error) {
	recorded := false
	err := store.inTx(ctx, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `
			INSERT INTO integrations_deliveries (subscription_id, delivery_key, received_at)
			VALUES ($1, $2, $3)
			ON CONFLICT DO NOTHING`, subscriptionID, key, at)
		if err != nil {
			return mapWriteError("record delivery", err)
		}
		if tag.RowsAffected() == 0 {
			return nil
		}
		recorded = true
		return insertEvents(ctx, tx, event)
	})
	return recorded, err
}

// DeleteDeliveriesBefore forgets deliveries received before a time.
func (store *Store) DeleteDeliveriesBefore(ctx context.Context, before time.Time) (int64, error) {
	tag, err := store.pool.Exec(ctx, `DELETE FROM integrations_deliveries WHERE received_at < $1`, before)
	if err != nil {
		return 0, fmt.Errorf("delete old deliveries: %w", err)
	}
	return tag.RowsAffected(), nil
}

// ScheduleRenewal brings an active subscription's renewal forward to at.
func (store *Store) ScheduleRenewal(ctx context.Context, subscriptionID string, at time.Time) error {
	if _, err := store.pool.Exec(ctx, `
		UPDATE integrations_subscriptions
		SET renew_at = LEAST(COALESCE(renew_at, $2), $2)
		WHERE id = $1 AND status = 'active' AND registration_id <> ''`, subscriptionID, at); err != nil {
		return fmt.Errorf("schedule renewal: %w", err)
	}
	return nil
}

// ClaimRenewals leases active subscriptions whose renewal is due.
func (store *Store) ClaimRenewals(ctx context.Context, now time.Time, limit int, leaseToken string, leaseUntil time.Time) ([]domain.Subscription, error) {
	return store.querySubscriptions(ctx, `
		WITH picked AS (
			SELECT id FROM integrations_subscriptions
			WHERE status = 'active' AND renew_at IS NOT NULL AND renew_at <= $1
			  AND (lease_until IS NULL OR lease_until < $1)
			ORDER BY renew_at, id
			FOR UPDATE SKIP LOCKED
			LIMIT $2
		)
		UPDATE integrations_subscriptions AS subscription
		SET lease_token = $3, lease_until = $4
		FROM picked
		WHERE subscription.id = picked.id
		RETURNING `+prefixed("subscription", subscriptionColumns), now, limit, leaseToken, leaseUntil)
}

// SaveRenewal records a renewed registration and releases the lease.
func (store *Store) SaveRenewal(ctx context.Context, subscriptionID, leaseToken string, expiresAt, renewAt *time.Time, at time.Time) error {
	return store.leasedUpdate(ctx, "save renewal", `
		UPDATE integrations_subscriptions
		SET expires_at = $3, renew_at = $4, renew_attempts = 0, updated_at = $5,
		    lease_token = NULL, lease_until = NULL, version = version + 1
		WHERE id = $1 AND lease_token = $2 AND status = 'active'`, subscriptionID, leaseToken, expiresAt, renewAt, at)
}

// RetryRenewal releases the lease and tries the renewal again at next.
func (store *Store) RetryRenewal(ctx context.Context, subscriptionID, leaseToken string, next time.Time) error {
	return store.leasedUpdate(ctx, "retry renewal", `
		UPDATE integrations_subscriptions
		SET renew_at = $3, renew_attempts = renew_attempts + 1, lease_token = NULL, lease_until = NULL
		WHERE id = $1 AND lease_token = $2 AND status = 'active'`, subscriptionID, leaseToken, next)
}

// ClaimCleanups leases stopped and failed subscriptions whose registration
// removal is due.
func (store *Store) ClaimCleanups(ctx context.Context, now time.Time, limit int, leaseToken string, leaseUntil time.Time) ([]domain.Subscription, error) {
	return store.querySubscriptions(ctx, `
		WITH picked AS (
			SELECT id FROM integrations_subscriptions
			WHERE cleanup_at IS NOT NULL AND cleanup_at <= $1
			  AND (lease_until IS NULL OR lease_until < $1)
			ORDER BY cleanup_at, id
			FOR UPDATE SKIP LOCKED
			LIMIT $2
		)
		UPDATE integrations_subscriptions AS subscription
		SET lease_token = $3, lease_until = $4
		FROM picked
		WHERE subscription.id = picked.id
		RETURNING `+prefixed("subscription", subscriptionColumns), now, limit, leaseToken, leaseUntil)
}

// FinishCleanup records that a subscription's registration is gone.
func (store *Store) FinishCleanup(ctx context.Context, subscriptionID, leaseToken string, at time.Time) error {
	return store.leasedUpdate(ctx, "finish cleanup", `
		UPDATE integrations_subscriptions
		SET cleanup_at = NULL, updated_at = $3, lease_token = NULL, lease_until = NULL, version = version + 1
		WHERE id = $1 AND lease_token = $2`, subscriptionID, leaseToken, at)
}

// RetryCleanup releases the lease and tries the removal again at next.
func (store *Store) RetryCleanup(ctx context.Context, subscriptionID, leaseToken string, next time.Time) error {
	return store.leasedUpdate(ctx, "retry cleanup", `
		UPDATE integrations_subscriptions
		SET cleanup_at = $3, cleanup_attempts = cleanup_attempts + 1, lease_token = NULL, lease_until = NULL
		WHERE id = $1 AND lease_token = $2`, subscriptionID, leaseToken, next)
}

// HoldsConnection reports whether any of a connection's subscriptions is live
// or still has a registration to remove, which keeps its hold.
func (store *Store) HoldsConnection(ctx context.Context, organizationID, connectionID string) (bool, error) {
	var held bool
	if err := store.pool.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM integrations_subscriptions
			WHERE organization_id = $1 AND connection_id = $2
			  AND (status IN ('pending', 'active') OR cleanup_at IS NOT NULL))`, organizationID, connectionID).Scan(&held); err != nil {
		return false, fmt.Errorf("check connection holds: %w", err)
	}
	return held, nil
}

func (store *Store) leasedUpdate(ctx context.Context, operation, query string, arguments ...any) error {
	tag, err := store.pool.Exec(ctx, query, arguments...)
	if err != nil {
		return mapWriteError(operation, err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("%s: the lease is gone: %w", operation, domain.ErrConflict)
	}
	return nil
}

func (store *Store) querySubscriptions(ctx context.Context, query string, arguments ...any) ([]domain.Subscription, error) {
	rows, err := store.pool.Query(ctx, query, arguments...)
	if err != nil {
		return nil, fmt.Errorf("query subscriptions: %w", err)
	}
	return collectSubscriptions(rows)
}

func collectSubscriptions(rows pgx.Rows) ([]domain.Subscription, error) {
	defer rows.Close()
	subscriptions := []domain.Subscription{}
	for rows.Next() {
		subscription, err := scanSubscription(rows)
		if err != nil {
			return nil, err
		}
		subscriptions = append(subscriptions, subscription)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read subscriptions: %w", err)
	}
	return subscriptions, nil
}

func scanSubscription(row pgx.Row) (domain.Subscription, error) {
	var subscription domain.Subscription
	var teamID, memberID *string
	var target, secret []byte
	err := row.Scan(&subscription.ID, &subscription.OrganizationID, &subscription.ConnectionID, &subscription.AppID,
		&subscription.Provider, &subscription.Scope, &teamID, &memberID, &subscription.ProviderAccount.ID,
		&subscription.ProviderAccount.Name, &subscription.AccountKey, &target, &subscription.Events, &subscription.Route,
		&subscription.RegistrationID, &secret, &subscription.Status, &subscription.Reason, &subscription.CreatedBy,
		&subscription.CreatedAt, &subscription.UpdatedAt, &subscription.ExpiresAt, &subscription.RenewAt,
		&subscription.StoppedBy, &subscription.StoppedAt, &subscription.CleanupAt, &subscription.Version)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Subscription{}, domain.ErrNotFound
	}
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) {
			return domain.Subscription{}, mapWriteError("read subscription", err)
		}
		return domain.Subscription{}, fmt.Errorf("scan subscription: %w", err)
	}
	if teamID != nil {
		subscription.TeamID = *teamID
	}
	if memberID != nil {
		subscription.MemberID = *memberID
	}
	subscription.Target = map[string]string{}
	if err := json.Unmarshal(target, &subscription.Target); err != nil {
		return domain.Subscription{}, fmt.Errorf("decode subscription target: %w", err)
	}
	if subscription.EncryptedSecret, err = sealedFrom(secret); err != nil {
		return domain.Subscription{}, err
	}
	return subscription, nil
}
