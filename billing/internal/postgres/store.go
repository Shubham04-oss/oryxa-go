package postgres

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Shubham04-oss/oryxa-go/billing/internal/domain"
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
		return fmt.Errorf("list billing migrations: %w", err)
	}
	slices.Sort(names)
	for _, name := range names {
		contents, err := migrations.ReadFile(name)
		if err != nil {
			return fmt.Errorf("read billing migration %s: %w", name, err)
		}
		if _, err := store.pool.Exec(ctx, string(contents)); err != nil {
			return fmt.Errorf("apply billing migration %s: %w", name, err)
		}
	}
	return nil
}

const organizationColumns = `organization_id, name, coalesce(customer_id, ''), coalesce(subscription_id, ''),
	plan, entitlement_status, member_limit, grace_ends_at, entitlements_revision, deleted_at IS NOT NULL`

const subscriptionColumns = `subscription_id, organization_id, customer_id, customer_email, product_id, quantity,
	status, cancel_at_next_billing_date, next_billing_date, past_due_ends_at, billing_interval,
	billing_interval_count, event_at`

type queryer interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}

// Organization returns the organization and its current subscription.
func (store *Store) Organization(ctx context.Context, organizationID string) (domain.Organization, *domain.Subscription, error) {
	organization, err := scanOrganization(store.pool.QueryRow(ctx,
		`SELECT `+organizationColumns+` FROM billing_organizations WHERE organization_id = $1`, organizationID))
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Organization{}, nil, domain.ErrNotFound
	}
	if err != nil {
		return domain.Organization{}, nil, fmt.Errorf("get organization: %w", err)
	}
	current, err := findSubscription(ctx, store.pool, organization.SubscriptionID, false)
	if err != nil {
		return domain.Organization{}, nil, err
	}
	return organization, current, nil
}

// UpdateOrganization reads the organization, creating it if billing hasn't
// seen it, and writes back what update returns, all under a row lock.
func (store *Store) UpdateOrganization(ctx context.Context, organizationID string, update func(domain.OrganizationState) (domain.Change, error)) error {
	tx, err := store.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin organization update: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

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
	if err := writeChange(ctx, tx, change); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit organization update: %w", err)
	}
	return nil
}

