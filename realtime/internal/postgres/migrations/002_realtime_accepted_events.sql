-- The ids of the Worker's events realtime has accepted. The Worker keeps an
-- event's id across its retries, so one sent again after realtime took it,
-- because the answer was lost on the way back, is acknowledged without a
-- second event. An id is kept for seven days, which outlasts every retry,
-- and then deleted (postgres.AcceptedRetention).

CREATE TABLE IF NOT EXISTS realtime_accepted_events (
    id text PRIMARY KEY CHECK (id ~ '^[A-Za-z0-9_-]{1,200}$'),
    accepted_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS realtime_accepted_events_accepted_idx
    ON realtime_accepted_events (accepted_at);
