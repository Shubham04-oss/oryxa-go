-- Who a mention was refused for, and when: a member's mention that ended in a
-- refusal of the whole ask counts toward their limit of open asks for a while
-- (RefusedAskWindow), as if it were open, so an asker refused once cannot keep
-- asking again for free. A job that was never refused keeps '' and NULL, which
-- the defaults backfill without an UPDATE. Safe to apply again.
ALTER TABLE agents_primary.slack_jobs
    ADD COLUMN IF NOT EXISTS refused_user_id text NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS refused_at timestamptz;

-- The count is of one member's recent refusals in one organization.
CREATE INDEX IF NOT EXISTS slack_jobs_refused_idx
    ON agents_primary.slack_jobs (organization_id, refused_user_id, refused_at)
    WHERE refused_at IS NOT NULL;
