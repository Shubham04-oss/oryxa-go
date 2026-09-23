-- Files has an empty connection_id. The older live-target index would allow
-- only one {"member":"self"} source in an organization, regardless of owner.
-- Keep that index for connected apps and key Files sources by their actual
-- placement. The earlier index already prevented duplicates for any given
-- Files target, so this replacement accepts existing data.
DROP INDEX IF EXISTS knowledge_sources_live_target_unique;

CREATE UNIQUE INDEX knowledge_sources_live_target_unique
    ON knowledge_sources (organization_id, provider, connection_id, target)
    WHERE status <> 'removed' AND provider <> 'files';

CREATE UNIQUE INDEX knowledge_files_sources_live_placement_unique
    ON knowledge_sources (
        organization_id, scope, COALESCE(team_id, ''), COALESCE(member_id, ''), target)
    WHERE provider = 'files' AND status <> 'removed';
