-- Who chose the toolboxes a request asks for (requested_tools): 'requester'
-- for its requester naming them, 'jev' for Jev judging what a request made in
-- a teamspace that named none needs, and 'none' when nobody did, so it asks
-- for none. '' is a request made before this was kept, which the column
-- default backfills without an UPDATE, and which the API leaves out.
--
-- As in 002, the check is added NOT VALID, so nothing is scanned under this
-- file's ACCESS EXCLUSIVE lock; 009 validates it. Every statement is safe to
-- repeat.

ALTER TABLE agents_primary.agent_interactions
    ADD COLUMN IF NOT EXISTS tools_judged_by text NOT NULL DEFAULT '';

DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint
        WHERE conname='agent_interactions_tools_judged_by_check'
          AND conrelid='agents_primary.agent_interactions'::regclass
    ) THEN
        ALTER TABLE agents_primary.agent_interactions
            ADD CONSTRAINT agent_interactions_tools_judged_by_check
            CHECK (tools_judged_by IN ('', 'requester', 'jev', 'none')) NOT VALID;
    END IF;
END $$;
