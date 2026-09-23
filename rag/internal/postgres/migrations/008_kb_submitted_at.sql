-- When a document's current content was handed to the knowledge base.
--
-- The ingest worker polls a submitted document until the knowledge base says
-- it is finished, and those polls do not count as attempts, so a document the
-- knowledge base never finished with was polled every few seconds for good.
-- Knowing when the submission was made is what lets the worker give up on one
-- that has run far longer than indexing takes and submit it again, which does
-- count, so a document that never finishes ends up failed rather than
-- ingesting forever. See knowledgeBaseStall in rag/internal/ingest.
ALTER TABLE rag_documents ADD COLUMN IF NOT EXISTS kb_submitted_at timestamptz;

-- A submission already waiting when this column arrived has no time of its
-- own. Its last poll stands in for one, so its wait starts now rather than
-- never.
UPDATE rag_documents
SET kb_submitted_at = updated_at
WHERE kb_synced_hash <> '' AND kb_submitted_at IS NULL AND status = 'ingesting';
