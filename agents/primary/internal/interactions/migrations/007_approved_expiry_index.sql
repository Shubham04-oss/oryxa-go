-- The waker closes approved requests whose expiry passed with their work
-- unanswered, and looks for them on every poll (Store.CloseExpired). This
-- index holds only approved requests, which are few at any time, so that
-- look reads them alone however many requests the table keeps. CREATE INDEX
-- holds a SHARE lock while it builds, which lets reads go on, as 004's do.
-- Safe to repeat.

CREATE INDEX IF NOT EXISTS agent_interactions_approved_expiry_idx
    ON agents_primary.agent_interactions (expires_at, id)
    WHERE status = 'approved';
