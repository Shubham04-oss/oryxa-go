-- Owner-first asks: who is to answer a request, as Jev judged it when the
-- request was made. 'owner' is a request whose answer needs its owner's own
-- judgment, which waits for them and wakes no agent until they let it;
-- 'agent' is one their agent may answer once allowed. '' is a request made
-- before this was kept, which the column default backfills without an UPDATE,
-- which the API leaves out, and which reads as 'agent'. Nothing is backfilled.
--
-- As in 008, the check is added NOT VALID, so nothing is scanned under this
-- file's ACCESS EXCLUSIVE lock. It holds for every row written from now on,
-- and every row already there holds the default, so a later file may validate
-- it under a lighter lock, as 009 does for 008's. Every statement is safe to
-- repeat.

ALTER TABLE agents_primary.agent_interactions
    ADD COLUMN IF NOT EXISTS answer_by text NOT NULL DEFAULT '';

DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint
        WHERE conname='agent_interactions_answer_by_check'
          AND conrelid='agents_primary.agent_interactions'::regclass
    ) THEN
        ALTER TABLE agents_primary.agent_interactions
            ADD CONSTRAINT agent_interactions_answer_by_check
            CHECK (answer_by IN ('', 'owner', 'agent')) NOT VALID;
    END IF;
END $$;
