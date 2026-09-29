CREATE TABLE IF NOT EXISTS workspace.external_mappings (
    organization_id text NOT NULL REFERENCES auth_organizations(id) ON DELETE CASCADE,
    source text NOT NULL,
    kind text NOT NULL CHECK (kind IN ('user', 'team')),
    external_id text NOT NULL,
    reference text NOT NULL,
    resource_id text,
    updated_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (organization_id, source, kind, external_id)
);

CREATE UNIQUE INDEX IF NOT EXISTS workspace_external_mappings_resource_unique
    ON workspace.external_mappings (organization_id, source, kind, resource_id)
    WHERE resource_id IS NOT NULL;
