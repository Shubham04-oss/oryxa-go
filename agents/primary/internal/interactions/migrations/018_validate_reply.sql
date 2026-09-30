-- Validates 017's consent check in this file's own transaction, under a lock
-- that lets reads and writes go on, as 014 does for 013's. A check that is
-- already valid is left as it is, so this is safe to repeat.

ALTER TABLE agents_primary.agent_interactions
    VALIDATE CONSTRAINT agent_interactions_consent_check;
