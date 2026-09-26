-- The answer guard. What it found in an answer is kept in guard (its state
-- and the kinds it found, never a value); an answer it holds for the
-- addressed member's review waits in held_result, in the two versions they
-- may send, while the request is 'review'; and their review, or Oryxa
-- withholding it when the request expires first, is kept in review. A
-- request can now wait in 'review' and end 'withheld', which 001's status
-- check does not allow, so that check is replaced by one that does. Every
-- column is NULL for a request made before, which needs no backfill.
--
-- As in 002, every check is added NOT VALID, so nothing is scanned under
-- this file's ACCESS EXCLUSIVE lock; 011 validates them. Dropping 001's
-- check and adding its replacement happen in this one transaction, so no
-- other session ever writes to the table without one. Every statement is
-- safe to repeat: a database that ran this already has no old check to drop
-- and keeps the checks it added.

ALTER TABLE agents_primary.agent_interactions
    ADD COLUMN IF NOT EXISTS guard jsonb;

ALTER TABLE agents_primary.agent_interactions
    ADD COLUMN IF NOT EXISTS held_result jsonb;

ALTER TABLE agents_primary.agent_interactions
    ADD COLUMN IF NOT EXISTS review jsonb;

ALTER TABLE agents_primary.agent_interactions
    DROP CONSTRAINT IF EXISTS agent_interactions_status_check;

DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint
        WHERE conname='agent_interactions_status_values_check'
          AND conrelid='agents_primary.agent_interactions'::regclass
    ) THEN
        ALTER TABLE agents_primary.agent_interactions
            ADD CONSTRAINT agent_interactions_status_values_check
            CHECK (status IN ('pending', 'approved', 'rejected', 'completed', 'failed', 'review', 'withheld')) NOT VALID;
    END IF;
    -- The held answer is kept while the request is in review, and only
    -- then; a request in review has no result the member who asked could
    -- read.
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint
        WHERE conname='agent_interactions_held_check'
          AND conrelid='agents_primary.agent_interactions'::regclass
    ) THEN
        ALTER TABLE agents_primary.agent_interactions
            ADD CONSTRAINT agent_interactions_held_check
            CHECK ((status = 'review') = (held_result IS NOT NULL) AND (status <> 'review' OR result IS NULL)) NOT VALID;
    END IF;
    -- A withheld request says who withheld it and has no result; a review
    -- is one of the three actions.
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint
        WHERE conname='agent_interactions_review_check'
          AND conrelid='agents_primary.agent_interactions'::regclass
    ) THEN
        ALTER TABLE agents_primary.agent_interactions
            ADD CONSTRAINT agent_interactions_review_check
            CHECK ((status <> 'withheld' OR (review IS NOT NULL AND result IS NULL))
                AND (review IS NULL OR review->>'action' IN ('send', 'send_masked', 'withhold'))) NOT VALID;
    END IF;
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint
        WHERE conname='agent_interactions_guard_check'
          AND conrelid='agents_primary.agent_interactions'::regclass
    ) THEN
        ALTER TABLE agents_primary.agent_interactions
            ADD CONSTRAINT agent_interactions_guard_check
            CHECK (guard IS NULL OR guard->>'state' IN ('clear', 'masked', 'held')) NOT VALID;
    END IF;
END $$;
