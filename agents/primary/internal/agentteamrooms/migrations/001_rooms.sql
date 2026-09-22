CREATE SCHEMA IF NOT EXISTS agents_primary;

CREATE TABLE IF NOT EXISTS agents_primary.member_agent_team_messages (
    id text PRIMARY KEY,
    team_id text NOT NULL REFERENCES agents_primary.member_agent_teams(id) ON DELETE CASCADE,
    seq bigint NOT NULL CHECK (seq > 0),
    kind text NOT NULL CHECK (kind IN ('member', 'agent')),
    author_name text NOT NULL,
    author_agent_id text NOT NULL DEFAULT '',
    body text NOT NULL,
    in_reply_to_seq bigint NOT NULL DEFAULT 0,
    created_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (team_id, seq)
);

CREATE INDEX IF NOT EXISTS member_agent_team_messages_recent_idx
    ON agents_primary.member_agent_team_messages (team_id, seq DESC);

CREATE TABLE IF NOT EXISTS agents_primary.member_agent_team_threads (
    team_id text NOT NULL REFERENCES agents_primary.member_agent_teams(id) ON DELETE CASCADE,
    agent_id text NOT NULL,
    thread_id text NOT NULL,
    updated_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (team_id, agent_id)
);
