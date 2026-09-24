-- A request can be made inside a shared teamspace, and says who decides it:
-- 'agent' lets the addressed agent, or its member's primary agent, decide;
-- 'owner' holds it for that member to decide in their own session. The
-- column defaults are the backfill: every request made before teamspaces was
-- made outside one and was decided by its agent, so existing rows read ''
-- and 'agent' without an UPDATE. Every statement is safe to repeat, since
-- the runner applies this file again whenever its checksum changes.

ALTER TABLE agents_primary.agent_interactions
    ADD COLUMN IF NOT EXISTS teamspace_id text NOT NULL DEFAULT '';

ALTER TABLE agents_primary.agent_interactions
    ADD COLUMN IF NOT EXISTS approval text NOT NULL DEFAULT 'agent';

DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint
        WHERE conname='agent_interactions_approval_check'
          AND conrelid='agents_primary.agent_interactions'::regclass
    ) THEN
        ALTER TABLE agents_primary.agent_interactions
            ADD CONSTRAINT agent_interactions_approval_check CHECK (approval IN ('agent', 'owner'));
    END IF;
END $$;

-- A side conversation reads the requests between two teammates in one
-- teamspace, polling as either of them.
CREATE INDEX IF NOT EXISTS agent_interactions_teamspace_idx
    ON agents_primary.agent_interactions (organization_id, teamspace_id, updated_at DESC, id DESC)
    WHERE teamspace_id <> '';
