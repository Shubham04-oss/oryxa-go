CREATE TABLE IF NOT EXISTS billing_organizations (
    organization_id text PRIMARY KEY,
    name text NOT NULL DEFAULT '',
    name_updated_at timestamptz,
    customer_id text UNIQUE,
    subscription_id text,
    plan text NOT NULL DEFAULT '',
    entitlement_status text NOT NULL DEFAULT 'unpaid',
    member_limit integer CHECK (member_limit IS NULL OR member_limit > 0),
    grace_ends_at timestamptz,
    entitlements_revision bigint NOT NULL DEFAULT 0,
    deleted_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS billing_subscriptions (
    subscription_id text PRIMARY KEY,
    organization_id text NOT NULL REFERENCES billing_organizations(organization_id),
    customer_id text NOT NULL,
    customer_email text NOT NULL DEFAULT '',
    product_id text NOT NULL DEFAULT '',
    quantity integer NOT NULL DEFAULT 1,
    status text NOT NULL,
    cancel_at_next_billing_date boolean NOT NULL DEFAULT false,
    next_billing_date timestamptz,
    past_due_ends_at timestamptz,
    billing_interval text NOT NULL DEFAULT '',
    billing_interval_count integer NOT NULL DEFAULT 0,
    event_at timestamptz NOT NULL,
    renewal_reminded_for timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS billing_subscriptions_organization_idx
    ON billing_subscriptions (organization_id);

CREATE INDEX IF NOT EXISTS billing_subscriptions_renewal_idx
    ON billing_subscriptions (next_billing_date, billing_interval)
    WHERE status = 'active' AND NOT cancel_at_next_billing_date;

CREATE TABLE IF NOT EXISTS billing_webhook_deliveries (
    webhook_id text PRIMARY KEY,
    event_type text NOT NULL,
    received_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS billing_webhook_deliveries_received_idx
    ON billing_webhook_deliveries (received_at);

CREATE TABLE IF NOT EXISTS billing_outbox (
    id text PRIMARY KEY,
    detail_type text NOT NULL,
    detail jsonb NOT NULL,
    occurred_at timestamptz NOT NULL,
    attempts integer NOT NULL DEFAULT 0,
    available_at timestamptz NOT NULL DEFAULT now(),
    locked_until timestamptz,
    published_at timestamptz,
    last_error text
);

CREATE INDEX IF NOT EXISTS billing_outbox_pending_idx
    ON billing_outbox (available_at, occurred_at, id)
    WHERE published_at IS NULL;
