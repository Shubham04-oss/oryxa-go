-- A member leaving an organization, or the organization going, revokes the
-- connections they made there (design §7.2, "Consumed").

-- The bus events already acted on, since the bus delivers at least once. A
-- row is kept as long as a message can wait in the dead-letter queue, so a
-- redriven one is still known.
CREATE TABLE IF NOT EXISTS integrations.handled_events (
    event_id text PRIMARY KEY,
    detail_type text NOT NULL,
    handled_at timestamptz NOT NULL
);

CREATE INDEX IF NOT EXISTS handled_events_handled_idx
    ON integrations.handled_events (handled_at);

-- Whether the app confirmed revoking a removed connection's grant: NULL
-- while the connection lives, false when it was revoked here only (the app
-- has no revoke endpoint, refused, or could not be asked).
ALTER TABLE integrations.connections
    ADD COLUMN IF NOT EXISTS provider_revoked boolean;

-- A member's live connections in an organization, which their departure revokes.
CREATE INDEX IF NOT EXISTS connections_member_idx
    ON integrations.connections (org_id, member_id)
    WHERE status <> 'revoked';
