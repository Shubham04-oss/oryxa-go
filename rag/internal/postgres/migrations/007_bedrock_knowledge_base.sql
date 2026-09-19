-- Bedrock Knowledge Bases join pgvector and S3 Vectors as a third place a
-- corpus's vectors can live: one fully managed, serverless knowledge base per
-- deployment that parses (including PDF and Office files), chunks, embeds and
-- retrieves on rag's behalf. A corpus on this backend is never cut or
-- embedded here, so it needs none of the chunk or vector tables the other two
-- backends share — only what ingesting a whole document into the knowledge
-- base, and polling its outcome, needs to remember. See rag/README.md.

-- Unnamed CHECK constraints on a column are named "<table>_<column>_check" by
-- Postgres, which is what backend's own constraint has carried since
-- 001_rag.sql; naming it explicitly here is what lets it be replaced rather
-- than only ever added to.
ALTER TABLE rag_corpora DROP CONSTRAINT IF EXISTS rag_corpora_backend_check;
ALTER TABLE rag_corpora ADD CONSTRAINT rag_corpora_backend_check
    CHECK (backend IN ('pgvector', 's3vectors', 'bedrock-kb'));

-- A knowledge base embeds with whatever model it was created with — Titan v2
-- at 1024 dimensions, cosine — chosen once by OpenTofu outside rag, not by
-- whoever creates a corpus. Pinning it here too means a corpus that could
-- never be searched, because what it claims to hold does not match what the
-- knowledge base actually indexes with, cannot be recorded in the first
-- place — the same reason rag_corpora_backend_metric already existed for S3
-- Vectors and inner-product.
ALTER TABLE rag_corpora DROP CONSTRAINT IF EXISTS rag_corpora_backend_metric;
ALTER TABLE rag_corpora ADD CONSTRAINT rag_corpora_backend_metric CHECK (
    (backend <> 's3vectors' OR embedding_metric <> 'inner_product')
    AND (backend <> 'bedrock-kb' OR (
        embedding_provider = 'bedrock'
        AND embedding_model = 'amazon.titan-embed-text-v2:0'
        AND embedding_dimensions = 1024
        AND embedding_metric = 'cosine'
    ))
);

-- original_bucket and original_key locate a document's own bytes in S3, for a
-- document whose text was never extracted — a PDF, an Office file — because
-- indexing it means handing those bytes to a knowledge base that parses them
-- itself, not chunking text rag never has. Both empty is the ordinary case, a
-- document indexed from the text already kept in body; a knowledge-sourced
-- document with neither text nor an original is refused before it reaches
-- this table (see application/knowledge_events.go), so a row always has one
-- or the other.
ALTER TABLE rag_documents ADD COLUMN IF NOT EXISTS original_bucket text NOT NULL DEFAULT '';
ALTER TABLE rag_documents ADD COLUMN IF NOT EXISTS original_key text NOT NULL DEFAULT '';

ALTER TABLE rag_documents DROP CONSTRAINT IF EXISTS rag_documents_original_pair;
ALTER TABLE rag_documents ADD CONSTRAINT rag_documents_original_pair
    CHECK ((original_bucket = '') = (original_key = ''));

ALTER TABLE rag_documents DROP CONSTRAINT IF EXISTS rag_documents_body_or_original;
ALTER TABLE rag_documents ADD CONSTRAINT rag_documents_body_or_original
    CHECK (body <> '' OR original_bucket <> '');

-- kb_synced_hash is the content_sha256 that was last handed to the knowledge
-- base for this document. Ingestion there is asynchronous — the knowledge
-- base takes seconds to report a document indexed — so the ingest worker
-- claims this document many times before that happens, exactly as it
-- revisits any other document mid-attempt using the same leases. Comparing
-- this column to the current content_sha256 is how a claim knows, without a
-- second place recording it, whether to submit again (never submitted, or
-- the content changed since it last was) or only to poll the knowledge base
-- for the outcome of a submission already in flight — without it, every
-- claim before the knowledge base finishes would resubmit the same content
-- and pay to embed it again.
ALTER TABLE rag_documents ADD COLUMN IF NOT EXISTS kb_synced_hash text NOT NULL DEFAULT '';
