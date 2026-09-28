CREATE TABLE IF NOT EXISTS authz_policies (
    id text PRIMARY KEY,
    organization_id text NOT NULL,
    scope_type text NOT NULL,
    scope_id text NOT NULL DEFAULT '',
    name text NOT NULL,
    text text NOT NULL,
    created_by text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (organization_id, scope_type, scope_id, name)
);

CREATE INDEX IF NOT EXISTS authz_policies_organization_idx
    ON authz_policies (organization_id);
