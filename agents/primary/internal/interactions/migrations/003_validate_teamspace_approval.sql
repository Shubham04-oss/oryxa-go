-- Validates 002's approval check, in a transaction of its own: VALIDATE
-- CONSTRAINT reads every row under a SHARE UPDATE EXCLUSIVE lock, which lets
-- reads and writes go on, where adding the check validated would have read
-- them under 002's ACCESS EXCLUSIVE lock. A check that is already valid,
-- such as one an earlier 002 added validated, is left as it is, so this is
-- safe to repeat.

ALTER TABLE agents_primary.agent_interactions
    VALIDATE CONSTRAINT agent_interactions_approval_check;
