-- composio_account_id is the connected account Composio proxies a provider
-- source's calls through, taken from its connection at registration. Empty
-- for an internal source, such as the organization's own files, which is
-- read with a grant instead.
ALTER TABLE knowledge_sources ADD COLUMN IF NOT EXISTS composio_account_id text NOT NULL DEFAULT '';
