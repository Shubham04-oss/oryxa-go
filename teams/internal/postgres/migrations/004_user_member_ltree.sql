CREATE TABLE IF NOT EXISTS teams_users (
    organization_id text NOT NULL REFERENCES teams_organizations(organization_id) ON DELETE CASCADE,
    user_id text NOT NULL,
    email text NOT NULL,
    display_name text NOT NULL DEFAULT '',
    path ltree NOT NULL,
    updated_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (organization_id, user_id)
);

CREATE UNIQUE INDEX IF NOT EXISTS teams_users_path_unique
    ON teams_users (path);

CREATE INDEX IF NOT EXISTS teams_users_org_path_gist_idx
    ON teams_users USING GIST (organization_id, path);

ALTER TABLE teams_memberships
    ADD COLUMN IF NOT EXISTS path ltree;

UPDATE teams_memberships m
SET path = (t.path::text || '.' || regexp_replace(m.user_id, '[^A-Za-z0-9_]', '_', 'g'))::ltree
FROM teams t
WHERE t.id = m.team_id AND m.path IS NULL;

ALTER TABLE teams_memberships
    ALTER COLUMN path SET NOT NULL;

CREATE UNIQUE INDEX IF NOT EXISTS teams_memberships_path_unique
    ON teams_memberships (path);

CREATE INDEX IF NOT EXISTS teams_memberships_org_path_gist_idx
    ON teams_memberships USING GIST (organization_id, path);

INSERT INTO teams_users (organization_id, user_id, email, display_name, path, updated_at)
SELECT DISTINCT ON (organization_id, user_id)
    organization_id,
    user_id,
    email,
    display_name,
    (
        regexp_replace(organization_id, '[^A-Za-z0-9_]', '_', 'g')
        || '.'
        || regexp_replace(user_id, '[^A-Za-z0-9_]', '_', 'g')
    )::ltree,
    now()
FROM teams_memberships
ORDER BY organization_id, user_id, joined_at DESC
ON CONFLICT (organization_id, user_id) DO NOTHING;
