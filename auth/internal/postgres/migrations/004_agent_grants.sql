-- A member's consent for the Cognito agent app client to act as them while
-- they are absent: an envelope-encrypted refresh token, one per member.
CREATE TABLE IF NOT EXISTS auth_agent_grants (
    user_id text PRIMARY KEY REFERENCES auth_users(id) ON DELETE CASCADE,
    encrypted_refresh_token jsonb NOT NULL,
    scope text NOT NULL,
    created_at timestamptz NOT NULL,
    refreshed_at timestamptz
);
