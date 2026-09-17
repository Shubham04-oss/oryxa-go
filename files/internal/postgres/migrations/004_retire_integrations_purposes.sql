-- The integrations service was archived on 2026-09-17 and nothing writes these
-- purposes any more: integrations.piece was for the Activepieces runner removed
-- before that, and the other two for a service whose work apps now does
-- without storing anything here.
--
-- A narrowing check constraint is validated against the rows already in the
-- table, so anything left under a retired purpose is re-pointed at upload
-- first. That does not extend its life: every object carries its own
-- expires_at, set when it was created, and lifecycle reads that rather than
-- the purpose.
UPDATE files_objects
SET purpose = 'upload'
WHERE purpose IN ('integrations.piece', 'integrations.webhook-payload', 'integrations.attachment');

ALTER TABLE files_objects DROP CONSTRAINT IF EXISTS files_objects_purpose_check;
ALTER TABLE files_objects ADD CONSTRAINT files_objects_purpose_check CHECK (purpose IN (
    'upload',
    'pipeline.artifact',
    'billing.attachment'
));
