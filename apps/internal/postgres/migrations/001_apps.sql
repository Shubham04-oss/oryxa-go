-- Apps owns which member connected which app in which organization, the
-- triggers subscribed on those connections, and the tools run through them.
-- Composio holds every credential, so nothing here is one.

CREATE TABLE IF NOT EXISTS apps_connections (
    id text PRIMARY KEY,
    organization_id text NOT NULL,
    member_id text NOT NULL,
    toolkit text NOT NULL,
    auth_config_id text NOT NULL,
    composio_user_id text NOT NULL,
    composio_account_id text,
    status text NOT NULL CHECK (status IN ('pending', 'active', 'failed', 'expired', 'removed')),
    status_reason text NOT NULL DEFAULT '',
    label text NOT NULL DEFAULT '',
    -- state_hash names a pending connection's browser round trip without
    -- storing the state itself.
    state_hash text,
    return_to text NOT NULL DEFAULT '',
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    connected_at timestamptz,
    removed_at timestamptz
);

CREATE UNIQUE INDEX IF NOT EXISTS apps_connections_account_idx
    ON apps_connections (composio_account_id)
    WHERE composio_account_id IS NOT NULL;

CREATE UNIQUE INDEX IF NOT EXISTS apps_connections_state_idx
    ON apps_connections (state_hash)
    WHERE state_hash IS NOT NULL;

CREATE INDEX IF NOT EXISTS apps_connections_member_idx
    ON apps_connections (organization_id, member_id, toolkit)
    WHERE status <> 'removed';

-- One auth config per toolkit, created the first time a member connects it.
CREATE TABLE IF NOT EXISTS apps_auth_configs (
    toolkit text PRIMARY KEY,
    auth_config_id text NOT NULL,
    auth_scheme text NOT NULL,
    composio_managed boolean NOT NULL,
    created_at timestamptz NOT NULL
);

CREATE TABLE IF NOT EXISTS apps_triggers (
    id text PRIMARY KEY,
    organization_id text NOT NULL,
    member_id text NOT NULL,
    connection_id text NOT NULL REFERENCES apps_connections (id),
    toolkit text NOT NULL,
    slug text NOT NULL,
    config jsonb NOT NULL,
    composio_trigger_id text NOT NULL,
    status text NOT NULL CHECK (status IN ('active', 'disabled', 'removed')),
    status_reason text NOT NULL DEFAULT '',
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL
);

-- Composio re-enables and answers the same instance for the same account,
-- trigger and config, so one live row per instance.
CREATE UNIQUE INDEX IF NOT EXISTS apps_triggers_instance_idx
    ON apps_triggers (composio_trigger_id)
    WHERE status <> 'removed';

CREATE INDEX IF NOT EXISTS apps_triggers_connection_idx
    ON apps_triggers (connection_id)
    WHERE status <> 'removed';

CREATE TABLE IF NOT EXISTS apps_actions (
    id text PRIMARY KEY,
    organization_id text NOT NULL,
    member_id text NOT NULL,
    connection_id text NOT NULL,
    toolkit text NOT NULL,
    tool text NOT NULL,
    read_only boolean NOT NULL,
    agent boolean NOT NULL,
    idempotency_key text,
    status text NOT NULL CHECK (status IN ('running', 'succeeded', 'failed')),
    result jsonb,
    error text NOT NULL DEFAULT '',
    log_id text NOT NULL DEFAULT '',
    created_at timestamptz NOT NULL,
    finished_at timestamptz
);

-- A key names one write per member per organization: asked again, it answers
-- what happened the first time.
CREATE UNIQUE INDEX IF NOT EXISTS apps_actions_idempotency_idx
    ON apps_actions (organization_id, member_id, idempotency_key)
    WHERE idempotency_key IS NOT NULL;

CREATE INDEX IF NOT EXISTS apps_actions_created_idx
    ON apps_actions (created_at);

-- Webhook deliveries and bus events already acted on, since both arrive at
-- least once.
CREATE TABLE IF NOT EXISTS apps_handled_events (
    event_id text PRIMARY KEY,
    detail_type text NOT NULL,
    handled_at timestamptz NOT NULL
);

CREATE INDEX IF NOT EXISTS apps_handled_events_handled_idx
    ON apps_handled_events (handled_at);

CREATE TABLE IF NOT EXISTS apps_outbox (
    id text PRIMARY KEY,
    detail_type text NOT NULL,
    detail jsonb NOT NULL,
    occurred_at timestamptz NOT NULL,
    attempts integer NOT NULL DEFAULT 0,
    available_at timestamptz NOT NULL DEFAULT now(),
    locked_until timestamptz,
    published_at timestamptz,
    last_error text,
    claim_token text
);

CREATE INDEX IF NOT EXISTS apps_outbox_pending_idx
    ON apps_outbox (available_at, occurred_at, id)
    WHERE published_at IS NULL;

CREATE INDEX IF NOT EXISTS apps_outbox_published_idx
    ON apps_outbox (published_at)
    WHERE published_at IS NOT NULL;
