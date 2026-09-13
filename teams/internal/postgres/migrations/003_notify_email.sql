ALTER TABLE teams_memberships
    ADD COLUMN IF NOT EXISTS notify_email boolean NOT NULL DEFAULT true;
