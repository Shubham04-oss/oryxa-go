ALTER TABLE workspace.configs DROP CONSTRAINT IF EXISTS configs_organization_id_fkey;
ALTER TABLE workspace.versions DROP CONSTRAINT IF EXISTS versions_organization_id_fkey;
ALTER TABLE workspace.external_mappings DROP CONSTRAINT IF EXISTS external_mappings_organization_id_fkey;
