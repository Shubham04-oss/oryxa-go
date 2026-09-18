-- Batching owns app trigger events from receipt until it has atomically
-- created the batch-ready event that releases them.

CREATE TABLE IF NOT EXISTS batching_triggers (
    id text PRIMARY KEY,
    trigger_id text NOT NULL,
    connection_id text NOT NULL,
    organization_id text NOT NULL,
    member_id text NOT NULL,
    toolkit text NOT NULL,
    trigger text NOT NULL,
    data jsonb,
    data_omitted boolean NOT NULL DEFAULT false,
    received_at timestamptz NOT NULL,
    lane text NOT NULL CHECK (lane IN ('digest', 'immediate')),
    priority double precision NOT NULL CHECK (priority >= 0 AND priority <= 1),
    classifier text NOT NULL,
    decision_reason text NOT NULL DEFAULT '',
    recorded_at timestamptz NOT NULL DEFAULT now(),
    batch_id text,
    claimed_until timestamptz,
    batched_at timestamptz
);

CREATE INDEX IF NOT EXISTS batching_triggers_waiting_idx
    ON batching_triggers (lane, organization_id, member_id, recorded_at, id)
    WHERE batched_at IS NULL;

CREATE INDEX IF NOT EXISTS batching_triggers_batched_idx
    ON batching_triggers (batched_at)
    WHERE batched_at IS NOT NULL;

CREATE TABLE IF NOT EXISTS batching_outbox (
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

CREATE INDEX IF NOT EXISTS batching_outbox_pending_idx
    ON batching_outbox (available_at, occurred_at, id)
    WHERE published_at IS NULL;

CREATE INDEX IF NOT EXISTS batching_outbox_published_idx
    ON batching_outbox (published_at)
    WHERE published_at IS NOT NULL;
