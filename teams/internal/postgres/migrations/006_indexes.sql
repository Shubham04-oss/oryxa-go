-- Indexes that match the queries. Earlier files used to create single-column
-- gist/gin names; drop those so a re-run does not put them back (002/004
-- now create the composite ones). btree_gist / btree_gin let organization_id
-- sit in the same index as path / name.

CREATE EXTENSION IF NOT EXISTS btree_gist;
CREATE EXTENSION IF NOT EXISTS btree_gin;

CREATE INDEX IF NOT EXISTS teams_org_path_gist_idx
    ON teams USING GIST (organization_id, path);

CREATE INDEX IF NOT EXISTS teams_org_name_trgm_idx
    ON teams USING GIN (organization_id, name gin_trgm_ops);

CREATE INDEX IF NOT EXISTS teams_users_org_path_gist_idx
    ON teams_users USING GIST (organization_id, path);

CREATE INDEX IF NOT EXISTS teams_memberships_org_path_gist_idx
    ON teams_memberships USING GIST (organization_id, path);

CREATE INDEX IF NOT EXISTS teams_memberships_admins_idx
    ON teams_memberships (team_id)
    WHERE role = 'admin';

DROP INDEX IF EXISTS teams_path_gist_idx;
DROP INDEX IF EXISTS teams_name_trgm_idx;
DROP INDEX IF EXISTS teams_users_path_gist_idx;
DROP INDEX IF EXISTS teams_memberships_path_gist_idx;

DO $$
BEGIN
    IF EXISTS (
        SELECT 1
        FROM pg_indexes
        WHERE schemaname = current_schema()
          AND indexname = 'teams_outbox_pending_idx'
          AND indexdef NOT LIKE '%(available_at, occurred_at, id)%'
    ) THEN
        DROP INDEX teams_outbox_pending_idx;
    END IF;
END $$;

CREATE INDEX IF NOT EXISTS teams_outbox_pending_idx
    ON teams_outbox (available_at, occurred_at, id)
    WHERE published_at IS NULL;
