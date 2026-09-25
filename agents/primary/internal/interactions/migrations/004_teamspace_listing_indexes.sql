-- The indexes a teamspace's listings read, each built in this file's own
-- transaction: CREATE INDEX holds a SHARE lock, which lets reads go on while
-- it builds, where building them in 002 would have held that file's ACCESS
-- EXCLUSIVE lock throughout. A database that ran an earlier 002, which built
-- the first of them, keeps it: every statement is safe to repeat.

-- A side conversation reads the requests between two teammates in one
-- teamspace, polling as either of them.
CREATE INDEX IF NOT EXISTS agent_interactions_teamspace_idx
    ON agents_primary.agent_interactions (organization_id, teamspace_id, updated_at DESC, id DESC)
    WHERE teamspace_id <> '';
