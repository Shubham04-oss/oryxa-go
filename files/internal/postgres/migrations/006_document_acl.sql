-- Every object carries an access label within its scope: members, or admins
-- (organization owners and admins only).
--
-- The column is added, and existing billing attachments are labelled, in one
-- step that runs only while the column is missing, so applying this file
-- again — which is what happens to a migration whose text changes — relabels
-- nothing. The default stays: a task of the build this replaces only ever
-- uploads member-visible files, and a billing attachment it uploads without
-- naming an access is refused by files_objects_billing_access below.
DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_attribute
                   WHERE attrelid = 'files_objects'::regclass
                     AND attname = 'access' AND NOT attisdropped) THEN
        ALTER TABLE files_objects ADD COLUMN access text NOT NULL DEFAULT 'members'
            CONSTRAINT files_objects_access_check CHECK (access IN ('members', 'admins'));

        UPDATE files_objects SET access = 'admins' WHERE purpose = 'billing.attachment';
    END IF;
END $$;

ALTER TABLE files_objects DROP CONSTRAINT IF EXISTS files_objects_admin_access_scope;
ALTER TABLE files_objects ADD CONSTRAINT files_objects_admin_access_scope
    CHECK (access <> 'admins' OR scope = 'organization');

ALTER TABLE files_objects DROP CONSTRAINT IF EXISTS files_objects_billing_access;
ALTER TABLE files_objects ADD CONSTRAINT files_objects_billing_access
    CHECK (purpose <> 'billing.attachment' OR access = 'admins');
