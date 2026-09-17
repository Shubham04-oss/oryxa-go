-- A member looks up the invitations pending for their own email address,
-- whichever organization sent them.
CREATE INDEX IF NOT EXISTS auth_invitations_pending_email_idx
    ON auth_invitations (lower(email))
    WHERE status = 'pending';
