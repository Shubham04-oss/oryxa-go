-- The work queue and the streams the scheduler keeps due (design §6, §10).
-- jobs carries org_id, which the design's column list leaves out: workers
-- take turns across orgs within a priority (§10.1).
CREATE TABLE IF NOT EXISTS integrations.jobs (
    id text PRIMARY KEY,
    org_id text NOT NULL,
    kind text NOT NULL CHECK (kind IN ('sync', 'slice', 'poll', 'refetch', 'renew', 'reconcile', 'revoke', 'refresh')),
    connection_id text NOT NULL,
    -- Empty for a job on the connection itself (refresh, renew).
    stream text NOT NULL DEFAULT '',
    payload jsonb NOT NULL DEFAULT '{}'::jsonb,
    -- Lower runs first.
    priority smallint NOT NULL,
    -- At most one queued or running job holds a dedupe key, so a scheduler
    -- that ticks twice before a sync ends does not queue two.
    dedupe_key text NOT NULL DEFAULT '',
    run_after timestamptz NOT NULL,
    lease_until timestamptz,
    lease_owner text,
    attempts integer NOT NULL DEFAULT 0,
    status text NOT NULL CHECK (status IN ('queued', 'running', 'done', 'failed')),
    last_error text NOT NULL DEFAULT '',
    claimed_at timestamptz,
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL
);

CREATE INDEX IF NOT EXISTS jobs_queued_idx ON integrations.jobs (priority, run_after) WHERE status = 'queued';
CREATE INDEX IF NOT EXISTS jobs_running_idx ON integrations.jobs (lease_until) WHERE status = 'running';
CREATE INDEX IF NOT EXISTS jobs_org_claimed_idx ON integrations.jobs (org_id, claimed_at);
CREATE UNIQUE INDEX IF NOT EXISTS jobs_dedupe_idx ON integrations.jobs (dedupe_key)
    WHERE dedupe_key <> '' AND status IN ('queued', 'running');
-- The backstop for one running job per (connection, stream): the claim
-- filters on it, and this refuses the claim that raced past the filter.
CREATE UNIQUE INDEX IF NOT EXISTS jobs_running_stream_idx ON integrations.jobs (connection_id, stream)
    WHERE status = 'running';

-- Sync position per stream and partition. The scheduler reads next_run_at;
-- the sync run (#294) writes checkpoint and last_run_at.
CREATE TABLE IF NOT EXISTS integrations.stream_state (
    connection_id text NOT NULL REFERENCES integrations.connections (id),
    stream text NOT NULL,
    partition text NOT NULL DEFAULT '',
    checkpoint jsonb NOT NULL DEFAULT '{}'::jsonb,
    last_run_at timestamptz,
    next_run_at timestamptz NOT NULL,
    status text NOT NULL CHECK (status IN ('active', 'paused', 'degraded')),
    PRIMARY KEY (connection_id, stream, partition)
);

CREATE INDEX IF NOT EXISTS stream_state_due_idx ON integrations.stream_state (next_run_at) WHERE status = 'active';
