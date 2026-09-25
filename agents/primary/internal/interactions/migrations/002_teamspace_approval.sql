-- A request can be made inside a shared teamspace, and says who decides it:
-- 'agent' lets the addressed agent, or its member's primary agent, decide;
-- 'owner' holds it for that member to decide in their own session. The
-- column defaults are the backfill: every request made before teamspaces was
-- made outside one and was decided by its agent, so existing rows read ''
-- and 'agent' without an UPDATE. Every statement is safe to repeat, since
-- the runner applies this file again whenever its checksum changes.
--
-- Each file runs in one transaction, and adding a column takes an ACCESS
-- EXCLUSIVE lock until it commits, so this file does nothing that scans or
-- builds while holding it: the check is added NOT VALID (new rows are
-- checked, existing ones are not read) and 003 validates it in a transaction
-- of its own; the teamspace listing index is built by 004.

ALTER TABLE agents_primary.agent_interactions
    ADD COLUMN IF NOT EXISTS teamspace_id text NOT NULL DEFAULT '';

ALTER TABLE agents_primary.agent_interactions
    ADD COLUMN IF NOT EXISTS approval text NOT NULL DEFAULT 'agent';

-- A database that ran this file before it said NOT VALID already has the
-- check, validated, and keeps it as it is.
DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint
        WHERE conname='agent_interactions_approval_check'
          AND conrelid='agents_primary.agent_interactions'::regclass
    ) THEN
        ALTER TABLE agents_primary.agent_interactions
            ADD CONSTRAINT agent_interactions_approval_check CHECK (approval IN ('agent', 'owner')) NOT VALID;
    END IF;
END $$;
