CREATE TABLE workspace.reviewed_plans (
    id text PRIMARY KEY,
    organization_id text NOT NULL,
    actor_id text NOT NULL,
    document_hash text NOT NULL,
    base_version bigint NOT NULL,
    plan bytea NOT NULL,
    expires_at timestamptz NOT NULL
);
CREATE INDEX workspace_reviewed_plans_expiry ON workspace.reviewed_plans (expires_at);
