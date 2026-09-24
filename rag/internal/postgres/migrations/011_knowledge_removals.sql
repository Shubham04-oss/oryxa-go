-- Remember removals even when the matching publish has not arrived yet.
-- Knowledge events are delivered at least once and may arrive out of order.
CREATE TABLE IF NOT EXISTS rag_knowledge_removals (
    organization_id text NOT NULL,
    knowledge_document_id text NOT NULL,
    removed_at timestamptz NOT NULL,
    PRIMARY KEY (organization_id, knowledge_document_id)
);

-- Recording a removal lets go of tombstones old enough that no publish sent
-- before them can still be delivered; this is how it finds them.
CREATE INDEX IF NOT EXISTS rag_knowledge_removals_removed_at_idx
    ON rag_knowledge_removals (removed_at);
