-- A document judged not worth indexing (a promotion, an automated
-- notification, an empty file) is removed like any other, but keeps its row
-- with the reason, so the same content is not judged again. judged_policy is
-- the quality policy the content was last judged under; a newer one judges it
-- again. Zero means never judged.
ALTER TABLE knowledge_documents ADD COLUMN IF NOT EXISTS excluded_reason text NOT NULL DEFAULT '';
ALTER TABLE knowledge_documents ADD COLUMN IF NOT EXISTS judged_policy integer NOT NULL DEFAULT 0;

-- What a run read and judged not worth indexing.
ALTER TABLE knowledge_sync_runs ADD COLUMN IF NOT EXISTS documents_excluded integer NOT NULL DEFAULT 0;
