CREATE SCHEMA IF NOT EXISTS agents_primary;

CREATE TABLE IF NOT EXISTS agents_primary.agent_interactions (
    id text PRIMARY KEY,
    organization_id text NOT NULL,
    requester_user_id text NOT NULL,
    requester_agent_id text NOT NULL,
    requester_agent_name text NOT NULL,
    target_user_id text NOT NULL,
    target_agent_id text NOT NULL,
    target_agent_name text NOT NULL,
    target_user_email text NOT NULL DEFAULT '',
    target_user_display_name text NOT NULL DEFAULT '',
    purpose text NOT NULL,
    task text NOT NULL,
    context jsonb NOT NULL DEFAULT '[]'::jsonb,
    requested_tools jsonb NOT NULL DEFAULT '[]'::jsonb,
    requested_permissions jsonb NOT NULL DEFAULT '[]'::jsonb,
    status text NOT NULL CHECK (status IN ('pending', 'approved', 'rejected', 'completed', 'failed')),
    decision jsonb,
    result jsonb,
    idempotency_key text NOT NULL,
    revision bigint NOT NULL DEFAULT 1 CHECK (revision > 0),
    expires_at timestamptz NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CHECK (requester_user_id <> target_user_id)
);

CREATE UNIQUE INDEX IF NOT EXISTS agent_interactions_idempotency_idx
    ON agents_primary.agent_interactions (organization_id, requester_agent_id, idempotency_key);

CREATE INDEX IF NOT EXISTS agent_interactions_inbox_idx
    ON agents_primary.agent_interactions (organization_id, target_user_id, updated_at DESC, id DESC);

CREATE INDEX IF NOT EXISTS agent_interactions_sent_idx
    ON agents_primary.agent_interactions (organization_id, requester_user_id, updated_at DESC, id DESC);
