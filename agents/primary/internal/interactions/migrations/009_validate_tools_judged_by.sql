-- Validates 008's check in a transaction of its own, under a lock that lets
-- reads and writes go on, as 003 does for 002's. A check that is already
-- valid is left as it is, so this is safe to repeat.

ALTER TABLE agents_primary.agent_interactions
    VALIDATE CONSTRAINT agent_interactions_tools_judged_by_check;
