-- Validates 013's checks, each in this file's own transaction, under a lock
-- that lets reads and writes go on, as 003 does for 002's. A check that is
-- already valid is left as it is, so this is safe to repeat. 013 replaced
-- 005's decider check, so it is validated again here.

ALTER TABLE agents_primary.agent_interactions
    VALIDATE CONSTRAINT agent_interactions_origin_check;

ALTER TABLE agents_primary.agent_interactions
    VALIDATE CONSTRAINT agent_interactions_consent_check;

ALTER TABLE agents_primary.agent_interactions
    VALIDATE CONSTRAINT agent_interactions_consent_decision_check;

ALTER TABLE agents_primary.agent_interactions
    VALIDATE CONSTRAINT agent_interactions_decided_via_check;

ALTER TABLE agents_primary.agent_interactions
    VALIDATE CONSTRAINT agent_interactions_decided_surface_check;

ALTER TABLE agents_primary.agent_interactions
    VALIDATE CONSTRAINT agent_interactions_decided_by_check;
