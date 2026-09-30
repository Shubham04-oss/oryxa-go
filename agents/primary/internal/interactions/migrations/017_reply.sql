-- "Reply yourself": an owner who answers a request themself, in their own
-- words. A pending request they reply to is approved and answered in one
-- transaction, and its consent is 'reply': how far the approval let the answer
-- go is the owner's own words, and nothing of their agent's, which never runs
-- for it. A reply to an answer held for review is recorded in the review's
-- JSON, which needs nothing here.
--
-- 013's consent check knows 'summary' and 'access'. It is replaced, under its
-- own name so that 013 finds it and leaves it alone if it runs again, by one
-- that also knows 'reply'. As in 013 the check is added NOT VALID, so nothing
-- is scanned under this file's ACCESS EXCLUSIVE lock, and 018 validates it.
-- Dropping and adding happen in this one transaction, so no other session ever
-- writes to the table without one, and a database that ran this already has
-- the new check, which is left as it is. Safe to repeat.

DO $$
BEGIN
    IF EXISTS (
        SELECT 1 FROM pg_constraint
        WHERE conname='agent_interactions_consent_check'
          AND conrelid='agents_primary.agent_interactions'::regclass
          AND pg_get_constraintdef(oid) NOT LIKE '%reply%'
    ) THEN
        ALTER TABLE agents_primary.agent_interactions
            DROP CONSTRAINT agent_interactions_consent_check;
    END IF;
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint
        WHERE conname='agent_interactions_consent_check'
          AND conrelid='agents_primary.agent_interactions'::regclass
    ) THEN
        ALTER TABLE agents_primary.agent_interactions
            ADD CONSTRAINT agent_interactions_consent_check
            CHECK (consent IN ('', 'summary', 'access', 'reply')) NOT VALID;
    END IF;
END $$;
