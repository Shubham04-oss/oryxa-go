-- Every document carries an access label: members (anyone its corpus's
-- placement admits) or admins (organization owners and admins only).
--
-- The column is added, and the documents already there are labelled, in one
-- step that runs only while the column is missing. Applying this file again
-- — which is what happens to a migration whose text changes — must not
-- relabel a document or requeue the knowledge base a second time.
DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_attribute
                   WHERE attrelid = 'rag_documents'::regclass
                     AND attname = 'access' AND NOT attisdropped) THEN
        -- Existing documents were member-visible, so that is what they are
        -- labelled; the default exists only to label them, and is dropped
        -- below.
        ALTER TABLE rag_documents ADD COLUMN access text NOT NULL DEFAULT 'members'
            CONSTRAINT rag_documents_access_check CHECK (access IN ('members', 'admins'));

        -- Older organization file documents have no purpose stored in RAG.
        -- Keep them admin-only until a privileged caller re-reads Files and
        -- reclassifies them from the authoritative object metadata.
        UPDATE rag_documents SET access = 'admins'
        WHERE source_kind = 'file' AND corpus_id IN (
            SELECT id FROM rag_corpora WHERE scope = 'organization'
        );

        -- Old Bedrock documents have no access attribute in the managed
        -- index. Re-submit them with one before member queries use the ACL
        -- filter.
        UPDATE rag_documents SET status = 'pending', failure_reason = '', attempts = 0,
            locked_until = NULL, kb_synced_hash = '', kb_submitted_at = NULL
        WHERE corpus_id IN (SELECT id FROM rag_corpora WHERE backend = 'bedrock-kb');
    END IF;
END $$;

-- No default from here on: every write names its access. During the rolling
-- deploy that adds this column, a task of the build being replaced still
-- inserts documents without naming one, and a default would label them
-- members for good — an admin-only billing attachment published in that
-- window would be visible to every member. Without one, such an insert fails,
-- and the event behind it is delivered again, to a task of this build.
ALTER TABLE rag_documents ALTER COLUMN access DROP DEFAULT;
