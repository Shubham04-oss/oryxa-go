-- Validates 002's status check in this file's own transaction, under a lock
-- that lets reads and writes go on. A valid check is left as it is, so this is
-- safe to repeat.
ALTER TABLE agents_primary.agent_runs VALIDATE CONSTRAINT agent_runs_status_check;
