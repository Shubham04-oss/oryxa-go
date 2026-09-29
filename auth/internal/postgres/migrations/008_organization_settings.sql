ALTER TABLE auth_organizations
    ADD COLUMN IF NOT EXISTS domains text[] NOT NULL DEFAULT '{}',
    ADD COLUMN IF NOT EXISTS timezone text NOT NULL DEFAULT 'UTC',
    ADD COLUMN IF NOT EXISTS guests_allowed boolean NOT NULL DEFAULT false;

ALTER TABLE auth_invitations
    ADD COLUMN IF NOT EXISTS guest boolean NOT NULL DEFAULT false;
