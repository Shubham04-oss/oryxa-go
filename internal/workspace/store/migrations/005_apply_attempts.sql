CREATE TABLE workspace.apply_attempts (
    id text PRIMARY KEY,
    organization_id text NOT NULL,
    actor_id text NOT NULL,
    status text NOT NULL,
    completed jsonb NOT NULL DEFAULT '[]',
    started_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL
);
CREATE INDEX workspace_apply_attempts_organization ON workspace.apply_attempts (organization_id, updated_at DESC);
