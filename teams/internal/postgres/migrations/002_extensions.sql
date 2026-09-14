CREATE EXTENSION IF NOT EXISTS vector;
CREATE EXTENSION IF NOT EXISTS ltree;
CREATE EXTENSION IF NOT EXISTS pg_trgm;
CREATE EXTENSION IF NOT EXISTS btree_gist;
CREATE EXTENSION IF NOT EXISTS btree_gin;

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

-- organization_id equality plus ltree <@ / descendants.
CREATE INDEX IF NOT EXISTS teams_org_path_gist_idx
    ON teams USING GIST (organization_id, path);

-- Tenant-scoped name search (`%`, ILIKE, similarity).
CREATE INDEX IF NOT EXISTS teams_org_name_trgm_idx
    ON teams USING GIN (organization_id, name gin_trgm_ops);
