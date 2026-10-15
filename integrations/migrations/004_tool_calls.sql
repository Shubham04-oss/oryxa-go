-- The audit of tool calls (design §12.3, step 5): who asked, which agent ran
-- the tool, and what was decided. The input and output are not kept.
CREATE TABLE IF NOT EXISTS integrations.tool_calls (
    id text PRIMARY KEY,
    org_id text NOT NULL,
    member_id text NOT NULL,
    agent_id text NOT NULL,
    agent_role text NOT NULL,
    connection_id text NOT NULL,
    tool text NOT NULL,
    effect text NOT NULL CHECK (effect IN ('read', 'write', 'external', 'delete')),
    -- Written before the connector runs, so a call that can't be recorded doesn't run.
    decision text NOT NULL CHECK (decision IN ('allowed', 'needs_confirmation')),
    status text NOT NULL CHECK (status IN ('started', 'ok', 'tool_error', 'failed', 'refused')),
    duration_ms integer NOT NULL DEFAULT 0,
    at timestamptz NOT NULL
);

CREATE INDEX IF NOT EXISTS tool_calls_org_idx ON integrations.tool_calls (org_id, at);
