-- A claim on an outbox event is now owned, by internal/outbox: it stamps the
-- rows it hands out with a token, and marks published or failed only touch a
-- row that still carries the same token. Without it a dispatcher whose claim
-- expired mid-batch could mark a row another replica had since re-claimed,
-- clearing that claim's lock while it was still publishing.
ALTER TABLE auth_outbox ADD COLUMN IF NOT EXISTS claim_token text;
