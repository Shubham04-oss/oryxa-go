-- The time of the last event the projector applied to each relationship key,
-- so an older event that arrives late (SQS doesn't order deliveries) can't
-- undo a newer one. Keys are written by the projector, see ordering.go.
CREATE TABLE IF NOT EXISTS access.spicedb_seen (
    key text PRIMARY KEY,
    occurred_at timestamptz NOT NULL
);

CREATE INDEX IF NOT EXISTS spicedb_seen_occurred_at ON access.spicedb_seen (occurred_at);
