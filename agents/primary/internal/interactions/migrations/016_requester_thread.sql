-- Keep the conversation the asking agent filed a request from. The constant
-- default backfills earlier requests without rewriting them; those continue
-- to use their existing delivery conversation. Safe to apply again.
ALTER TABLE agents_primary.agent_interactions
    ADD COLUMN IF NOT EXISTS requester_thread_id text NOT NULL DEFAULT '';

-- Main primary chats and managed-agent conversations have separate session
-- namespaces. A trusted run records which held its originating thread.
ALTER TABLE agents_primary.agent_interactions
    ADD COLUMN IF NOT EXISTS requester_thread_app_name text NOT NULL DEFAULT '';
