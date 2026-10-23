-- Where SpiceDB, asked in shadow, answered differently from the code
-- (docs/design/permissions, §6 step 4). The code decided; this is the
-- evidence each service's flip (step 5) waits on.
CREATE TABLE IF NOT EXISTS access.disagreements (
    id bigserial PRIMARY KEY,
    at timestamptz NOT NULL,
    -- The hour of at: one row per actor, resource, action and answer an hour.
    bucket timestamptz NOT NULL,
    organization_id text NOT NULL,
    service text NOT NULL,
    action text NOT NULL,
    resource_kind text NOT NULL,
    resource_id text NOT NULL,
    actor_user_id text NOT NULL,
    actor_agent_id text NOT NULL DEFAULT '',
    actor_agent_role text NOT NULL DEFAULT '',
    code_allowed boolean NOT NULL,
    spicedb_allowed boolean NOT NULL,
    -- What SpiceDB was asked, as resource#permission@subject.
    checks text[] NOT NULL
);

CREATE UNIQUE INDEX IF NOT EXISTS disagreements_hourly_idx ON access.disagreements
    (organization_id, service, actor_user_id, actor_agent_id, resource_kind, resource_id, action, code_allowed, bucket);
CREATE INDEX IF NOT EXISTS disagreements_service_idx ON access.disagreements (service, at DESC);
CREATE INDEX IF NOT EXISTS disagreements_at_idx ON access.disagreements (at);
