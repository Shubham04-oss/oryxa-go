CREATE SCHEMA IF NOT EXISTS workspace;

CREATE TABLE IF NOT EXISTS workspace.configs (
    organization_id text PRIMARY KEY REFERENCES auth_organizations(id) ON DELETE CASCADE,
    version bigint NOT NULL CHECK (version > 0),
    document bytea NOT NULL,
    applied_by text NOT NULL,
    applied_at timestamptz NOT NULL
);

CREATE TABLE IF NOT EXISTS workspace.versions (
    organization_id text NOT NULL REFERENCES auth_organizations(id) ON DELETE CASCADE,
    version bigint NOT NULL CHECK (version > 0),
    document bytea NOT NULL,
    applied_by text NOT NULL,
    applied_at timestamptz NOT NULL,
    PRIMARY KEY (organization_id, version)
);
