ALTER TABLE files_objects ADD COLUMN IF NOT EXISTS scope text NOT NULL DEFAULT 'member';
ALTER TABLE files_objects ADD COLUMN IF NOT EXISTS team_id text;
ALTER TABLE files_objects ADD COLUMN IF NOT EXISTS member_id text;

UPDATE files_objects
SET member_id = created_by
WHERE scope = 'member' AND member_id IS NULL;

ALTER TABLE files_objects DROP CONSTRAINT IF EXISTS files_objects_purpose_check;
ALTER TABLE files_objects ADD CONSTRAINT files_objects_purpose_check CHECK (purpose IN (
    'upload',
    'integrations.piece',
    'integrations.webhook-payload',
    'integrations.attachment',
    'pipeline.artifact',
    'billing.attachment'
));

ALTER TABLE files_objects DROP CONSTRAINT IF EXISTS files_objects_scope_check;
ALTER TABLE files_objects ADD CONSTRAINT files_objects_scope_check CHECK (scope IN ('member', 'team', 'organization'));

ALTER TABLE files_objects DROP CONSTRAINT IF EXISTS files_objects_placement_check;
ALTER TABLE files_objects ADD CONSTRAINT files_objects_placement_check CHECK (
    (scope = 'member' AND member_id IS NOT NULL AND team_id IS NULL)
    OR (scope = 'team' AND team_id IS NOT NULL AND member_id IS NULL)
    OR (scope = 'organization' AND team_id IS NULL AND member_id IS NULL)
);

CREATE INDEX IF NOT EXISTS files_objects_org_scope_idx
    ON files_objects (organization_id, scope, created_at DESC, id);

CREATE INDEX IF NOT EXISTS files_objects_org_team_idx
    ON files_objects (organization_id, team_id, created_at DESC, id)
    WHERE team_id IS NOT NULL;

CREATE INDEX IF NOT EXISTS files_objects_org_member_idx
    ON files_objects (organization_id, member_id, created_at DESC, id)
    WHERE member_id IS NOT NULL;
