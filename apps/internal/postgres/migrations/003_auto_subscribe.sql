-- Connecting an app subscribes to everything it can deliver, so a member who
-- connects GitHub starts hearing about GitHub rather than facing a list of
-- forty-six events to choose from.
--
-- It cannot happen in the callback: a toolkit's triggers are subscribed one
-- Composio call at a time, and a member's browser is waiting on that redirect.
-- So the connection records that it is owed, and the maintenance worker does
-- the work — which also means it survives a restart mid-way and can be
-- retried without the member noticing.

ALTER TABLE apps_connections
    ADD COLUMN IF NOT EXISTS auto_subscribe boolean NOT NULL DEFAULT true;

-- When the subscribing finished. NULL on an active connection that wants it
-- means the work is still owed.
ALTER TABLE apps_connections
    ADD COLUMN IF NOT EXISTS auto_subscribed_at timestamptz;

-- How many times it has been tried, so a toolkit that keeps failing is given
-- up on rather than retried for ever.
ALTER TABLE apps_connections
    ADD COLUMN IF NOT EXISTS auto_subscribe_attempts integer NOT NULL DEFAULT 0;

-- What the worker claims: an active connection that wants subscribing and has
-- not had it.
CREATE INDEX IF NOT EXISTS apps_connections_awaiting_subscribe_idx
    ON apps_connections (connected_at)
    WHERE status = 'active' AND auto_subscribe AND auto_subscribed_at IS NULL;
