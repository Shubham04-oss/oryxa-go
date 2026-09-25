-- Validates 010's checks, each in this file's own transaction, under a lock
-- that lets reads and writes go on, as 003 does for 002's. A check that is
-- already valid is left as it is, so this is safe to repeat.

ALTER TABLE agents_primary.agent_interactions
    VALIDATE CONSTRAINT agent_interactions_status_values_check;

ALTER TABLE agents_primary.agent_interactions
    VALIDATE CONSTRAINT agent_interactions_held_check;

ALTER TABLE agents_primary.agent_interactions
    VALIDATE CONSTRAINT agent_interactions_review_check;

ALTER TABLE agents_primary.agent_interactions
    VALIDATE CONSTRAINT agent_interactions_guard_check;
