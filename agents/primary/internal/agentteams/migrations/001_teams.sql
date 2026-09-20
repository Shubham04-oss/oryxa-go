CREATE SCHEMA IF NOT EXISTS agents_primary;

CREATE TABLE IF NOT EXISTS agents_primary.member_agent_teams (
    id text PRIMARY KEY,
    organization_id text NOT NULL,
    owner_user_id text NOT NULL,
    name text NOT NULL,
    description text NOT NULL DEFAULT '',
    coordinator_agent_id text NOT NULL REFERENCES agents_primary.member_agents(id) ON DELETE RESTRICT,
    status text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'disabled')),
    revision bigint NOT NULL DEFAULT 1 CHECK (revision > 0),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE UNIQUE INDEX IF NOT EXISTS member_agent_teams_owner_name_idx
    ON agents_primary.member_agent_teams (organization_id, owner_user_id, lower(name));

CREATE INDEX IF NOT EXISTS member_agent_teams_owner_idx
    ON agents_primary.member_agent_teams (organization_id, owner_user_id, created_at, id);

CREATE TABLE IF NOT EXISTS agents_primary.member_agent_team_members (
    team_id text NOT NULL REFERENCES agents_primary.member_agent_teams(id) ON DELETE CASCADE,
    agent_id text NOT NULL,
    role text NOT NULL,
    position integer NOT NULL CHECK (position >= 0),
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (team_id, agent_id),
    UNIQUE (team_id, position),
    CONSTRAINT member_agent_team_members_agent_fk
        FOREIGN KEY (agent_id) REFERENCES agents_primary.member_agents(id) ON DELETE RESTRICT
);
