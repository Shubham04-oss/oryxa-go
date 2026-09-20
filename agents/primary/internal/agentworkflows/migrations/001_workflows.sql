CREATE SCHEMA IF NOT EXISTS agents_primary;

CREATE TABLE IF NOT EXISTS agents_primary.member_agent_workflows (
    id text PRIMARY KEY,
    organization_id text NOT NULL,
    owner_user_id text NOT NULL,
    name text NOT NULL,
    description text NOT NULL DEFAULT '',
    team_id text NOT NULL REFERENCES agents_primary.member_agent_teams(id) ON DELETE RESTRICT,
    status text NOT NULL CHECK (status IN ('active', 'disabled')),
    assignments jsonb NOT NULL,
    schedule_expression text,
    schedule_timezone text,
    schedule_state text CHECK (schedule_state IS NULL OR schedule_state IN ('enabled', 'disabled')),
    revision bigint NOT NULL DEFAULT 1 CHECK (revision > 0),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CHECK (jsonb_typeof(assignments) = 'array'),
    CHECK ((schedule_expression IS NULL) = (schedule_timezone IS NULL)),
    CHECK ((schedule_expression IS NULL) = (schedule_state IS NULL))
);

CREATE UNIQUE INDEX IF NOT EXISTS member_agent_workflows_owner_name_idx
    ON agents_primary.member_agent_workflows (organization_id, owner_user_id, lower(name));

CREATE INDEX IF NOT EXISTS member_agent_workflows_owner_idx
    ON agents_primary.member_agent_workflows (organization_id, owner_user_id, created_at, id);

CREATE TABLE IF NOT EXISTS agents_primary.member_agent_workflow_executions (
    id text PRIMARY KEY,
    workflow_id text NOT NULL REFERENCES agents_primary.member_agent_workflows(id) ON DELETE CASCADE,
    organization_id text NOT NULL,
    owner_user_id text NOT NULL,
    started_by text NOT NULL CHECK (started_by IN ('api', 'schedule')),
    status text NOT NULL CHECK (status IN ('queued', 'running', 'succeeded', 'needs_confirmation', 'failed')),
    external_id text NOT NULL DEFAULT '',
    result jsonb,
    error text NOT NULL DEFAULT '',
    created_at timestamptz NOT NULL DEFAULT now(),
    started_at timestamptz,
    completed_at timestamptz,
    lease_until timestamptz,
    attempts integer NOT NULL DEFAULT 0 CHECK (attempts >= 0)
);

CREATE INDEX IF NOT EXISTS member_agent_workflow_executions_owner_idx
    ON agents_primary.member_agent_workflow_executions
    (organization_id, owner_user_id, workflow_id, created_at DESC, id DESC);

CREATE UNIQUE INDEX IF NOT EXISTS member_agent_workflow_executions_external_idx
    ON agents_primary.member_agent_workflow_executions (external_id)
    WHERE external_id <> '';
