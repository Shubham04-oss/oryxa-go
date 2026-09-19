package postgres

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jmoiron/sqlx"

	"github.com/Shubham04-oss/oryxa-go/billing/internal/domain"
	"github.com/Shubham04-oss/oryxa-go/internal/observability"
	"github.com/Shubham04-oss/oryxa-go/internal/postgreskit"
)

//go:embed migrations/*.sql
var migrations embed.FS

// Replicas that start together must not install the schema at the same time;
// IF NOT EXISTS alone does not serialize catalog changes.
const migrationsAdvisoryLock int64 = 7263510402

// Store is billing's PostgreSQL access through the shared postgreskit
// boundary: one instrumented pgx pool, with sqlx on that same pool.
type Store struct {
	pool     *pgxpool.Pool
	db       *sqlx.DB
	database *postgreskit.Database
	// borrowed is a pool someone else opened and will close.
	borrowed bool
}

// Open opens a pool of billing's own, which Close closes.
func Open(ctx context.Context, databaseURL string, requiredExtensions ...string) (*Store, error) {
	database, err := postgreskit.Open(ctx, postgreskit.Config{
		URL: databaseURL, ApplicationName: "oryxa-billing", RequiredExtensions: requiredExtensions,
	})
	if err != nil {
		return nil, fmt.Errorf("open billing postgres: %w", err)
	}
	return &Store{pool: database.Pool, db: database.SQLX, database: database}, nil
}

// New uses a pool the caller owns, such as the one a host shares between
// services. Close leaves it open.
func New(database *postgreskit.Database) *Store {
	return &Store{pool: database.Pool, db: database.SQLX, database: database, borrowed: true}
}

func (store *Store) Close() {
	if !store.borrowed {
		store.database.Close()
	}
}

func (store *Store) ExtensionVersions() map[string]string { return store.database.ExtensionVersions() }

// Pool is billing's pool, for internal/outbox's table to claim, mark and
// sweep billing_outbox directly rather than through a copy of that logic
// here.
func (store *Store) Pool() *pgxpool.Pool { return store.pool }

// Migrate applies each migration in name order, once while it stays unchanged,
// recording what it applied; see postgreskit's Migrate.
func (store *Store) Migrate(ctx context.Context) error {
	return store.database.Migrate(ctx, "billing", migrations, "migrations/*.sql", migrationsAdvisoryLock)
}

type execer interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
	GetContext(context.Context, any, string, ...any) error
	SelectContext(context.Context, any, string, ...any) error
	QueryRowxContext(context.Context, string, ...any) *sqlx.Row
	QueryxContext(context.Context, string, ...any) (*sqlx.Rows, error)
}

func (store *Store) inTx(ctx context.Context, fn func(*sqlx.Tx) error) error {
	tx, err := store.db.BeginTxx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err := fn(tx); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	return nil
}

const organizationColumns = `organization_id, name, coalesce(customer_id, ''), coalesce(subscription_id, ''),
	plan, entitlement_status, member_limit, grace_ends_at, entitlements_revision, deleted_at IS NOT NULL`

const subscriptionColumns = `subscription_id, organization_id, customer_id, customer_email, product_id, quantity,
	status, cancel_at_next_billing_date, next_billing_date, past_due_ends_at, billing_interval,
	billing_interval_count, event_at`

// Organization returns the organization and its current subscription.
func (store *Store) Organization(ctx context.Context, organizationID string) (domain.Organization, *domain.Subscription, error) {
	organization, err := scanOrganization(store.db.QueryRowxContext(ctx,
		`SELECT `+organizationColumns+` FROM billing_organizations WHERE organization_id = $1`, organizationID))
	if isNoRows(err) {
		return domain.Organization{}, nil, domain.ErrNotFound
	}
	if err != nil {
		return domain.Organization{}, nil, fmt.Errorf("get organization: %w", err)
	}
	current, err := findSubscription(ctx, store.db, organization.SubscriptionID, false)
	if err != nil {
		return domain.Organization{}, nil, err
	}
	return organization, current, nil
}

// UpdateOrganization reads the organization, creating it if billing hasn't
// seen it, and writes back what update returns, all under a row lock.
func (store *Store) UpdateOrganization(ctx context.Context, organizationID string, update func(domain.OrganizationState) (domain.Change, error)) error {
	return store.inTx(ctx, func(tx *sqlx.Tx) error {
		organization, err := lockOrganization(ctx, tx, organizationID)
		if err != nil {
			return err
		}
		current, err := findSubscription(ctx, tx, organization.SubscriptionID, true)
		if err != nil {
			return err
		}
		change, err := update(domain.OrganizationState{Organization: organization, Subscription: current})
		if err != nil {
			return err
		}
		return writeChange(ctx, tx, change)
	})
}

