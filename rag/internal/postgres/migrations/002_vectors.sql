-- The pgvector tables, one per vector width.
--
-- One table per width is not a preference. pgvector needs a declared width on
-- the column before it can build an index on it, and this service serves
-- corpora of several widths at once, so a single shared column would mean
-- every search was an exact scan of every vector in the database.
--
-- pgvector puts two ceilings on an HNSW index: the vector type stops at 2000
-- dimensions and halfvec at 4000. So widths above 2000 are stored as halfvec,
-- which is half the storage and, for the unit-length vectors these models
-- emit, a precision difference that does not show up in ranking. Without it a
-- 3072-wide corpus such as gemini-embedding-001 could not be indexed at all.
-- A width above 4000 cannot be indexed either way and belongs in
-- rag_vectors_other below.
--
-- The index is built for cosine, which is the default metric and the one every
-- model in the catalog is trained for. A corpus on another metric is correct
-- but scans; add the matching operator class here if one is ever needed.
DO $$
DECLARE
    width integer;
    vectors_table text;
    column_type text;
    operator_class text;
BEGIN
    FOREACH width IN ARRAY ARRAY[256, 384, 512, 768, 1024, 1536, 3072] LOOP
        vectors_table := format('rag_vectors_%s', width);
        IF width > 2000 THEN
            column_type := format('halfvec(%s)', width);
            operator_class := 'halfvec_cosine_ops';
        ELSE
            column_type := format('vector(%s)', width);
            operator_class := 'vector_cosine_ops';
        END IF;

        -- The chunk reference means a deleted document takes its vectors with
        -- it, so this table cannot drift into holding vectors for chunks that
        -- no longer exist.
        EXECUTE format($statement$
            CREATE TABLE IF NOT EXISTS %I (
                chunk_id text PRIMARY KEY REFERENCES rag_chunks(id) ON DELETE CASCADE,
                corpus_id text NOT NULL,
                organization_id text NOT NULL,
                document_id text NOT NULL,
                embedding %s NOT NULL,
                metadata jsonb NOT NULL DEFAULT '{}'::jsonb
            )$statement$, vectors_table, column_type);

        EXECUTE format('CREATE INDEX IF NOT EXISTS %I ON %I (corpus_id, organization_id)',
            vectors_table || '_corpus_idx', vectors_table);
        EXECUTE format('CREATE INDEX IF NOT EXISTS %I ON %I (document_id)',
            vectors_table || '_document_idx', vectors_table);
        EXECUTE format('CREATE INDEX IF NOT EXISTS %I ON %I USING hnsw (embedding %s)',
            vectors_table || '_hnsw_idx', vectors_table, operator_class);
    END LOOP;
END $$;

-- Where a width with no table of its own goes. The column has no declared
-- width, so it cannot be indexed and is searched exactly: slow on a large
-- corpus, correct on any. A model released after this migration works on the
-- day it is configured, and earns a table of its own when it is worth one.
CREATE TABLE IF NOT EXISTS rag_vectors_other (
    chunk_id text PRIMARY KEY REFERENCES rag_chunks(id) ON DELETE CASCADE,
    corpus_id text NOT NULL,
    organization_id text NOT NULL,
    document_id text NOT NULL,
    embedding vector NOT NULL,
    metadata jsonb NOT NULL DEFAULT '{}'::jsonb
);

CREATE INDEX IF NOT EXISTS rag_vectors_other_corpus_idx
    ON rag_vectors_other (corpus_id, organization_id);

CREATE INDEX IF NOT EXISTS rag_vectors_other_document_idx
    ON rag_vectors_other (document_id);
