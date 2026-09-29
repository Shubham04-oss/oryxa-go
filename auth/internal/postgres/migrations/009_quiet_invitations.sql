-- Existing invitation events have already been redacted after publication, so
-- their quiet preference cannot be recovered reliably. Suppress withdrawal
-- notices for those rows and for old writers during a rolling upgrade. New
-- writers explicitly record false for an ordinary mailed invitation.
ALTER TABLE auth_invitations ADD COLUMN quiet boolean NOT NULL DEFAULT true;
