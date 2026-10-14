-- What a specialist's agent token may reach in its member's apps, as agents
-- sent it when the token was minted (I3). Auth only carries it back in
-- agent_claims; apps enforces it. NULL for every other token.
ALTER TABLE auth_agent_token_identities ADD COLUMN app_grant jsonb;
