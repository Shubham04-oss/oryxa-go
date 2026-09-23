-- Sources own their documents and sync runs within one organization. A
-- source_id alone identifies a parent but does not prove that the child row's
-- organization_id agrees with it.

ALTER TABLE knowledge_documents
    ADD CONSTRAINT knowledge_documents_source_organization_fk
    FOREIGN KEY (source_id, organization_id)
    REFERENCES knowledge_sources (id, organization_id) ON DELETE CASCADE;

ALTER TABLE knowledge_sync_runs
    ADD CONSTRAINT knowledge_sync_runs_source_organization_fk
    FOREIGN KEY (source_id, organization_id)
    REFERENCES knowledge_sources (id, organization_id) ON DELETE CASCADE;
