-- Teamspace entry events carry routing identity only. The worker claims them
-- here, loads authoritative shared content, and atomically advances the one
-- organization-agent synthesis.

CREATE SCHEMA IF NOT EXISTS agents_primary;

CREATE TABLE IF NOT EXISTS agents_primary.organization_agent_deliveries (
    event_id text PRIMARY KEY,
    entry_id text NOT NULL,
    teamspace_id text NOT NULL,
    organization_id text NOT NULL,
    author_user_id text NOT NULL,
    author_agent_id text NOT NULL,
    entry_kind text NOT NULL,
    published_at timestamptz NOT NULL,
    recorded_at timestamptz NOT NULL DEFAULT now(),
    available_at timestamptz NOT NULL DEFAULT now(),
    claimed_until timestamptz,
    attempts integer NOT NULL DEFAULT 0,
    handled_at timestamptz,
    dropped boolean NOT NULL DEFAULT false,
    last_error text NOT NULL DEFAULT ''
);

CREATE INDEX IF NOT EXISTS organization_agent_deliveries_waiting_idx
    ON agents_primary.organization_agent_deliveries
    (available_at, published_at, recorded_at, event_id)
    WHERE handled_at IS NULL;

CREATE INDEX IF NOT EXISTS organization_agent_deliveries_org_idx
    ON agents_primary.organization_agent_deliveries
    (organization_id, published_at, recorded_at, event_id)
    WHERE handled_at IS NULL;

CREATE TABLE IF NOT EXISTS agents_primary.organization_agent_syntheses (
    organization_id text PRIMARY KEY,
    agent_id text NOT NULL REFERENCES agents_primary.organization_agents(id) ON DELETE CASCADE,
    revision bigint NOT NULL CHECK (revision > 0),
    summary text NOT NULL,
    decisions jsonb NOT NULL DEFAULT '[]'::jsonb CHECK (jsonb_typeof(decisions) = 'array'),
    risks jsonb NOT NULL DEFAULT '[]'::jsonb CHECK (jsonb_typeof(risks) = 'array'),
    open_questions jsonb NOT NULL DEFAULT '[]'::jsonb CHECK (jsonb_typeof(open_questions) = 'array'),
    source_entry_id text NOT NULL,
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS agents_primary.organization_agent_followups (
    id text PRIMARY KEY,
    organization_id text NOT NULL,
    synthesis_revision bigint NOT NULL CHECK (synthesis_revision > 0),
    source_entry_id text NOT NULL,
    teamspace_id text NOT NULL,
    target_agent_id text NOT NULL DEFAULT '',
    task text NOT NULL,
    rationale text NOT NULL,
    status text NOT NULL DEFAULT 'proposed' CHECK (status IN ('proposed')),
    created_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (organization_id, synthesis_revision, id)
);

CREATE INDEX IF NOT EXISTS organization_agent_followups_org_idx
    ON agents_primary.organization_agent_followups
    (organization_id, created_at DESC, id DESC);
