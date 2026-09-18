-- Wake-ups keep what a member's apps reported until a run hands it to Krude,
-- in agents_primary beside the runs and ADK's sessions.

CREATE SCHEMA IF NOT EXISTS agents_primary;

-- One row per trigger event. The event's own id is the key, so a delivery
-- the bus repeats is recorded once. A batch claims its rows with a lease; a
-- handled row keeps its id, to recognise a late repeat, and loses its data.
CREATE TABLE IF NOT EXISTS agents_primary.wakeup_events (
    id text PRIMARY KEY,
    organization_id text NOT NULL,
    member_id text NOT NULL,
    toolkit text NOT NULL,
    trigger text NOT NULL,
    data jsonb,
    data_omitted boolean NOT NULL DEFAULT false,
    received_at timestamptz NOT NULL,
    recorded_at timestamptz NOT NULL DEFAULT now(),
    batch_id text,
    claimed_until timestamptz,
    attempts integer NOT NULL DEFAULT 0,
    handled_at timestamptz,
    dropped boolean NOT NULL DEFAULT false
);

CREATE INDEX IF NOT EXISTS wakeup_events_waiting
    ON agents_primary.wakeup_events (organization_id, member_id, recorded_at)
    WHERE handled_at IS NULL;

CREATE INDEX IF NOT EXISTS wakeup_events_handled
    ON agents_primary.wakeup_events (handled_at)
    WHERE handled_at IS NOT NULL;

-- The thread each member's wake-ups go to, in each organization.
CREATE TABLE IF NOT EXISTS agents_primary.wakeup_threads (
    organization_id text NOT NULL,
    member_id text NOT NULL,
    thread_id text NOT NULL,
    PRIMARY KEY (organization_id, member_id)
);
