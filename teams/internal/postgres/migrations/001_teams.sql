CREATE TABLE IF NOT EXISTS teams_organizations (
    organization_id text PRIMARY KEY,
    name text NOT NULL DEFAULT '',
    name_updated_at timestamptz,
    deleted_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS teams (
    id text PRIMARY KEY,
    organization_id text NOT NULL REFERENCES teams_organizations(organization_id),
    name text NOT NULL CHECK (length(name) BETWEEN 1 AND 120),
    created_by text NOT NULL,
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL
);

CREATE UNIQUE INDEX IF NOT EXISTS teams_organization_name_unique
    ON teams (organization_id, lower(name));

CREATE INDEX IF NOT EXISTS teams_organization_idx
    ON teams (organization_id);

CREATE TABLE IF NOT EXISTS teams_memberships (
    team_id text NOT NULL REFERENCES teams(id) ON DELETE CASCADE,
    organization_id text NOT NULL REFERENCES teams_organizations(organization_id),
    user_id text NOT NULL,
    email text NOT NULL,
    display_name text NOT NULL DEFAULT '',
    role text NOT NULL CHECK (role IN ('admin', 'member')),
    joined_at timestamptz NOT NULL,
    PRIMARY KEY (team_id, user_id)
);

CREATE INDEX IF NOT EXISTS teams_memberships_user_idx
    ON teams_memberships (organization_id, user_id);

CREATE TABLE IF NOT EXISTS teams_outbox (
    id text PRIMARY KEY,
    detail_type text NOT NULL,
    detail jsonb NOT NULL,
    occurred_at timestamptz NOT NULL,
    attempts integer NOT NULL DEFAULT 0,
    available_at timestamptz NOT NULL DEFAULT now(),
    locked_until timestamptz,
    published_at timestamptz,
    last_error text
);

CREATE INDEX IF NOT EXISTS teams_outbox_pending_idx
    ON teams_outbox (available_at, occurred_at)
    WHERE published_at IS NULL;
