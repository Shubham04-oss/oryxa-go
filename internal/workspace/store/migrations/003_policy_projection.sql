ALTER TABLE workspace.configs ADD COLUMN IF NOT EXISTS policy_settings jsonb;
ALTER TABLE workspace.versions ADD COLUMN IF NOT EXISTS policy_settings jsonb;
