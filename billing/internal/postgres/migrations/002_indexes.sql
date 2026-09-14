-- Indexes that match the queries. 001 used to omit id on the outbox claim
-- index and billing_interval on renewal; replace those definitions once.

CREATE INDEX IF NOT EXISTS billing_organizations_active_idx
    ON billing_organizations (organization_id)
    WHERE deleted_at IS NULL;

DO $$
BEGIN
    IF EXISTS (
        SELECT 1
        FROM pg_indexes
        WHERE schemaname = current_schema()
          AND indexname = 'billing_subscriptions_renewal_idx'
          AND indexdef NOT LIKE '%billing_interval%'
    ) THEN
        DROP INDEX billing_subscriptions_renewal_idx;
    END IF;
    IF EXISTS (
        SELECT 1
        FROM pg_indexes
        WHERE schemaname = current_schema()
          AND indexname = 'billing_outbox_pending_idx'
          AND indexdef NOT LIKE '%(available_at, occurred_at, id)%'
    ) THEN
        DROP INDEX billing_outbox_pending_idx;
    END IF;
END $$;

CREATE INDEX IF NOT EXISTS billing_subscriptions_renewal_idx
    ON billing_subscriptions (next_billing_date, billing_interval)
    WHERE status = 'active' AND NOT cancel_at_next_billing_date;

CREATE INDEX IF NOT EXISTS billing_outbox_pending_idx
    ON billing_outbox (available_at, occurred_at, id)
    WHERE published_at IS NULL;
