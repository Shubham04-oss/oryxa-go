-- Existing Knowledge documents already have their provider in PostgreSQL, but
-- the Bedrock index predates the filterable provenance fields. Submit them
-- once more so provider/source filters do not silently omit older content.
UPDATE rag_documents AS document
SET status = 'pending', failure_reason = '', attempts = 0,
    locked_until = NULL, ingested_at = NULL, kb_synced_hash = '',
    kb_submitted_at = NULL, updated_at = now(),
    metadata = document.metadata || CASE document.metadata->>'provider'
        WHEN 'gmail' THEN '{"document_kind":"message"}'::jsonb
        WHEN 'slack' THEN '{"document_kind":"message"}'::jsonb
        WHEN 'googledrive' THEN '{"document_kind":"file"}'::jsonb
        WHEN 'files' THEN '{"document_kind":"file"}'::jsonb
        WHEN 'notion' THEN '{"document_kind":"page"}'::jsonb
        ELSE '{}'::jsonb END
FROM rag_corpora AS corpus
WHERE document.corpus_id = corpus.id
  AND corpus.backend = 'bedrock-kb'
  AND document.metadata ? 'knowledge_source_id';
