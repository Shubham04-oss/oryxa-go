-- What a trigger is beyond what a member asked for, now that Oryxa's own
-- integrations carry the events instead of Composio.
--
-- A Composio subscription is an instance at Composio, keyed there by account,
-- trigger and config, and apps keeps Composio's id for it. An Oryxa
-- subscription has no such instance: it is a webhook endpoint the core made
-- and registered with the app, or a cursor the poll worker carries, and the
-- two live beside each other in one table without either of them having to
-- know about the other.
--
-- So a trigger gains its broker, and with it the three things only a polled
-- Oryxa trigger has: where its last poll got to, when it is next due, and
-- what the worker claims it with.

-- Who delivers the events: 'oryxa' for a subscription made through an
-- integration, '' for Composio's, whose rows are left exactly as they were.
ALTER TABLE apps_triggers
    ADD COLUMN IF NOT EXISTS broker text NOT NULL DEFAULT '';

-- One live subscription per connection, event and config, which is what
-- Composio keys its instances on too. An Oryxa row has no Composio instance,
-- so this is its own key; '' leaves a Composio row to the index below.
ALTER TABLE apps_triggers
    ADD COLUMN IF NOT EXISTS own_key text NOT NULL DEFAULT '';

-- Where a polled trigger's last poll got to, which is the app's own cursor
-- and nothing else: the first poll without one starts from now rather than
-- delivering the app's history.
ALTER TABLE apps_triggers
    ADD COLUMN IF NOT EXISTS cursor text NOT NULL DEFAULT '';

-- When the trigger is next polled. NULL on a push trigger, which is answered
-- by the app rather than asked.
ALTER TABLE apps_triggers
    ADD COLUMN IF NOT EXISTS next_poll_at timestamptz;

-- Until when this worker's claim owns a poll. FOR UPDATE SKIP LOCKED protects
-- only the claiming statement itself, so the moment it commits a second
-- replica's next tick would otherwise see the same row as unclaimed and take
-- it too: two polls for the same cursor, and the second one's events deduped
-- against the first's for nothing.
ALTER TABLE apps_triggers
    ADD COLUMN IF NOT EXISTS poll_claimed_until timestamptz;

-- The Composio instance index, skipping ''. Every Oryxa row has no Composio
-- instance, so they would all collide on the empty string and the second one
-- would be read back as though the first were its own.
DROP INDEX IF EXISTS apps_triggers_instance_idx;
CREATE UNIQUE INDEX IF NOT EXISTS apps_triggers_instance_idx
    ON apps_triggers (composio_trigger_id)
    WHERE status <> 'removed' AND composio_trigger_id <> '';

CREATE UNIQUE INDEX IF NOT EXISTS apps_triggers_own_idx
    ON apps_triggers (own_key)
    WHERE status <> 'removed' AND own_key <> '';

-- Polled triggers that are due, which is what a worker's claim statement
-- walks in order. Every replica may run that claim, so it is the index the
-- pass spends its time in.
CREATE INDEX IF NOT EXISTS apps_triggers_poll_due_idx
    ON apps_triggers (next_poll_at, id)
    WHERE status = 'active' AND broker = 'oryxa' AND kind = 'poll';
