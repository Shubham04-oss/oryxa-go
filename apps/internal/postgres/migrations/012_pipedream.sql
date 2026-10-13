-- Pipedream Connect, a third broker beside Composio and Oryxa's own sign-in,
-- for the apps neither of the other two serves. Each app is served by one
-- broker, and a connection keeps the broker it was made through. Nothing
-- here is written until apps is given a Pipedream client.

-- 006 left the broker check unnamed, so Postgres named it
-- apps_connections_broker_check. It is dropped and made again under that
-- name, so running this twice changes nothing.
ALTER TABLE apps_connections
    DROP CONSTRAINT IF EXISTS apps_connections_broker_check;
ALTER TABLE apps_connections
    ADD CONSTRAINT apps_connections_broker_check
        CHECK (broker IN ('composio', 'oryxa', 'pipedream'));

-- Pipedream's account, known once the member finished connecting. One
-- connection per account, as Composio's accounts are, so a webhook naming an
-- account finds exactly one connection.
ALTER TABLE apps_connections
    ADD COLUMN IF NOT EXISTS pipedream_account_id text;

CREATE UNIQUE INDEX IF NOT EXISTS apps_connections_pipedream_account_idx
    ON apps_connections (pipedream_account_id)
    WHERE pipedream_account_id IS NOT NULL;

-- A Pipedream trigger is a deployed component, keyed there by its own id,
-- and it signs what it delivers with a key apps keeps sealed: an
-- internal/secrets envelope bound to service oryxa-apps, as every other
-- sealed column is. Both are NULL on a Composio or Oryxa trigger.
ALTER TABLE apps_triggers
    ADD COLUMN IF NOT EXISTS pipedream_trigger_id text;
ALTER TABLE apps_triggers
    ADD COLUMN IF NOT EXISTS pipedream_signing_key bytea;

CREATE UNIQUE INDEX IF NOT EXISTS apps_triggers_pipedream_idx
    ON apps_triggers (pipedream_trigger_id)
    WHERE status <> 'removed' AND pipedream_trigger_id IS NOT NULL;

-- Which broker ran each action, so usage can be counted per broker without
-- a join against connections that may since have been removed. Every action
-- so far ran through its connection's broker, which is what fills it.
ALTER TABLE apps_actions
    ADD COLUMN IF NOT EXISTS broker text NOT NULL DEFAULT 'composio'
        CHECK (broker IN ('composio', 'oryxa', 'pipedream'));

UPDATE apps_actions
SET broker = apps_connections.broker
FROM apps_connections
WHERE apps_connections.id = apps_actions.connection_id
  AND apps_actions.broker <> apps_connections.broker;
