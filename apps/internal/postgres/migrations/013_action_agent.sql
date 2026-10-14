-- Which of the member's agents ran a tool: its ID (agt_…, oag_…) and role
-- (primary, specialist, organization), as auth answers for its token. Empty
-- for a member's own run, and for an agent token minted before agents were
-- named (its role is then primary).
ALTER TABLE apps_actions
    ADD COLUMN agent_id text NOT NULL DEFAULT '',
    ADD COLUMN agent_role text NOT NULL DEFAULT '';
