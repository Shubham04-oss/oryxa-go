-- The service's transactional outbox, in the shape every service's has
-- (internal/outbox), which its dispatcher publishes as oryxa.integrations.
-- A connection's events are written here in the transaction that changes the
-- connection, so an event is published if and only if the change committed.

CREATE TABLE IF NOT EXISTS integrations.outbox (
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

CREATE INDEX IF NOT EXISTS outbox_pending_idx
    ON integrations.outbox (available_at, occurred_at, id)
    WHERE published_at IS NULL;

CREATE INDEX IF NOT EXISTS outbox_published_idx
    ON integrations.outbox (published_at)
    WHERE published_at IS NOT NULL;
