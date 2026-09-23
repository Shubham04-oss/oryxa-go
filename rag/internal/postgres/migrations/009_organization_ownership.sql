-- A child row's organization must be the organization of its parent. The
-- existing single-column foreign keys prove the parent exists but allow a
-- document, chunk, or knowledge mapping to claim a different organization.
-- Keep the composite keys as database invariants so a future write path
-- cannot turn a bad organization id into searchable or readable content.

ALTER TABLE rag_corpora
    ADD CONSTRAINT rag_corpora_id_organization_unique UNIQUE (id, organization_id);

ALTER TABLE rag_documents
    ADD CONSTRAINT rag_documents_id_corpus_organization_unique
    UNIQUE (id, corpus_id, organization_id);

ALTER TABLE rag_documents
    ADD CONSTRAINT rag_documents_corpus_organization_fk
    FOREIGN KEY (corpus_id, organization_id)
    REFERENCES rag_corpora (id, organization_id) ON DELETE CASCADE;

ALTER TABLE rag_chunks
    ADD CONSTRAINT rag_chunks_document_corpus_organization_fk
    FOREIGN KEY (document_id, corpus_id, organization_id)
    REFERENCES rag_documents (id, corpus_id, organization_id) ON DELETE CASCADE;

ALTER TABLE rag_knowledge_documents
    ADD CONSTRAINT rag_knowledge_documents_document_corpus_organization_fk
    FOREIGN KEY (document_id, corpus_id, organization_id)
    REFERENCES rag_documents (id, corpus_id, organization_id) ON DELETE CASCADE;
