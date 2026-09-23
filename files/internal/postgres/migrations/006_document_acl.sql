ALTER TABLE files_objects ADD COLUMN access text NOT NULL DEFAULT 'members'
    CHECK (access IN ('members', 'admins'));

UPDATE files_objects SET access = 'admins' WHERE purpose = 'billing.attachment';

ALTER TABLE files_objects ADD CONSTRAINT files_objects_admin_access_scope
    CHECK (access <> 'admins' OR scope = 'organization');
ALTER TABLE files_objects ADD CONSTRAINT files_objects_billing_access
    CHECK (purpose <> 'billing.attachment' OR access = 'admins');
