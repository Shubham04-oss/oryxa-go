CREATE TABLE IF NOT EXISTS integrations_action_executions (
    id text PRIMARY KEY,
    organization_id text NOT NULL,
    member_id text NOT NULL,
    connection_id text NOT NULL,
    provider text NOT NULL,
    piece_name text NOT NULL,
    piece_version text NOT NULL,
    action_name text NOT NULL,
    idempotency_key_hash text NOT NULL CHECK (length(idempotency_key_hash) = 64),
    input_hash text NOT NULL CHECK (length(input_hash) = 64),
    status text NOT NULL CHECK (status IN ('processing', 'succeeded', 'failed')),
    attempt integer NOT NULL CHECK (attempt > 0),
    safe_result jsonb,
    started_at timestamptz NOT NULL,
    completed_at timestamptz,
    last_error_code text NOT NULL DEFAULT '',
    UNIQUE (organization_id, connection_id, action_name, idempotency_key_hash),
    FOREIGN KEY (organization_id, connection_id)
        REFERENCES integrations_connections(organization_id, id)
);

CREATE INDEX IF NOT EXISTS integrations_action_executions_audit_idx
    ON integrations_action_executions (organization_id, member_id, started_at DESC);
