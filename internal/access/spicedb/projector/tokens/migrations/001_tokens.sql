-- The latest ZedToken the projector got back from SpiceDB for each
-- organization (docs/design/permissions, Risks: new-enemy). A check that
-- follows a change passes it as at_least_as_fresh.
CREATE SCHEMA IF NOT EXISTS access;

CREATE TABLE IF NOT EXISTS access.spicedb_tokens (
    organization_id text PRIMARY KEY,
    token text NOT NULL,
    updated_at timestamptz NOT NULL
);
