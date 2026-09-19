-- 002 looked its check constraints up by name alone. pg_constraint spans the
-- whole database, so a same-named constraint on another table, such as a
-- batching_triggers in another schema, made it skip adding one here. These
-- look on this table and add whichever is missing. 002 itself is left as it
-- is: changed, it would run again everywhere, locking batching_triggers
-- while it did.

DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_constraint
                   WHERE conrelid = 'batching_triggers'::regclass
                     AND conname = 'batching_triggers_target_size_check') THEN
        ALTER TABLE batching_triggers ADD CONSTRAINT batching_triggers_target_size_check CHECK (target_size > 0);
    END IF;
    IF NOT EXISTS (SELECT 1 FROM pg_constraint
                   WHERE conrelid = 'batching_triggers'::regclass
                     AND conname = 'batching_triggers_pressure_check') THEN
        ALTER TABLE batching_triggers ADD CONSTRAINT batching_triggers_pressure_check CHECK (pressure >= 1);
    END IF;
    IF NOT EXISTS (SELECT 1 FROM pg_constraint
                   WHERE conrelid = 'batching_triggers'::regclass
                     AND conname = 'batching_triggers_deadline_order_check') THEN
        ALTER TABLE batching_triggers ADD CONSTRAINT batching_triggers_deadline_order_check CHECK (soft_due_at <= hard_due_at);
    END IF;
END $$;
