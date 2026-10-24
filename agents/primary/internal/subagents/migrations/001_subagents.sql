-- The sub-agents single-agent Krude starts (internal/subagents). A row is
-- what Krude sees of one: its brief's objective, its state, the question it
-- asked, its progress and its condensed result, never its transcript, which
-- is an ADK session of its own (app krude_sub). Rows are scoped to the
-- thread, member and organization that started them.

CREATE SCHEMA IF NOT EXISTS agents_primary;

CREATE TABLE IF NOT EXISTS agents_primary.subagents (
    id text PRIMARY KEY,
    organization_id text NOT NULL,
    user_id text NOT NULL,
    -- thread_app and thread_id are the ADK session of the thread Krude
    -- started it in; parent_run_id the run that last set it working.
    thread_app text NOT NULL,
    thread_id text NOT NULL,
    parent_run_id text NOT NULL,
    objective text NOT NULL,
    output text NOT NULL DEFAULT '',
    tools text[] NOT NULL DEFAULT '{}',
    files text[] NOT NULL DEFAULT '{}',
    state text NOT NULL CHECK (state IN ('running', 'waiting_on_parent', 'done', 'failed', 'stopped')),
    question text NOT NULL DEFAULT '',
    progress text NOT NULL DEFAULT '',
    result text NOT NULL DEFAULT '',
    -- full_result holds a result too long for result until a run of
    -- Krude's saves it as an artifact of the thread, named in
    -- result_artifact; it is emptied then.
    full_result text NOT NULL DEFAULT '',
    result_artifact text NOT NULL DEFAULT '',
    error text NOT NULL DEFAULT '',
    budget_model_calls integer NOT NULL CHECK (budget_model_calls > 0),
    budget_minutes integer NOT NULL CHECK (budget_minutes > 0),
    model_calls integer NOT NULL DEFAULT 0,
    input_tokens bigint NOT NULL DEFAULT 0,
    output_tokens bigint NOT NULL DEFAULT 0,
    cached_tokens bigint NOT NULL DEFAULT 0,
    total_tokens bigint NOT NULL DEFAULT 0,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    -- expires_at is the last activity plus the TTL: past it, the sub-agent
    -- is gone for Krude's purposes and agent_status says so.
    expires_at timestamptz NOT NULL
);

CREATE INDEX IF NOT EXISTS subagents_thread_idx
    ON agents_primary.subagents (thread_id, created_at);

CREATE INDEX IF NOT EXISTS subagents_member_idx
    ON agents_primary.subagents (organization_id, user_id);
