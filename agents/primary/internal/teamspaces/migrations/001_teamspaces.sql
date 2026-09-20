CREATE SCHEMA IF NOT EXISTS agents_primary;

CREATE TABLE IF NOT EXISTS agents_primary.organization_agents (
    organization_id text PRIMARY KEY,
    id text NOT NULL UNIQUE,
    name text NOT NULL,
    description text NOT NULL DEFAULT '',
    instructions text NOT NULL,
    status text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'disabled')),
    revision bigint NOT NULL DEFAULT 1 CHECK (revision > 0),
    created_by_user_id text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS agents_primary.teamspaces (
    id text PRIMARY KEY,
    organization_id text NOT NULL,
    team_id text NOT NULL,
    name text NOT NULL,
    description text NOT NULL DEFAULT '',
    created_by_user_id text NOT NULL,
    organization_agent_id text NOT NULL,
    status text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'disabled')),
    revision bigint NOT NULL DEFAULT 1 CHECK (revision > 0),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (organization_id, team_id)
);

-- The first development draft put one coordinator on every space. Reapplying
-- this migration upgrades those local databases to one organization agent.
ALTER TABLE agents_primary.teamspaces
    ADD COLUMN IF NOT EXISTS organization_agent_id text;

DO $$
BEGIN
    IF EXISTS (
        SELECT 1 FROM information_schema.columns
        WHERE table_schema='agents_primary' AND table_name='teamspaces' AND column_name='team_agent_id'
    ) THEN
        INSERT INTO agents_primary.organization_agents
            (organization_id, id, name, description, instructions, status, revision, created_by_user_id, created_at, updated_at)
        SELECT DISTINCT ON (organization_id)
            organization_id, team_agent_id, team_agent_name, team_agent_description,
            team_agent_instructions, status, revision, created_by_user_id, created_at, updated_at
        FROM agents_primary.teamspaces
        WHERE team_agent_id IS NOT NULL
        ORDER BY organization_id, created_at, id
        ON CONFLICT (organization_id) DO NOTHING;

        UPDATE agents_primary.teamspaces space
        SET organization_agent_id = agent.id
        FROM agents_primary.organization_agents agent
        WHERE agent.organization_id=space.organization_id AND space.organization_agent_id IS NULL;

        ALTER TABLE agents_primary.teamspaces ALTER COLUMN team_agent_id DROP NOT NULL;
        ALTER TABLE agents_primary.teamspaces ALTER COLUMN team_agent_name DROP NOT NULL;
        ALTER TABLE agents_primary.teamspaces ALTER COLUMN team_agent_instructions DROP NOT NULL;
    END IF;
END $$;

ALTER TABLE agents_primary.teamspaces
    ALTER COLUMN organization_agent_id SET NOT NULL;

DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint
        WHERE conname='teamspaces_organization_agent_fk'
          AND conrelid='agents_primary.teamspaces'::regclass
    ) THEN
        ALTER TABLE agents_primary.teamspaces
            ADD CONSTRAINT teamspaces_organization_agent_fk
            FOREIGN KEY (organization_agent_id) REFERENCES agents_primary.organization_agents(id) ON DELETE RESTRICT;
    END IF;
END $$;

CREATE UNIQUE INDEX IF NOT EXISTS teamspaces_org_name_idx
    ON agents_primary.teamspaces (organization_id, lower(name));

CREATE INDEX IF NOT EXISTS teamspaces_org_idx
    ON agents_primary.teamspaces (organization_id, created_at, id);

CREATE TABLE IF NOT EXISTS agents_primary.teamspace_agent_contributions (
    teamspace_id text NOT NULL REFERENCES agents_primary.teamspaces(id) ON DELETE CASCADE,
    agent_id text NOT NULL,
    owner_user_id text NOT NULL,
    role text NOT NULL,
    delegation_mode text NOT NULL CHECK (delegation_mode IN ('request', 'automatic')),
    position integer NOT NULL CHECK (position >= 0),
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (teamspace_id, agent_id),
    UNIQUE (teamspace_id, position),
    CONSTRAINT teamspace_agent_contributions_agent_fk
        FOREIGN KEY (agent_id) REFERENCES agents_primary.member_agents(id) ON DELETE RESTRICT
);

CREATE INDEX IF NOT EXISTS teamspace_agent_contributions_owner_idx
    ON agents_primary.teamspace_agent_contributions (owner_user_id, teamspace_id);

CREATE TABLE IF NOT EXISTS agents_primary.teamspace_entries (
    id text PRIMARY KEY,
    teamspace_id text NOT NULL REFERENCES agents_primary.teamspaces(id) ON DELETE CASCADE,
    organization_id text NOT NULL,
    author_user_id text NOT NULL,
    author_agent_id text NOT NULL,
    author_agent_name text NOT NULL,
    kind text NOT NULL CHECK (kind IN ('update', 'result', 'artifact', 'handoff', 'decision')),
    title text NOT NULL,
    summary text NOT NULL,
    tools_used jsonb NOT NULL DEFAULT '[]'::jsonb CHECK (jsonb_typeof(tools_used) = 'array'),
    artifacts jsonb NOT NULL DEFAULT '[]'::jsonb CHECK (jsonb_typeof(artifacts) = 'array'),
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS teamspace_entries_space_idx
    ON agents_primary.teamspace_entries (organization_id, teamspace_id, created_at DESC, id DESC);
