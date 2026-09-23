-- Existing documents were member-visible. New documents carry an explicit
-- access label, with the same default for backward compatibility.
ALTER TABLE rag_documents ADD COLUMN access text NOT NULL DEFAULT 'members'
    CHECK (access IN ('members', 'admins'));

-- Older organization file documents have no purpose stored in RAG. Keep
-- them admin-only until a privileged caller re-reads Files and reclassifies
-- them from the authoritative object metadata.
UPDATE rag_documents SET access = 'admins'
WHERE source_kind = 'file' AND corpus_id IN (
    SELECT id FROM rag_corpora WHERE scope = 'organization'
);

-- Old Bedrock documents have no access attribute in the managed index.
-- Re-submit them with one before member queries use the ACL filter.
UPDATE rag_documents SET status = 'pending', failure_reason = '', attempts = 0,
    locked_until = NULL, kb_synced_hash = '', kb_submitted_at = NULL
WHERE corpus_id IN (SELECT id FROM rag_corpora WHERE backend = 'bedrock-kb');
