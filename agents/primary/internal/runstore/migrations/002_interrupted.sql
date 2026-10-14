-- A run its process stopped as it was replaced, at the end of the drain a
-- deploy gives it, ends 'interrupted' rather than 'failed': it was not a
-- failure of its own, and whoever started it may start it again.
--
-- 001's inline check is named agent_runs_status_check. It is replaced by one
-- that also knows 'interrupted', added NOT VALID so nothing is scanned under
-- this file's ACCESS EXCLUSIVE lock; 003 validates it. A database that ran this
-- already keeps its check. Safe to repeat.
DO $$
BEGIN
    IF EXISTS (
        SELECT 1 FROM pg_constraint
        WHERE conname='agent_runs_status_check'
          AND conrelid='agents_primary.agent_runs'::regclass
          AND pg_get_constraintdef(oid) NOT LIKE '%interrupted%'
    ) THEN
        ALTER TABLE agents_primary.agent_runs DROP CONSTRAINT agent_runs_status_check;
    END IF;
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint
        WHERE conname='agent_runs_status_check'
          AND conrelid='agents_primary.agent_runs'::regclass
    ) THEN
        ALTER TABLE agents_primary.agent_runs
            ADD CONSTRAINT agent_runs_status_check
            CHECK (status IN ('running', 'completed', 'failed', 'abandoned', 'interrupted')) NOT VALID;
    END IF;
END $$;
