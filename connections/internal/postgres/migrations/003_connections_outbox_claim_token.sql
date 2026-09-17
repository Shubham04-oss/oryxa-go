-- A claim on an outbox event is now owned: ClaimOutbox stamps the rows it
-- hands out with a token, and MarkOutboxPublished/MarkOutboxFailed only touch
-- a row that still carries the same token. Without it a dispatcher whose claim
-- expired mid-batch could mark a row another replica had since re-claimed,
-- clearing that claim's lock while it was still publishing.
ALTER TABLE connections_outbox ADD COLUMN IF NOT EXISTS claim_token text;