// ApplyWebhook records the delivery and applies it once. Retries of a
// delivery already recorded change nothing.
func (store *Store) ApplyWebhook(ctx context.Context, delivery domain.WebhookDelivery, incoming domain.Subscription, update func(domain.WebhookState) (domain.Change, error)) (domain.WebhookOutcome, error) {
	var outcome domain.WebhookOutcome
	err := store.inTx(ctx, func(tx *sqlx.Tx) error {
		recorded, err := tx.ExecContext(ctx, `
			INSERT INTO billing_webhook_deliveries (webhook_id, event_type)
			VALUES ($1, $2)
			ON CONFLICT (webhook_id) DO NOTHING`, delivery.ID, delivery.Type)
		if err != nil {
			return fmt.Errorf("record webhook delivery: %w", err)
		}
		if rows, _ := recorded.RowsAffected(); rows == 0 {
			outcome = domain.WebhookDuplicate
			return nil
		}

		organizationID, err := webhookOrganization(ctx, tx, incoming)
		if err != nil {
			return err
		}
		if organizationID == "" {
			outcome = domain.WebhookUnmatched
			return nil
		}

		organization, err := lockOrganization(ctx, tx, organizationID)
		if err != nil {
			return err
		}
		previous, err := findSubscription(ctx, tx, incoming.ID, true)
		if err != nil {
			return err
		}
		var current *domain.Subscription
		if organization.SubscriptionID != incoming.ID {
			if current, err = findSubscription(ctx, tx, organization.SubscriptionID, true); err != nil {
				return err
			}
		}
		change, err := update(domain.WebhookState{Organization: organization, Previous: previous, Current: current})
		if err != nil {
			return err
		}
		if err := writeChange(ctx, tx, change); err != nil {
			return err
		}
		outcome = domain.WebhookApplied
		return nil
	})
	if err != nil {
		return "", err
	}
	return outcome, nil
}

// webhookOrganization finds whose subscription this is: the organization it
// is already recorded under, the one its metadata names, or the owner of its
// customer.
func webhookOrganization(ctx context.Context, tx execer, incoming domain.Subscription) (string, error) {
	var organizationID string
	err := tx.GetContext(ctx, &organizationID, `SELECT organization_id FROM billing_subscriptions WHERE subscription_id = $1`, incoming.ID)
	if err == nil {
		return organizationID, nil
	}
	if !isNoRows(err) {
		return "", fmt.Errorf("find subscription organization: %w", err)
	}
	if incoming.OrganizationID != "" {
		return incoming.OrganizationID, nil
	}
	err = tx.GetContext(ctx, &organizationID, `SELECT organization_id FROM billing_organizations WHERE customer_id = $1`, incoming.CustomerID)
	if isNoRows(err) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("find customer organization: %w", err)
	}
	return organizationID, nil
}

// ClaimRenewals finds active subscriptions renewing within window that have
// not been reminded about this renewal, records each event remind returns, and
// marks them reminded. Concurrent callers claim different subscriptions.
func (store *Store) ClaimRenewals(ctx context.Context, now time.Time, window time.Duration, intervals []string, limit int, remind func(domain.Renewal) (*domain.Event, error)) (int, error) {
	var claimed int
	err := store.inTx(ctx, func(tx *sqlx.Tx) error {
		rows, err := tx.QueryxContext(ctx, `
			SELECT s.subscription_id, s.organization_id, s.customer_id, s.customer_email, s.product_id, s.quantity,
			       s.status, s.cancel_at_next_billing_date, s.next_billing_date, s.past_due_ends_at, s.billing_interval,
			       s.billing_interval_count, s.event_at, o.name
			FROM billing_subscriptions s
			JOIN billing_organizations o
			  ON o.organization_id = s.organization_id AND o.subscription_id = s.subscription_id
			WHERE s.status = 'active'
			  AND NOT s.cancel_at_next_billing_date
			  AND o.deleted_at IS NULL
			  AND s.billing_interval = ANY($3)
			  AND s.next_billing_date > $1 AND s.next_billing_date <= $2
			  AND s.renewal_reminded_for IS DISTINCT FROM s.next_billing_date
			ORDER BY s.next_billing_date
			LIMIT $4
			FOR UPDATE OF s SKIP LOCKED`, now, now.Add(window), intervals, limit)
		if err != nil {
			return fmt.Errorf("find due renewals: %w", err)
		}
		var renewals []domain.Renewal
		for rows.Next() {
			var renewal domain.Renewal
			subscription, err := scanSubscriptionFrom(rows, &renewal.OrganizationName)
			if err != nil {
				_ = rows.Close()
				return fmt.Errorf("scan renewal: %w", err)
			}
			renewal.Subscription = subscription
			renewals = append(renewals, renewal)
		}
		if err := rows.Close(); err != nil {
			return fmt.Errorf("close renewals: %w", err)
		}
		if err := rows.Err(); err != nil {
			return fmt.Errorf("iterate renewals: %w", err)
		}

		for _, renewal := range renewals {
			event, err := remind(renewal)
			if err != nil {
				return err
			}
			if event != nil {
				if err := insertEvents(ctx, tx, []domain.Event{*event}); err != nil {
					return err
				}
			}
			if _, err := tx.ExecContext(ctx, `
				UPDATE billing_subscriptions
				SET renewal_reminded_for = next_billing_date, updated_at = now()
				WHERE subscription_id = $1`, renewal.Subscription.ID); err != nil {
				return fmt.Errorf("mark renewal reminded: %w", err)
			}
		}
		claimed = len(renewals)
		return nil
	})
	if err != nil {
		return 0, err
	}
	return claimed, nil
}

