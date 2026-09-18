-- The bridge between a document knowledge published and the rag document it
-- became, keyed by knowledge's own document id.
--
-- knowledge and rag each mint their own ids, and a later published or removed
-- event only ever names knowledge's. Without this a content change could not
-- find the rag document to update in place, and a removal could not find it
-- to delete — both would have to guess, or leave a stale copy searchable
-- forever.
CREATE TABLE IF NOT EXISTS rag_knowledge_documents (
    knowledge_document_id text PRIMARY KEY,
    organization_id text NOT NULL,
    corpus_id text NOT NULL REFERENCES rag_corpora(id) ON DELETE CASCADE,
    document_id text NOT NULL REFERENCES rag_documents(id) ON DELETE CASCADE,
    content_hash text NOT NULL,
    extracted_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL
);

-- Both foreign keys above cascade a corpus or a document going away — an
-- organization, a team or a member wiped, or the document removed some other
-- way — onto this table too, so a later event can never find a mapping that
-- points at nothing. Indexing both columns is what keeps that cascade cheap:
-- without one, deleting a row from rag_corpora or rag_documents would have to
-- scan every row here to find what points at it.
CREATE INDEX IF NOT EXISTS rag_knowledge_documents_corpus_idx
    ON rag_knowledge_documents (corpus_id);

CREATE INDEX IF NOT EXISTS rag_knowledge_documents_document_idx
    ON rag_knowledge_documents (document_id);
