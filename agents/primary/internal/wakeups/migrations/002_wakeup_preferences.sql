-- Whether a member wants Krude to tell them what their apps report, in each
-- organization. No row means yes: updates are on until a member turns them
-- off.
CREATE TABLE IF NOT EXISTS agents_primary.wakeup_preferences (
    organization_id text NOT NULL,
    member_id text NOT NULL,
    app_updates boolean NOT NULL,
    updated_at timestamptz NOT NULL,
    PRIMARY KEY (organization_id, member_id)
);
