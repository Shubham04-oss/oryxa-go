-- Interaction lifecycle events are routed through EventBridge and SQS, then
-- kept here until the addressed persistent agent has handled them. The event
-- carries identities and state only; request and response content stays in
-- agent_interactions.

CREATE SCHEMA IF NOT EXISTS agents_primary;

CREATE TABLE IF NOT EXISTS agents_primary.interaction_deliveries (
    event_id text PRIMARY KEY,
    request_id text NOT NULL,
    organization_id text NOT NULL,
    recipient_user_id text NOT NULL,
    recipient_agent_id text NOT NULL,
    kind text NOT NULL CHECK (kind IN (
        'target_requested',
        'target_approved',
        'requester_rejected',
        'requester_completed'
    )),
    interaction_status text NOT NULL,
    revision bigint NOT NULL CHECK (revision > 0),
    changed_at timestamptz NOT NULL,
    recorded_at timestamptz NOT NULL DEFAULT now(),
    available_at timestamptz NOT NULL DEFAULT now(),
    claimed_until timestamptz,
    attempts integer NOT NULL DEFAULT 0,
    handled_at timestamptz,
    dropped boolean NOT NULL DEFAULT false,
    last_error text NOT NULL DEFAULT ''
);

CREATE INDEX IF NOT EXISTS interaction_deliveries_waiting_idx
    ON agents_primary.interaction_deliveries (available_at, recorded_at, event_id)
    WHERE handled_at IS NULL;

CREATE INDEX IF NOT EXISTS interaction_deliveries_request_idx
    ON agents_primary.interaction_deliveries (request_id, recipient_agent_id, revision, recorded_at)
    WHERE handled_at IS NULL;

-- Each persistent agent gets one stable conversation for one interaction.
-- Request, approval and outcome notifications therefore share context while
-- unrelated interactions remain isolated.
CREATE TABLE IF NOT EXISTS agents_primary.interaction_delivery_threads (
    request_id text NOT NULL,
    organization_id text NOT NULL,
    user_id text NOT NULL,
    agent_id text NOT NULL,
    thread_id text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (request_id, agent_id)
);

CREATE INDEX IF NOT EXISTS interaction_delivery_threads_owner_idx
    ON agents_primary.interaction_delivery_threads (organization_id, user_id);
