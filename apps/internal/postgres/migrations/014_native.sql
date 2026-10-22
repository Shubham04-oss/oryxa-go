-- The integrations service as a fourth broker. It holds the credential and
-- publishes a connection's life as events, and apps keeps a row per
-- connection so a member's tools and connections list find it. Nothing here
-- is written until apps is given the integrations service.

-- Dropped and made again under its own name, as 012 did, so running this
-- twice changes nothing.
ALTER TABLE apps_connections
    DROP CONSTRAINT IF EXISTS apps_connections_broker_check;
ALTER TABLE apps_connections
    ADD CONSTRAINT apps_connections_broker_check
        CHECK (broker IN ('composio', 'oryxa', 'pipedream', 'native'));

-- The service's id for the connection, and the UpdatedAt of the newest event
-- applied. Events arrive in any order and twice; an event older than
-- native_updated_at changes nothing, and a removed row is never changed.
ALTER TABLE apps_connections
    ADD COLUMN IF NOT EXISTS native_connection_id text,
    ADD COLUMN IF NOT EXISTS native_updated_at timestamptz;

CREATE UNIQUE INDEX IF NOT EXISTS apps_connections_native_idx
    ON apps_connections (native_connection_id);
