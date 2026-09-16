-- Subscriptions are connections' interest in a provider's events. A webhook
-- subscription's secret, which the provider signs or marks its deliveries
-- with, is envelope-encrypted. A subscription that stopped or failed keeps its
-- row until what it registered at the provider is removed.
CREATE TABLE IF NOT EXISTS integrations_subscriptions (
    id text PRIMARY KEY,
    organization_id text NOT NULL,
    connection_id text NOT NULL,
    app_id text NOT NULL,
    provider text NOT NULL CHECK (length(provider) BETWEEN 1 AND 80),
    scope text NOT NULL CHECK (scope IN ('member', 'team', 'organization')),
    team_id text,
    member_id text,
    provider_account_id text NOT NULL,
    provider_account_name text NOT NULL DEFAULT '',
    account_key text NOT NULL DEFAULT '',
    target jsonb NOT NULL DEFAULT '{}'::jsonb,
    events text[] NOT NULL DEFAULT '{}',
    route text NOT NULL UNIQUE CHECK (length(route) BETWEEN 32 AND 64),
    registration_id text NOT NULL DEFAULT '',
    encrypted_secret jsonb,
    status text NOT NULL CHECK (status IN ('pending', 'active', 'failed', 'stopped')),
    reason text NOT NULL DEFAULT '',
    created_by text NOT NULL,
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    expires_at timestamptz,
    renew_at timestamptz,
    renew_attempts integer NOT NULL DEFAULT 0,
    stopped_by text NOT NULL DEFAULT '',
    stopped_at timestamptz,
    -- cleanup_at is when removing the registration at the provider is next
    -- tried; NULL once there is nothing to remove.
    cleanup_at timestamptz,
    cleanup_attempts integer NOT NULL DEFAULT 0,
    lease_token text,
    lease_until timestamptz,
    version bigint NOT NULL DEFAULT 1 CHECK (version > 0),
    UNIQUE (organization_id, id),
    CONSTRAINT integrations_subscriptions_placement_check CHECK (
        (scope = 'member' AND member_id IS NOT NULL AND team_id IS NULL)
        OR (scope = 'team' AND team_id IS NOT NULL AND member_id IS NULL)
        OR (scope = 'organization' AND team_id IS NULL AND member_id IS NULL)
    ),
    CONSTRAINT integrations_subscriptions_cleanup_check CHECK (
        cleanup_at IS NULL OR status IN ('failed', 'stopped')
    )
);

CREATE INDEX IF NOT EXISTS integrations_subscriptions_connection_idx
    ON integrations_subscriptions (organization_id, connection_id, status);

CREATE INDEX IF NOT EXISTS integrations_subscriptions_creator_idx
    ON integrations_subscriptions (organization_id, created_by)
    WHERE status IN ('pending', 'active');

-- App deliveries reach the live subscriptions of the app's account.
CREATE INDEX IF NOT EXISTS integrations_subscriptions_account_idx
    ON integrations_subscriptions (app_id, account_key)
    WHERE status = 'active';

CREATE INDEX IF NOT EXISTS integrations_subscriptions_renew_idx
    ON integrations_subscriptions (renew_at)
    WHERE status = 'active' AND renew_at IS NOT NULL;

CREATE INDEX IF NOT EXISTS integrations_subscriptions_cleanup_idx
    ON integrations_subscriptions (cleanup_at)
    WHERE cleanup_at IS NOT NULL;

-- App webhooks are where a provider that delivers through its app sends one
-- organization app's events, and the envelope-encrypted secret they are signed
-- with.
CREATE TABLE IF NOT EXISTS integrations_app_webhooks (
    app_id text PRIMARY KEY,
    organization_id text NOT NULL,
    provider text NOT NULL CHECK (length(provider) BETWEEN 1 AND 80),
    route text NOT NULL UNIQUE CHECK (length(route) BETWEEN 32 AND 64),
    encrypted_secret jsonb NOT NULL,
    updated_by text NOT NULL,
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    UNIQUE (organization_id, app_id)
);

-- Deliveries remember what each subscription published, so a delivery the
-- provider repeats is published once. Rows older than a week are removed.
CREATE TABLE IF NOT EXISTS integrations_deliveries (
    subscription_id text NOT NULL REFERENCES integrations_subscriptions (id) ON DELETE CASCADE,
    delivery_key text NOT NULL CHECK (length(delivery_key) BETWEEN 1 AND 200),
    received_at timestamptz NOT NULL,
    PRIMARY KEY (subscription_id, delivery_key)
);

CREATE INDEX IF NOT EXISTS integrations_deliveries_received_idx
    ON integrations_deliveries (received_at);

CREATE TABLE IF NOT EXISTS integrations_outbox (
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

CREATE INDEX IF NOT EXISTS integrations_outbox_pending_idx
    ON integrations_outbox (available_at, occurred_at, id)
    WHERE published_at IS NULL;
