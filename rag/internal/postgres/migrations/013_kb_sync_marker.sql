-- A knowledge base submission is recorded in kb_synced_hash as a marker of
-- the content and every attribute submitted with it (knowledgebase.SyncMarker),
-- prefixed with its format. A ready document still carrying a marker of an
-- older format, or the bare content hash recorded before markers existed, was
-- submitted without something rag now writes, such as its access, and the
-- ingest worker puts it back in the queue
-- (postgres.RequeueUnsyncedKnowledgeBaseDocuments). This index is what keeps
-- asking for those cheap: outside a deploy it holds nothing.
CREATE INDEX IF NOT EXISTS rag_documents_kb_unsynced_idx
    ON rag_documents (id)
    WHERE status = 'ready' AND kb_synced_hash <> '' AND kb_synced_hash NOT LIKE 'v2:%';
