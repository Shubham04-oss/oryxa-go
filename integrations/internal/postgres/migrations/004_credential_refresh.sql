ALTER TABLE integrations_connections
    ADD COLUMN IF NOT EXISTS refresh_lease_token text;

ALTER TABLE integrations_connections
    ADD COLUMN IF NOT EXISTS refresh_lease_until timestamptz;

CREATE INDEX IF NOT EXISTS integrations_connections_refresh_lease_idx
    ON integrations_connections (refresh_lease_until)
    WHERE refresh_lease_until IS NOT NULL;