// ApplyWebhook records the delivery and applies it once. Retries of a
// delivery already recorded change nothing.
func (store *Store) ApplyWebhook(ctx context.Context, delivery domain.WebhookDelivery, incoming domain.Subscription, update func(domain.WebhookState) (domain.Change, error)) (domain.WebhookOutcome, error) {
	tx, err := store.pool.Begin(ctx)
	if err != nil {
		return "", fmt.Errorf("begin webhook transaction: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	recorded, err := tx.Exec(ctx, `
		INSERT INTO billing_webhook_deliveries (webhook_id, event_type)
		VALUES ($1, $2)
		ON CONFLICT (webhook_id) DO NOTHING`, delivery.ID, delivery.Type)
	if err != nil {
		return "", fmt.Errorf("record webhook delivery: %w", err)
	}
	if recorded.RowsAffected() == 0 {
		return domain.WebhookDuplicate, tx.Commit(ctx)
	}

	organizationID, err := webhookOrganization(ctx, tx, incoming)
	if err != nil {
		return "", err
	}
	if organizationID == "" {
		if err := tx.Commit(ctx); err != nil {
			return "", fmt.Errorf("commit unmatched webhook: %w", err)
		}
		return domain.WebhookUnmatched, nil
	}

	organization, err := lockOrganization(ctx, tx, organizationID)
	if err != nil {
		return "", err
	}
	previous, err := findSubscription(ctx, tx, incoming.ID, true)
	if err != nil {
		return "", err
	}
	var current *domain.Subscription
	if organization.SubscriptionID != incoming.ID {
		if current, err = findSubscription(ctx, tx, organization.SubscriptionID, true); err != nil {
			return "", err
		}
	}
	change, err := update(domain.WebhookState{Organization: organization, Previous: previous, Current: current})
	if err != nil {
		return "", err
	}
	if err := writeChange(ctx, tx, change); err != nil {
		return "", err
	}
	if err := tx.Commit(ctx); err != nil {
		return "", fmt.Errorf("commit webhook transaction: %w", err)
	}
	return domain.WebhookApplied, nil
}

// webhookOrganization finds whose subscription this is: the organization it
// is already recorded under, the one its metadata names, or the owner of its
// customer.
func webhookOrganization(ctx context.Context, tx pgx.Tx, incoming domain.Subscription) (string, error) {
	var organizationID string
	err := tx.QueryRow(ctx, `SELECT organization_id FROM billing_subscriptions WHERE subscription_id = $1`, incoming.ID).Scan(&organizationID)
	if err == nil {
		return organizationID, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return "", fmt.Errorf("find subscription organization: %w", err)
	}
	if incoming.OrganizationID != "" {
		return incoming.OrganizationID, nil
	}
	err = tx.QueryRow(ctx, `SELECT organization_id FROM billing_organizations WHERE customer_id = $1`, incoming.CustomerID).Scan(&organizationID)
	if errors.Is(err, pgx.ErrNoRows) {
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
	tx, err := store.pool.Begin(ctx)
	if err != nil {
		return 0, fmt.Errorf("begin renewal claim: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	rows, err := tx.Query(ctx, `
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
		return 0, fmt.Errorf("find due renewals: %w", err)
	}
	var renewals []domain.Renewal
	for rows.Next() {
		var renewal domain.Renewal
		subscription, err := scanSubscriptionFrom(rows, &renewal.OrganizationName)
		if err != nil {
			rows.Close()
			return 0, fmt.Errorf("scan renewal: %w", err)
		}
		renewal.Subscription = subscription
		renewals = append(renewals, renewal)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, fmt.Errorf("iterate renewals: %w", err)
	}

	for _, renewal := range renewals {
		event, err := remind(renewal)
		if err != nil {
			return 0, err
		}
		if event != nil {
			if err := insertEvents(ctx, tx, []domain.Event{*event}); err != nil {
				return 0, err
			}
		}
		if _, err := tx.Exec(ctx, `
			UPDATE billing_subscriptions
			SET renewal_reminded_for = next_billing_date, updated_at = now()
			WHERE subscription_id = $1`, renewal.Subscription.ID); err != nil {
			return 0, fmt.Errorf("mark renewal reminded: %w", err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, fmt.Errorf("commit renewal claim: %w", err)
	}
	return len(renewals), nil
}

// OrganizationIDs lists every organization that has not been deleted.
func (store *Store) OrganizationIDs(ctx context.Context) ([]string, error) {
	rows, err := store.pool.Query(ctx, `
		SELECT organization_id FROM billing_organizations
		WHERE deleted_at IS NULL
		ORDER BY organization_id`)
	if err != nil {
		return nil, fmt.Errorf("list organizations: %w", err)
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return nil, fmt.Errorf("list organizations: %w", err)
	}
	return ids, nil
}

// PruneWebhookDeliveries forgets deliveries received before cutoff, long after
// Dodo stops retrying them.
func (store *Store) PruneWebhookDeliveries(ctx context.Context, cutoff time.Time) error {
	if _, err := store.pool.Exec(ctx, `DELETE FROM billing_webhook_deliveries WHERE received_at < $1`, cutoff); err != nil {
		return fmt.Errorf("prune webhook deliveries: %w", err)
	}
	return nil
}

func lockOrganization(ctx context.Context, tx pgx.Tx, organizationID string) (domain.Organization, error) {
	if _, err := tx.Exec(ctx, `
		INSERT INTO billing_organizations (organization_id)
		VALUES ($1)
		ON CONFLICT (organization_id) DO NOTHING`, organizationID); err != nil {
		return domain.Organization{}, fmt.Errorf("record organization: %w", err)
	}
	organization, err := scanOrganization(tx.QueryRow(ctx,
		`SELECT `+organizationColumns+` FROM billing_organizations WHERE organization_id = $1 FOR UPDATE`, organizationID))
	if err != nil {
		return domain.Organization{}, fmt.Errorf("lock organization: %w", err)
	}
	return organization, nil
}

func findSubscription(ctx context.Context, db queryer, subscriptionID string, lock bool) (*domain.Subscription, error) {
	if subscriptionID == "" {
		return nil, nil
	}
	query := `SELECT ` + subscriptionColumns + ` FROM billing_subscriptions WHERE subscription_id = $1`
	if lock {
		query += ` FOR UPDATE`
	}
	subscription, err := scanSubscriptionFrom(db.QueryRow(ctx, query, subscriptionID))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get subscription: %w", err)
	}
	return &subscription, nil
}

func writeChange(ctx context.Context, tx pgx.Tx, change domain.Change) error {
	if subscription := change.Subscription; subscription != nil {
		_, err := tx.Exec(ctx, `
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
		_, err := tx.Exec(ctx, `
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

func insertEvents(ctx context.Context, tx pgx.Tx, events []domain.Event) error {
	for _, event := range events {
		_, err := tx.Exec(ctx, `
			INSERT INTO billing_outbox (id, detail_type, detail, occurred_at)
			VALUES ($1, $2, $3, $4)`, event.ID, event.DetailType, event.Data, event.OccurredAt)
		if err != nil {
			return fmt.Errorf("insert outbox event: %w", err)
		}
	}
	return nil
}

func scanOrganization(row pgx.Row) (domain.Organization, error) {
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
func scanSubscriptionFrom(row pgx.Row, extra ...any) (domain.Subscription, error) {
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
