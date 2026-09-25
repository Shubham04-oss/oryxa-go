-- Who recorded a request's decision: 'member' for the addressed agent's
-- member deciding in their own signed-in session, 'agent' for an agent
-- deciding in a run (with that agent's id), and 'system' for Oryxa closing a
-- request made in a teamspace that no longer stands there. '' is a request
-- not decided yet, or one an older build decided, which recorded no decider:
-- the column defaults are that backfill, so existing rows need no UPDATE.
--
-- As in 002, the check is added NOT VALID so nothing is scanned under this
-- file's ACCESS EXCLUSIVE lock; 006 validates it. Every statement is safe to
-- repeat.

ALTER TABLE agents_primary.agent_interactions
    ADD COLUMN IF NOT EXISTS decided_by_kind text NOT NULL DEFAULT '';

ALTER TABLE agents_primary.agent_interactions
    ADD COLUMN IF NOT EXISTS decided_by_user_id text NOT NULL DEFAULT '';

ALTER TABLE agents_primary.agent_interactions
    ADD COLUMN IF NOT EXISTS decided_by_agent_id text NOT NULL DEFAULT '';

DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint
        WHERE conname='agent_interactions_decided_by_check'
          AND conrelid='agents_primary.agent_interactions'::regclass
    ) THEN
        ALTER TABLE agents_primary.agent_interactions
            ADD CONSTRAINT agent_interactions_decided_by_check CHECK (
                (decided_by_kind = '' AND decided_by_user_id = '' AND decided_by_agent_id = '')
                OR (decided_by_kind = 'member' AND decided_by_user_id <> '' AND decided_by_agent_id = '')
                OR (decided_by_kind = 'agent' AND decided_by_user_id <> '' AND decided_by_agent_id <> '')
                OR (decided_by_kind = 'system' AND decided_by_user_id = '' AND decided_by_agent_id = '')
            ) NOT VALID;
    END IF;
END $$;