// OrganizationIDs lists every organization that has not been deleted.
func (store *Store) OrganizationIDs(ctx context.Context) ([]string, error) {
	ids := make([]string, 0)
	if err := store.db.SelectContext(ctx, &ids, `
		SELECT organization_id FROM billing_organizations
		WHERE deleted_at IS NULL
		ORDER BY organization_id`); err != nil {
		return nil, fmt.Errorf("list organizations: %w", err)
	}
	return ids, nil
}

// PruneWebhookDeliveries forgets deliveries received before cutoff, long after
// Dodo stops retrying them.
func (store *Store) PruneWebhookDeliveries(ctx context.Context, cutoff time.Time) error {
	if _, err := store.db.ExecContext(ctx, `DELETE FROM billing_webhook_deliveries WHERE received_at < $1`, cutoff); err != nil {
		return fmt.Errorf("prune webhook deliveries: %w", err)
	}
	return nil
}

func lockOrganization(ctx context.Context, tx execer, organizationID string) (domain.Organization, error) {
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO billing_organizations (organization_id)
		VALUES ($1)
		ON CONFLICT (organization_id) DO NOTHING`, organizationID); err != nil {
		return domain.Organization{}, fmt.Errorf("record organization: %w", err)
	}
	organization, err := scanOrganization(tx.QueryRowxContext(ctx,
		`SELECT `+organizationColumns+` FROM billing_organizations WHERE organization_id = $1 FOR UPDATE`, organizationID))
	if err != nil {
		return domain.Organization{}, fmt.Errorf("lock organization: %w", err)
	}
	return organization, nil
}

func findSubscription(ctx context.Context, db execer, subscriptionID string, lock bool) (*domain.Subscription, error) {
	if subscriptionID == "" {
		return nil, nil
	}
	query := `SELECT ` + subscriptionColumns + ` FROM billing_subscriptions WHERE subscription_id = $1`
	if lock {
		query += ` FOR UPDATE`
	}
	subscription, err := scanSubscriptionFrom(db.QueryRowxContext(ctx, query, subscriptionID))
	if isNoRows(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get subscription: %w", err)
	}
	return &subscription, nil
}

func writeChange(ctx context.Context, tx execer, change domain.Change) error {
	if subscription := change.Subscription; subscription != nil {
		_, err := tx.ExecContext(ctx, `
			INSERT INTO billing_subscriptions (
			    subscription_id, organization_id, customer_id, customer_email, product_id, quantity, status,
			    cancel_at_next_billing_date, next_billing_date, past_due_ends_at, billing_interval,
			    billing_interval_count, event_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)
			ON CONFLICT (subscription_id) DO UPDATE SET
			    customer_id = EXCLUDED.customer_id,
			    customer_email = EXCLUDED.customer_email,
			    product_id = EXCLUDED.product_id,
			    quantity = EXCLUDED.quantity,
			    status = EXCLUDED.status,
			    cancel_at_next_billing_date = EXCLUDED.cancel_at_next_billing_date,
			    next_billing_date = EXCLUDED.next_billing_date,
			    past_due_ends_at = EXCLUDED.past_due_ends_at,
			    billing_interval = EXCLUDED.billing_interval,
			    billing_interval_count = EXCLUDED.billing_interval_count,
			    event_at = EXCLUDED.event_at,
			    updated_at = now()`,
			subscription.ID,
			subscription.OrganizationID,
			subscription.CustomerID,
			subscription.CustomerEmail,
			subscription.ProductID,
			subscription.Quantity,
			subscription.Status,
			subscription.CancelAtNextBillingDate,
			nullableTime(subscription.NextBillingDate),
			subscription.PastDueEndsAt,
			subscription.BillingInterval,
			subscription.BillingIntervalCount,
			subscription.EventAt,
		)
		if err != nil {
			return fmt.Errorf("save subscription: %w", err)
		}
	}

	if organization := change.Organization; organization != nil {
		entitlements := organization.Entitlements
		_, err := tx.ExecContext(ctx, `
			UPDATE billing_organizations
			SET name = CASE
			        WHEN $2::timestamptz IS NULL OR name_updated_at IS NULL OR $2::timestamptz >= name_updated_at THEN $3
			        ELSE name
			    END,
			    name_updated_at = CASE
			        WHEN $2::timestamptz IS NULL THEN name_updated_at
			        WHEN name_updated_at IS NULL OR $2::timestamptz >= name_updated_at THEN $2::timestamptz
			        ELSE name_updated_at
			    END,
			    customer_id = nullif($4, ''),
			    subscription_id = nullif($5, ''),
			    plan = $6,
			    entitlement_status = $7,
			    member_limit = $8,
			    grace_ends_at = $9,
			    entitlements_revision = $10,
			    deleted_at = CASE WHEN $11 THEN coalesce(deleted_at, now()) ELSE NULL END,
			    updated_at = now()
			WHERE organization_id = $1`,
			organization.ID,
			nullableTime(change.NameUpdatedAt),
			organization.Name,
			organization.CustomerID,
			organization.SubscriptionID,
			entitlements.Plan,
			entitlements.Status,
			entitlements.MemberLimit,
			entitlements.GraceEndsAt,
			entitlements.Revision,
			organization.Deleted,
		)
		// A violated constraint here means billing's own records disagree, such
		// as one Dodo customer under two organizations: an integrity failure to
		// investigate, not a conflict a caller could resolve.
		if err != nil {
			return fmt.Errorf("save organization: %w", err)
		}
	}
	return insertEvents(ctx, tx, change.Events)
}

func insertEvents(ctx context.Context, ext execer, events []domain.Event) error {
	// The writing transaction's trace, if it has one, travels with the event
	// from here on: the dispatcher's publisher links its producer span back
	// to it, and a consumer becomes that producer span's child.
	traceContext := observability.CaptureTraceCarrier(ctx).Encode()
	for _, event := range events {
		if _, err := ext.ExecContext(ctx, `
			INSERT INTO billing_outbox (id, detail_type, detail, occurred_at, trace_context)
			VALUES ($1, $2, $3, $4, NULLIF($5, ''))`, event.ID, event.DetailType, event.Data, event.OccurredAt, traceContext); err != nil {
			return fmt.Errorf("insert outbox event: %w", err)
		}
	}
	return nil
}

type scanner interface {
	Scan(dest ...any) error
}

func scanOrganization(row scanner) (domain.Organization, error) {
	var organization domain.Organization
	err := row.Scan(
		&organization.ID,
		&organization.Name,
		&organization.CustomerID,
		&organization.SubscriptionID,
		&organization.Entitlements.Plan,
		&organization.Entitlements.Status,
		&organization.Entitlements.MemberLimit,
		&organization.Entitlements.GraceEndsAt,
		&organization.Entitlements.Revision,
		&organization.Deleted,
	)
	organization.Entitlements.GraceEndsAt = utc(organization.Entitlements.GraceEndsAt)
	return organization, err
}

// scanSubscriptionFrom scans subscriptionColumns, then any extra columns the
// query adds after them.
func scanSubscriptionFrom(row scanner, extra ...any) (domain.Subscription, error) {
	var subscription domain.Subscription
	var nextBillingDate *time.Time
	destinations := []any{
		&subscription.ID,
		&subscription.OrganizationID,
		&subscription.CustomerID,
		&subscription.CustomerEmail,
		&subscription.ProductID,
		&subscription.Quantity,
		&subscription.Status,
		&subscription.CancelAtNextBillingDate,
		&nextBillingDate,
		&subscription.PastDueEndsAt,
		&subscription.BillingInterval,
		&subscription.BillingIntervalCount,
		&subscription.EventAt,
	}
	if err := row.Scan(append(destinations, extra...)...); err != nil {
		return domain.Subscription{}, err
	}
	if nextBillingDate != nil {
		subscription.NextBillingDate = nextBillingDate.UTC()
	}
	subscription.PastDueEndsAt = utc(subscription.PastDueEndsAt)
	subscription.EventAt = subscription.EventAt.UTC()
	return subscription, nil
}

// utc keeps times read from PostgreSQL in UTC, whatever the process's zone.
func utc(value *time.Time) *time.Time {
	if value == nil {
		return nil
	}
	converted := value.UTC()
	return &converted
}

func nullableTime(value time.Time) *time.Time {
	if value.IsZero() {
		return nil
	}
	return &value
}

func isNoRows(err error) bool {
	return errors.Is(err, sql.ErrNoRows) || errors.Is(err, pgx.ErrNoRows)
}
