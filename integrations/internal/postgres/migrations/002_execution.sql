CREATE UNIQUE INDEX IF NOT EXISTS integrations_connections_org_id_unique
    ON integrations_connections (organization_id, id);

CREATE TABLE IF NOT EXISTS integrations_triggers (
    id text PRIMARY KEY,
    organization_id text NOT NULL,
    member_id text NOT NULL,
    connection_id text NOT NULL,
    profile_id text NOT NULL,
    provider text NOT NULL,
    piece_name text NOT NULL,
    piece_version text NOT NULL,
    trigger_name text NOT NULL,
    provider_identifier text NOT NULL CHECK (provider_identifier <> ''),
    props jsonb NOT NULL DEFAULT '{}'::jsonb,
    status text NOT NULL CHECK (status IN ('active', 'disabled')),
    version bigint NOT NULL CHECK (version > 0),
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    UNIQUE (organization_id, id),
    FOREIGN KEY (organization_id, connection_id)
        REFERENCES integrations_connections(organization_id, id)
);

CREATE INDEX IF NOT EXISTS integrations_triggers_delivery_idx
    ON integrations_triggers (
        organization_id, profile_id, piece_name, piece_version,
        trigger_name, provider_identifier
    )
    WHERE status = 'active';

CREATE UNIQUE INDEX IF NOT EXISTS integrations_triggers_connection_operation_active_unique
    ON integrations_triggers (organization_id, connection_id, trigger_name)
    WHERE status = 'active';

CREATE TABLE IF NOT EXISTS integrations_trigger_executions (
    id text PRIMARY KEY,
    organization_id text NOT NULL,
    trigger_id text NOT NULL,
    invocation_id text NOT NULL,
    status text NOT NULL CHECK (status IN ('processing', 'succeeded', 'failed')),
    attempt integer NOT NULL CHECK (attempt > 0),
    started_at timestamptz NOT NULL,
    completed_at timestamptz,
    last_error_code text NOT NULL DEFAULT '',
    UNIQUE (trigger_id, invocation_id),
    FOREIGN KEY (organization_id, trigger_id)
        REFERENCES integrations_triggers(organization_id, id)
);

CREATE INDEX IF NOT EXISTS integrations_trigger_executions_status_idx
    ON integrations_trigger_executions (status, started_at);
