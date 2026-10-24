-- The sweeper (Manager.Sweep) finds sub-agents past their expiry, and
-- running ones a dead process left, across every thread.

CREATE INDEX IF NOT EXISTS subagents_expires_idx
    ON agents_primary.subagents (expires_at)
    WHERE state <> 'running';

CREATE INDEX IF NOT EXISTS subagents_running_idx
    ON agents_primary.subagents (updated_at)
    WHERE state = 'running';
