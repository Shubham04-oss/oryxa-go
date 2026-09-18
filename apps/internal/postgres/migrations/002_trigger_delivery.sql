-- What a trigger is, beyond what a member asked for, and whether it has ever
-- delivered anything.
--
-- A subscription can be accepted by Composio and still deliver nothing: an
-- app that polls rather than pushes arrives on its own interval, and one
-- whose webhook the member has to register by hand arrives never until they
-- do. Neither is visible afterwards from what was stored, so both are
-- recorded when the subscription is made, and every delivery is counted
-- against the trigger it came from.

ALTER TABLE apps_triggers
    ADD COLUMN IF NOT EXISTS kind text NOT NULL DEFAULT '';

ALTER TABLE apps_triggers
    ADD COLUMN IF NOT EXISTS manual_setup boolean NOT NULL DEFAULT false;

ALTER TABLE apps_triggers
    ADD COLUMN IF NOT EXISTS delivery_count bigint NOT NULL DEFAULT 0;

ALTER TABLE apps_triggers
    ADD COLUMN IF NOT EXISTS last_delivery_at timestamptz;

-- Triggers that have never delivered, which is what an operator looks for
-- when asking whether a subscription works at all.
CREATE INDEX IF NOT EXISTS apps_triggers_silent_idx
    ON apps_triggers (toolkit, created_at)
    WHERE status = 'active' AND delivery_count = 0;
