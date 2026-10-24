-- A sign-in is finished only by the browser that started it (#477): the
-- SHA-256 of a nonce that browser keeps in an HttpOnly cookie. NULL on rows
-- from before this, which expire within minutes and are then refused.
ALTER TABLE integrations.oauth_states
    ADD COLUMN IF NOT EXISTS browser_hash text;
