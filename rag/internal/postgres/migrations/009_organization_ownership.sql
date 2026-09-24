-- A child row's organization must be the organization of its parent. The
-- existing single-column foreign keys prove the parent exists but allow a
-- document, chunk, or knowledge mapping to claim a different organization.
-- Keep the composite keys as database invariants so a future write path
-- cannot turn a bad organization id into searchable or readable content.
--
-- Each is added only where it is missing, so applying this again — which is
-- what happens to a migration whose text changes — neither fails on a
-- constraint already there nor rebuilds and revalidates one that is: the
-- unique keys are what the foreign keys reference, so they could not simply
-- be dropped and added back. Constraints are looked up on their own table,
-- since pg_constraint spans every schema in the database.
DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_constraint
                   WHERE conrelid = 'rag_corpora'::regclass
                     AND conname = 'rag_corpora_id_organization_unique') THEN
        ALTER TABLE rag_corpora
            ADD CONSTRAINT rag_corpora_id_organization_unique UNIQUE (id, organization_id);
    END IF;

    IF NOT EXISTS (SELECT 1 FROM pg_constraint
                   WHERE conrelid = 'rag_documents'::regclass
                     AND conname = 'rag_documents_id_corpus_organization_unique') THEN
        ALTER TABLE rag_documents
            ADD CONSTRAINT rag_documents_id_corpus_organization_unique
            UNIQUE (id, corpus_id, organization_id);
    END IF;

    IF NOT EXISTS (SELECT 1 FROM pg_constraint
                   WHERE conrelid = 'rag_documents'::regclass
                     AND conname = 'rag_documents_corpus_organization_fk') THEN
        ALTER TABLE rag_documents
            ADD CONSTRAINT rag_documents_corpus_organization_fk
            FOREIGN KEY (corpus_id, organization_id)
            REFERENCES rag_corpora (id, organization_id) ON DELETE CASCADE;
    END IF;

    IF NOT EXISTS (SELECT 1 FROM pg_constraint
                   WHERE conrelid = 'rag_chunks'::regclass
                     AND conname = 'rag_chunks_document_corpus_organization_fk') THEN
        ALTER TABLE rag_chunks
            ADD CONSTRAINT rag_chunks_document_corpus_organization_fk
            FOREIGN KEY (document_id, corpus_id, organization_id)
            REFERENCES rag_documents (id, corpus_id, organization_id) ON DELETE CASCADE;
    END IF;

    IF NOT EXISTS (SELECT 1 FROM pg_constraint
                   WHERE conrelid = 'rag_knowledge_documents'::regclass
                     AND conname = 'rag_knowledge_documents_document_corpus_organization_fk') THEN
        ALTER TABLE rag_knowledge_documents
            ADD CONSTRAINT rag_knowledge_documents_document_corpus_organization_fk
            FOREIGN KEY (document_id, corpus_id, organization_id)
            REFERENCES rag_documents (id, corpus_id, organization_id) ON DELETE CASCADE;
    END IF;
END $$;
