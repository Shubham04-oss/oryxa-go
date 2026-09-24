-- Knowledge documents filter on provider, source, kind and category. Existing
-- ones already have their provider in PostgreSQL but no document_kind, which
-- is derived here for the providers whose kind is known, and the Bedrock
-- index predates every one of these fields, so each document this fills in
-- is submitted again.
--
-- It only touches a document still missing its document_kind, so applying
-- this file again — which is what happens to a migration whose text changes
-- — finds nothing left to do. Every other bedrock-kb document is submitted
-- again anyway: 010 requeued them all in the same release, and a document
-- whose submission predates the sync marker that records these fields is
-- requeued by the ingest worker (see 013).
UPDATE rag_documents AS document
SET status = 'pending', failure_reason = '', attempts = 0,
    locked_until = NULL, ingested_at = NULL, kb_synced_hash = '',
    kb_submitted_at = NULL, updated_at = now(),
    metadata = document.metadata || jsonb_build_object('document_kind',
        CASE document.metadata->>'provider'
            WHEN 'gmail' THEN 'message'
            WHEN 'slack' THEN 'message'
            WHEN 'googledrive' THEN 'file'
            WHEN 'files' THEN 'file'
            WHEN 'notion' THEN 'page'
        END)
FROM rag_corpora AS corpus
WHERE document.corpus_id = corpus.id
  AND corpus.backend = 'bedrock-kb'
  AND document.metadata ? 'knowledge_source_id'
  AND NOT document.metadata ? 'document_kind'
  AND document.metadata->>'provider' IN ('gmail', 'slack', 'googledrive', 'files', 'notion');
