-- Why something was allowed (docs/design/permissions, §6 step 3): each allow
-- that reaches what isn't the caller's own, and each an agent makes, with the
-- rule that granted it. Members read their part on the Activity page.
CREATE SCHEMA IF NOT EXISTS access;

CREATE TABLE IF NOT EXISTS access.decisions (
    id bigserial PRIMARY KEY,
    at timestamptz NOT NULL,
    -- The hour of at: one row per actor, resource, action and rule an hour.
    bucket timestamptz NOT NULL,
    organization_id text NOT NULL,
    service text NOT NULL,
    action text NOT NULL,
    resource_kind text NOT NULL,
    resource_id text NOT NULL,
    -- The member whose resource it is; empty for a team's or the organization's.
    owner_id text NOT NULL DEFAULT '',
    actor_user_id text NOT NULL,
    -- Empty for a member's own session.
    actor_agent_id text NOT NULL DEFAULT '',
    actor_agent_role text NOT NULL DEFAULT '',
    reason text NOT NULL
);

CREATE UNIQUE INDEX IF NOT EXISTS decisions_hourly_idx ON access.decisions
    (organization_id, actor_user_id, actor_agent_id, resource_kind, resource_id, action, reason, bucket);
CREATE INDEX IF NOT EXISTS decisions_owner_idx ON access.decisions (organization_id, owner_id, at DESC);
CREATE INDEX IF NOT EXISTS decisions_actor_idx ON access.decisions (organization_id, actor_user_id, at DESC);
CREATE INDEX IF NOT EXISTS decisions_at_idx ON access.decisions (at);
