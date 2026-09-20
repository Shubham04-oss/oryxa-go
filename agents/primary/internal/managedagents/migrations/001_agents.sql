CREATE SCHEMA IF NOT EXISTS agents_primary;

CREATE TABLE IF NOT EXISTS agents_primary.member_agents (
    id text PRIMARY KEY,
    organization_id text NOT NULL,
    owner_user_id text NOT NULL,
    kind text NOT NULL CHECK (kind IN ('primary', 'specialist')),
    name text NOT NULL,
    description text NOT NULL DEFAULT '',
    instructions text NOT NULL DEFAULT '',
    status text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'disabled')),
    revision bigint NOT NULL DEFAULT 1 CHECK (revision > 0),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE UNIQUE INDEX IF NOT EXISTS member_agents_one_primary_idx
    ON agents_primary.member_agents (organization_id, owner_user_id)
    WHERE kind = 'primary';

CREATE UNIQUE INDEX IF NOT EXISTS member_agents_org_name_idx
    ON agents_primary.member_agents (organization_id, lower(name));

CREATE INDEX IF NOT EXISTS member_agents_owner_idx
    ON agents_primary.member_agents (organization_id, owner_user_id, created_at, id);
