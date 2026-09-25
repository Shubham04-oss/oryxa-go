-- Realtime's transactional outbox, in the shape every service's has
-- (internal/outbox), which its dispatcher publishes as oryxa.realtime. It
-- holds what the Worker says happened in the web app's conversations, and
-- the audit event of every purge the Worker has done.

CREATE TABLE IF NOT EXISTS realtime_outbox (
    id text PRIMARY KEY,
    detail_type text NOT NULL,
    detail jsonb NOT NULL,
    occurred_at timestamptz NOT NULL,
    attempts integer NOT NULL DEFAULT 0,
    available_at timestamptz NOT NULL DEFAULT now(),
    locked_until timestamptz,
    published_at timestamptz,
    last_error text,
    claim_token text,
    trace_context text
);

CREATE INDEX IF NOT EXISTS realtime_outbox_pending_idx
    ON realtime_outbox (available_at, occurred_at, id)
    WHERE published_at IS NULL;

CREATE INDEX IF NOT EXISTS realtime_outbox_published_idx
    ON realtime_outbox (published_at)
    WHERE published_at IS NOT NULL;
