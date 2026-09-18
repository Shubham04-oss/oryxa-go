-- Batching remembers what it has been told to forget. The bus orders
-- nothing, so a trigger apps received before a member was removed can arrive
-- after the removal, and recording it would start a new group for someone
-- who is gone. Each row is one removal, and an empty field covers
-- everything: (organization, '', '', '') is the whole organization,
-- (organization, member, '', '') one member in it, and a connection or a
-- trigger names just that. A trigger within a removal that apps received at
-- or before forgotten_at is refused.

CREATE TABLE IF NOT EXISTS batching_forgotten (
    organization_id text NOT NULL,
    member_id text NOT NULL DEFAULT '',
    connection_id text NOT NULL DEFAULT '',
    trigger_id text NOT NULL DEFAULT '',
    forgotten_at timestamptz NOT NULL,
    PRIMARY KEY (organization_id, member_id, connection_id, trigger_id)
);

-- Forgetting a member or an organization finds its triggers through this
-- rather than by reading the whole table.
CREATE INDEX IF NOT EXISTS batching_triggers_member_idx
    ON batching_triggers (organization_id, member_id);
