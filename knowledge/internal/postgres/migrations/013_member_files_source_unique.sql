-- Files has an empty connection_id. The older live-target index would allow
-- only one {"member":"self"} source in an organization, regardless of owner.
-- Keep that index for connected apps and key Files sources by their actual
-- placement. The earlier index already prevented duplicates for any given
-- Files target, so this replacement accepts existing data.
--
-- The connected-app index keeps its name with a narrower predicate, so it is
-- dropped and built again, which applying this file again does too; the
-- Files index is only built where it is missing.
DROP INDEX IF EXISTS knowledge_sources_live_target_unique;

CREATE UNIQUE INDEX knowledge_sources_live_target_unique
    ON knowledge_sources (organization_id, provider, connection_id, target)
    WHERE status <> 'removed' AND provider <> 'files';

CREATE UNIQUE INDEX IF NOT EXISTS knowledge_files_sources_live_placement_unique
    ON knowledge_sources (
        organization_id, scope, COALESCE(team_id, ''), COALESCE(member_id, ''), target)
    WHERE provider = 'files' AND status <> 'removed';
