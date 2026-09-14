-- Disabling a GitHub trigger, connection, or OAuth profile must be durable even
-- if GitHub is unavailable at that instant. This queue contains no credential
-- material: the worker reloads the exact connection under the organization
-- boundary.
CREATE TABLE IF NOT EXISTS integrations_github_hook_cleanup (
    organization_id text NOT NULL,
    trigger_id text NOT NULL,
    connection_id text NOT NULL,
    profile_id text NOT NULL,
    trigger_name text NOT NULL,
    created_at timestamptz NOT NULL,
    attempts integer NOT NULL DEFAULT 0 CHECK (attempts >= 0),
    available_at timestamptz NOT NULL,
    claim_token text,
    locked_until timestamptz,
    completed_at timestamptz,
    last_error text,
    PRIMARY KEY (organization_id, trigger_id)
);

CREATE INDEX IF NOT EXISTS integrations_github_hook_cleanup_pending_idx
    ON integrations_github_hook_cleanup (available_at, created_at, trigger_id)
    WHERE completed_at IS NULL;
