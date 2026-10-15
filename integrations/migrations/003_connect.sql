-- Connecting (design §9): the level a sign-in in flight is for, and one live
-- connection per member (or org), app and level, so a second sign-in
-- renews the first rather than making a twin.
ALTER TABLE integrations.oauth_states
    ADD COLUMN IF NOT EXISTS level text NOT NULL DEFAULT 'personal' CHECK (level IN ('personal', 'org'));

CREATE UNIQUE INDEX IF NOT EXISTS connections_live_idx
    ON integrations.connections (org_id, coalesce(member_id, ''), app, level)
    WHERE status <> 'revoked';
