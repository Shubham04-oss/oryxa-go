-- Agent runs live in agents_primary, the same schema ADK sessions and the
-- agents outbox already use. This migration creates that schema, so it does
-- not matter whether it is the first thing to run against a fresh database.

CREATE SCHEMA IF NOT EXISTS agents_primary;

CREATE TABLE IF NOT EXISTS agents_primary.agent_runs (
    id text PRIMARY KEY,
    session_id text NOT NULL,
    user_id text NOT NULL,
    organization_id text NOT NULL,
    started_by text NOT NULL,
    status text NOT NULL CHECK (status IN ('running', 'completed', 'failed', 'abandoned')),
    started_at timestamptz NOT NULL DEFAULT now(),
    -- lease_until is set only while the run is running: Start sets it, Renew
    -- pushes it forward, and Finish clears it. The next Start on the same
    -- conversation abandons a run whose lease it finds already lapsed.
    lease_until timestamptz,
    finished_at timestamptz,
    error text,
    input_tokens integer NOT NULL DEFAULT 0,
    output_tokens integer NOT NULL DEFAULT 0,
    cached_tokens integer NOT NULL DEFAULT 0,
    total_tokens integer NOT NULL DEFAULT 0,
    cost_micros bigint NOT NULL DEFAULT 0
);

-- This partial index, not application logic, is what keeps a second run off
-- a conversation across every replica: Start's INSERT relies on it failing
-- with a unique violation when one is already running.
CREATE UNIQUE INDEX IF NOT EXISTS agent_runs_one_live_per_session
    ON agents_primary.agent_runs (session_id)
    WHERE status = 'running';

CREATE INDEX IF NOT EXISTS agent_runs_by_member
    ON agents_primary.agent_runs (organization_id, user_id, started_at DESC);

-- The events outbox belongs to package events (see events.Outbox.Ensure),
-- which agent_runs shares without importing: Finish writes a run's outcome
-- and its announcing event in the same transaction, so it needs the outbox
-- to exist too. The statements below are copied from Ensure, unchanged, so
-- whichever of the two runs first against a fresh database creates the
-- table and the other's IF NOT EXISTS is simply a no-op.
CREATE TABLE IF NOT EXISTS agents_primary.agents_outbox (
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

CREATE INDEX IF NOT EXISTS agents_outbox_pending_idx
    ON agents_primary.agents_outbox (available_at, occurred_at, id)
    WHERE published_at IS NULL;

-- A claim is owned, by internal/outbox: it stamps the rows it hands out with
-- a token, and marking published or failed only touches a row that still
-- carries that same token. See events.Outbox.Ensure for the full reasoning.
ALTER TABLE agents_primary.agents_outbox ADD COLUMN IF NOT EXISTS claim_token text;

-- The event pipeline now carries a W3C trace context end to end: an outbox
-- row records the trace of the transaction that wrote it, so the
-- dispatcher's publisher can link its producer span back to that write, and
-- a consumer can become its child. NULL means the write carried no span,
-- which is ordinary for anything that runs unsampled or outside a traced
-- request. See events.Outbox.Ensure for the full reasoning.
ALTER TABLE agents_primary.agents_outbox ADD COLUMN IF NOT EXISTS trace_context text;
