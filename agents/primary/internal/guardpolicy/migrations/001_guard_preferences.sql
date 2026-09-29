-- What each member chose about how strictly the answer guard reads what their
-- agent says, in each organization, and every change they made to it.

CREATE SCHEMA IF NOT EXISTS agents_primary;

-- One row per member who has ever chosen. A null level is no choice: the
-- default for its origin applies, exactly as it does for a member with no row
-- at all. owner_level is for an answer the member started themselves; peer_level
-- is for an answer another member reads. What applies is decided in code, from
-- these and the organization's own minimum, which is never kept here.
CREATE TABLE IF NOT EXISTS agents_primary.guard_preferences (
    organization_id text NOT NULL,
    user_id text NOT NULL,
    owner_level text CHECK (owner_level IN ('secrets', 'strict')),
    peer_level text CHECK (peer_level IN ('secrets', 'strict')),
    updated_at timestamptz NOT NULL,
    PRIMARY KEY (organization_id, user_id)
);

-- Every change of a level, appended in the transaction that made it and never
-- edited. field is the origin that changed; "old" and "new" are what it was
-- and what it became, null for no choice.
CREATE TABLE IF NOT EXISTS agents_primary.guard_preference_changes (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    organization_id text NOT NULL,
    user_id text NOT NULL,
    field text NOT NULL CHECK (field IN ('owner', 'peer')),
    "old" text CHECK ("old" IN ('secrets', 'strict')),
    "new" text CHECK ("new" IN ('secrets', 'strict')),
    changed_at timestamptz NOT NULL DEFAULT now()
);

-- Forgetting a member, or an organization, finds its changes by these.
CREATE INDEX IF NOT EXISTS guard_preference_changes_member
    ON agents_primary.guard_preference_changes (organization_id, user_id, changed_at);
