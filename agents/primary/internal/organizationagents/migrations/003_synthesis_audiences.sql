-- Every synthesis has an audience (docs/design/live-layer/06-agent-isolation.md,
-- L1). A teamspace's synthesis is built only from that teamspace's posts and is
-- read by its team and organization admins; teamspace_id '' is the
-- organization-wide synthesis, built only from inputs every member may read.
--
-- The one-per-organization rows written before this mixed every team's posts,
-- so they fit no audience. They are moved, not deleted, into a legacy table
-- that no route reads (an owner can inspect or drop it; Forget removes an
-- organization's rows), and each teamspace's synthesis starts fresh.

DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM information_schema.columns
        WHERE table_schema = 'agents_primary'
          AND table_name = 'organization_agent_syntheses'
          AND column_name = 'teamspace_id'
    ) THEN
        CREATE TABLE agents_primary.organization_agent_syntheses_legacy AS
            SELECT * FROM agents_primary.organization_agent_syntheses;
        DELETE FROM agents_primary.organization_agent_syntheses;
        ALTER TABLE agents_primary.organization_agent_syntheses
            ADD COLUMN teamspace_id text NOT NULL DEFAULT '';
        ALTER TABLE agents_primary.organization_agent_syntheses
            DROP CONSTRAINT organization_agent_syntheses_pkey;
        ALTER TABLE agents_primary.organization_agent_syntheses
            ADD PRIMARY KEY (organization_id, teamspace_id);
    END IF;
END $$;

-- Follow-ups already carry their source teamspace, which is their audience.
CREATE INDEX IF NOT EXISTS organization_agent_followups_teamspace_idx
    ON agents_primary.organization_agent_followups
    (organization_id, teamspace_id, created_at DESC, id DESC);
