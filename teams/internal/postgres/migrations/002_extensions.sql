CREATE EXTENSION IF NOT EXISTS vector;
CREATE EXTENSION IF NOT EXISTS ltree;
CREATE EXTENSION IF NOT EXISTS pg_trgm;

ALTER TABLE teams
    ADD COLUMN IF NOT EXISTS path ltree,
    ADD COLUMN IF NOT EXISTS embedding vector(1536);

UPDATE teams
SET path = (
        regexp_replace(organization_id, '[^A-Za-z0-9_]', '_', 'g')
        || '.'
        || regexp_replace(id, '[^A-Za-z0-9_]', '_', 'g')
    )::ltree
WHERE path IS NULL;

ALTER TABLE teams
    ALTER COLUMN path SET NOT NULL;

CREATE UNIQUE INDEX IF NOT EXISTS teams_path_unique
    ON teams (path);

CREATE INDEX IF NOT EXISTS teams_path_gist_idx
    ON teams USING GIST (path);

CREATE INDEX IF NOT EXISTS teams_name_trgm_idx
    ON teams USING GIN (name gin_trgm_ops);
