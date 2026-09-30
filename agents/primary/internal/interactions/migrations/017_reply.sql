-- "Reply yourself": an owner who answers a request themself, in their own
-- words. A pending request they reply to is approved and answered in one
-- transaction, and its consent is 'reply': how far the approval let the answer
-- go is the owner's own words, and nothing of their agent's, which never runs
-- for it. A reply instead of an answer held for review completes the request,
-- and the review's action is 'reply'.
--
-- 013's consent check knows 'summary' and 'access', and 010's review check the
-- actions 'send', 'send_masked' and 'withhold'. Each is replaced, under its own
-- name so that 013 and 010 find it and leave it alone if they run again, by one
-- that also knows 'reply' and is otherwise the same. As in 010 and 013 the
-- checks are added NOT VALID, so nothing is scanned under this file's ACCESS
-- EXCLUSIVE lock, and 018 validates them. Dropping and adding happen in this one
-- transaction, so no other session ever writes to the table without them, and a
-- database that ran this already has the new checks, which are left as they
-- are. Safe to repeat.

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
    IF EXISTS (
        SELECT 1 FROM pg_constraint
        WHERE conname='agent_interactions_review_check'
          AND conrelid='agents_primary.agent_interactions'::regclass
          AND pg_get_constraintdef(oid) NOT LIKE '%reply%'
    ) THEN
        ALTER TABLE agents_primary.agent_interactions
            DROP CONSTRAINT agent_interactions_review_check;
    END IF;
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint
        WHERE conname='agent_interactions_review_check'
          AND conrelid='agents_primary.agent_interactions'::regclass
    ) THEN
        ALTER TABLE agents_primary.agent_interactions
            ADD CONSTRAINT agent_interactions_review_check
            CHECK ((status <> 'withheld' OR (review IS NOT NULL AND result IS NULL))
                AND (review IS NULL OR review->>'action' IN ('send', 'send_masked', 'withhold', 'reply'))) NOT VALID;
    END IF;
END $$;
