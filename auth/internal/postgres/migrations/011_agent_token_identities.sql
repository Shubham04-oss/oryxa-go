-- Which agent each minted agent token acts as. A Cognito access token cannot
-- carry our own claims, so its agent and role are bound to it here, by hash,
-- the way auth_cli_sessions binds a CLI token. No raw token is stored.
CREATE TABLE auth_agent_token_identities (
    token_hash text PRIMARY KEY,
    user_id text NOT NULL,
    agent_id text NOT NULL DEFAULT '',
    role text NOT NULL,
    expires_at timestamptz NOT NULL
);
CREATE INDEX auth_agent_token_identities_expiry ON auth_agent_token_identities (expires_at);
