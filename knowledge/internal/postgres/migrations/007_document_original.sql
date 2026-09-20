-- A document whose text knowledge does not read itself — a PDF, an Office
-- file — keeps its bytes in the bucket KNOWLEDGE_ORIGINALS_BUCKET names
-- instead of in body, which stays empty for one of these. All three are set
-- together or not at all; original_size is nullable only because a column
-- cannot be conditionally NOT NULL, not because it is meaningful without the
-- other two.
ALTER TABLE knowledge_documents ADD COLUMN IF NOT EXISTS original_bucket text;
ALTER TABLE knowledge_documents ADD COLUMN IF NOT EXISTS original_key text;
ALTER TABLE knowledge_documents ADD COLUMN IF NOT EXISTS original_size bigint;
