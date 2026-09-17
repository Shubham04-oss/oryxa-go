-- When a chunk's vector was actually written.
--
-- Re-cutting a document keeps the chunks whose text did not change, so their
-- vectors need not be paid for again. Matching on the text hash alone is not
-- enough to know a vector exists: a first attempt writes every chunk row and
-- may then fail while embedding them. On the retry those rows match by hash,
-- look reusable, and the document is marked searchable holding no vectors at
-- all — the worst kind of failure, because it looks like success.
--
-- So a chunk is only reusable once something has confirmed its vector was
-- written, which is what this records.
ALTER TABLE rag_chunks
    ADD COLUMN IF NOT EXISTS embedded_at timestamptz;

-- Existing rows predate this column. They were written by a pipeline that
-- marked a document ready only after its vectors were upserted, so the ones
-- under a document that reached 'ready' do have vectors; anything else is
-- re-embedded on the next pass, which is the safe direction.
UPDATE rag_chunks AS chunk
SET embedded_at = document.ingested_at
FROM rag_documents AS document
WHERE chunk.document_id = document.id
  AND chunk.embedded_at IS NULL
  AND document.status = 'ready'
  AND document.ingested_at IS NOT NULL;

-- Finding the documents indexed from one file, for when that file is deleted.
-- Without this, removing them means reading every document in every corpus.
CREATE INDEX IF NOT EXISTS rag_documents_source_ref_idx
    ON rag_documents (organization_id, source_kind, source_ref)
    WHERE source_ref <> '';
