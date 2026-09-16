-- Actions are what a caller ran with a connection: a tool call an agent made,
-- or a step other work ran. The idempotency key is what makes a repeat of the
-- same action on the same connection return the first outcome instead of
-- running it twice; the unique constraint below is what enforces that.
CREATE TABLE IF NOT EXISTS integrations_actions (
    id text PRIMARY KEY,
    organization_id text NOT NULL,
    connection_id text NOT NULL,
    app_id text NOT NULL,
    provider text NOT NULL CHECK (length(provider) BETWEEN 1 AND 80),
    action text NOT NULL CHECK (length(action) BETWEEN 1 AND 100),
    idempotency_key text NOT NULL CHECK (length(idempotency_key) BETWEEN 8 AND 200),
    scope text NOT NULL CHECK (scope IN ('member', 'team', 'organization')),
    team_id text,
    member_id text,
    provider_account_id text NOT NULL,
    provider_account_name text NOT NULL DEFAULT '',
    requested_by text NOT NULL,
    status text NOT NULL CHECK (status IN ('running', 'completed', 'failed')),
    error text NOT NULL DEFAULT '',
    -- resource is what the action created or changed at the provider; result
    -- is what it reports back to the caller. Both are empty while it runs.
    resource jsonb,
    result jsonb,
    started_at timestamptz NOT NULL,
    ended_at timestamptz,
    -- One connection never runs the same action under the same idempotency
    -- key twice: a repeat insert conflicts here, and the caller reads back
    -- the first outcome instead of running anything.
    UNIQUE (connection_id, action, idempotency_key)
);

-- Reading an organization's recent actions.
CREATE INDEX IF NOT EXISTS integrations_actions_org_started_idx
    ON integrations_actions (organization_id, started_at DESC);

-- DeleteActionsBefore only ever matches ended actions; a running action
-- (ended_at IS NULL) is never in this index.
CREATE INDEX IF NOT EXISTS integrations_actions_ended_idx
    ON integrations_actions (ended_at)
    WHERE ended_at IS NOT NULL;
