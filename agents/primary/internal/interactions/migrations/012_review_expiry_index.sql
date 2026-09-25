-- The waker withholds answers whose request expired while they waited in
-- review, and looks for them on every poll (Store.WithholdExpired). This
-- index holds only requests in review, which are few at any time, so that
-- look reads them alone however many requests the table keeps. CREATE INDEX
-- holds a SHARE lock while it builds, which lets reads go on, as 007's does.
-- Safe to repeat.

CREATE INDEX IF NOT EXISTS agent_interactions_review_expiry_idx
    ON agents_primary.agent_interactions (expires_at, id)
    WHERE status = 'review';
