-- Sources own their documents and sync runs within one organization. A
-- source_id alone identifies a parent but does not prove that the child row's
-- organization_id agrees with it.
--
-- Each key is added only where it is missing, so applying this file again —
-- which is what happens to a migration whose text changes — neither fails on
-- a key already there nor revalidates it. Constraints are looked up on their
-- own table, since pg_constraint spans every schema in the database.
DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_constraint
                   WHERE conrelid = 'knowledge_documents'::regclass
                     AND conname = 'knowledge_documents_source_organization_fk') THEN
        ALTER TABLE knowledge_documents
            ADD CONSTRAINT knowledge_documents_source_organization_fk
            FOREIGN KEY (source_id, organization_id)
            REFERENCES knowledge_sources (id, organization_id) ON DELETE CASCADE;
    END IF;

    IF NOT EXISTS (SELECT 1 FROM pg_constraint
                   WHERE conrelid = 'knowledge_sync_runs'::regclass
                     AND conname = 'knowledge_sync_runs_source_organization_fk') THEN
        ALTER TABLE knowledge_sync_runs
            ADD CONSTRAINT knowledge_sync_runs_source_organization_fk
            FOREIGN KEY (source_id, organization_id)
            REFERENCES knowledge_sources (id, organization_id) ON DELETE CASCADE;
    END IF;
END $$;
