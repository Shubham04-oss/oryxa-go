-- No raw access token, ID token, or OAuth code is stored.
CREATE TABLE auth_cli_challenges (
    state_hash text PRIMARY KEY,
    nonce text NOT NULL,
    expires_at timestamptz NOT NULL
);
CREATE INDEX auth_cli_challenges_expiry ON auth_cli_challenges (expires_at);
CREATE TABLE auth_cli_sessions (
    token_hash text PRIMARY KEY,
    organization_id text NOT NULL DEFAULT '',
    expires_at timestamptz NOT NULL
);
CREATE INDEX auth_cli_sessions_expiry ON auth_cli_sessions (expires_at);
