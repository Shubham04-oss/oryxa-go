-- Corpora, the documents in them, the chunks those become, and the outbox
-- the service publishes from.
--
-- A corpus pins its embedding spec and its chunk policy. Both are written here
-- as columns with checks rather than as a blob, so a corpus that could never
-- be searched cannot be recorded in the first place.

CREATE TABLE IF NOT EXISTS rag_corpora (
    id text PRIMARY KEY,
    organization_id text NOT NULL,
    slug text NOT NULL CHECK (length(slug) BETWEEN 2 AND 63),
    name text NOT NULL CHECK (length(name) BETWEEN 1 AND 120),
    scope text NOT NULL CHECK (scope IN ('member', 'team', 'organization')),
    team_id text,
    member_id text,
    embedding_provider text NOT NULL CHECK (embedding_provider IN ('bedrock', 'vertex')),
    embedding_model text NOT NULL,
    embedding_dimensions integer NOT NULL CHECK (embedding_dimensions BETWEEN 1 AND 4096),
    embedding_metric text NOT NULL CHECK (embedding_metric IN ('cosine', 'l2', 'inner_product')),
    backend text NOT NULL CHECK (backend IN ('pgvector', 's3vectors')),
    chunk_strategy text NOT NULL CHECK (chunk_strategy IN ('structure', 'fixed')),
    chunk_max_tokens integer NOT NULL CHECK (chunk_max_tokens BETWEEN 32 AND 2048),
    chunk_overlap_tokens integer NOT NULL CHECK (chunk_overlap_tokens >= 0),
    metadata jsonb NOT NULL DEFAULT '{}'::jsonb,
    created_by text NOT NULL,
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    -- Placement mirrors files: a corpus hangs on a member, a team or the
    -- organization, and exactly one of those.
    CONSTRAINT rag_corpora_placement CHECK (
        (scope = 'member' AND member_id IS NOT NULL AND team_id IS NULL)
        OR (scope = 'team' AND team_id IS NOT NULL AND member_id IS NULL)
        OR (scope = 'organization' AND team_id IS NULL AND member_id IS NULL)
    ),
    -- S3 Vectors indexes on cosine or euclidean only. Without this a corpus
    -- could be created that accepts documents and can never be queried.
    CONSTRAINT rag_corpora_backend_metric CHECK (
        backend <> 's3vectors' OR embedding_metric <> 'inner_product'
    ),
    -- Overlap at or above half the budget makes each chunk mostly a copy of
    -- the one before it: the index grows without holding more meaning.
    CONSTRAINT rag_corpora_overlap CHECK (chunk_overlap_tokens * 2 < chunk_max_tokens)
);

CREATE UNIQUE INDEX IF NOT EXISTS rag_corpora_slug_unique
    ON rag_corpora (organization_id, lower(slug));

CREATE INDEX IF NOT EXISTS rag_corpora_team_idx
    ON rag_corpora (organization_id, team_id) WHERE scope = 'team';

CREATE INDEX IF NOT EXISTS rag_corpora_member_idx
    ON rag_corpora (organization_id, member_id) WHERE scope = 'member';

-- The document keeps the text it was chunked from. That is what makes a
-- citation checkable: a chunk's byte range addresses this column, so a
-- passage can be shown in its surroundings rather than taken on trust.
CREATE TABLE IF NOT EXISTS rag_documents (
    id text PRIMARY KEY,
    corpus_id text NOT NULL REFERENCES rag_corpora(id) ON DELETE CASCADE,
    organization_id text NOT NULL,
    source_kind text NOT NULL CHECK (source_kind IN ('inline', 'file', 'external')),
    source_ref text NOT NULL DEFAULT '',
    title text NOT NULL DEFAULT '',
    content_type text NOT NULL,
    content_sha256 text NOT NULL,
    byte_size bigint NOT NULL CHECK (byte_size >= 0),
    body text NOT NULL,
    metadata jsonb NOT NULL DEFAULT '{}'::jsonb,
    status text NOT NULL CHECK (status IN ('pending', 'ingesting', 'ready', 'failed')),
    failure_reason text NOT NULL DEFAULT '',
    chunk_count integer NOT NULL DEFAULT 0,
    attempts integer NOT NULL DEFAULT 0,
    locked_until timestamptz,
    created_by text NOT NULL,
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    ingested_at timestamptz
);

-- One document per source. Re-delivering a file or a URL updates that
-- document rather than adding a second copy of it to the index, which is what
-- lets an upstream pipeline re-run without duplicating every answer.
CREATE UNIQUE INDEX IF NOT EXISTS rag_documents_source_unique
    ON rag_documents (corpus_id, source_kind, source_ref)
    WHERE source_ref <> '';

-- Inline text has no reference to be identified by, so its content is its
-- identity.
CREATE UNIQUE INDEX IF NOT EXISTS rag_documents_inline_unique
    ON rag_documents (corpus_id, content_sha256)
    WHERE source_kind = 'inline';

CREATE INDEX IF NOT EXISTS rag_documents_corpus_idx
    ON rag_documents (corpus_id, created_at DESC, id);

-- Work waiting to be ingested, ordered oldest first.
CREATE INDEX IF NOT EXISTS rag_documents_pending_idx
    ON rag_documents (created_at, id)
    WHERE status IN ('pending', 'ingesting');

CREATE TABLE IF NOT EXISTS rag_chunks (
    id text PRIMARY KEY,
    document_id text NOT NULL REFERENCES rag_documents(id) ON DELETE CASCADE,
    corpus_id text NOT NULL REFERENCES rag_corpora(id) ON DELETE CASCADE,
    organization_id text NOT NULL,
    ordinal integer NOT NULL CHECK (ordinal >= 0),
    body text NOT NULL,
    token_count integer NOT NULL CHECK (token_count >= 0),
    heading_path text[] NOT NULL DEFAULT '{}',
    -- The same trail as one string. It is stored rather than derived because
    -- a generated column has to be immutable and array_to_string is not, and
    -- it is the exact string the chunk was embedded and cited under.
    heading_trail text NOT NULL DEFAULT '',
    start_byte integer NOT NULL CHECK (start_byte >= 0),
    end_byte integer NOT NULL CHECK (end_byte > start_byte),
    sha256 text NOT NULL,
    metadata jsonb NOT NULL DEFAULT '{}'::jsonb,
    created_at timestamptz NOT NULL,
    -- Deferrable because re-cutting a document moves chunks between ordinals,
    -- and an ordinal being vacated by one row and taken by another inside the
    -- same transaction is correct. Checking it per statement would reject the
    -- rewrite half way through; checking it at commit asks the question that
    -- actually matters.
    CONSTRAINT rag_chunks_ordinal_unique UNIQUE (document_id, ordinal) DEFERRABLE INITIALLY IMMEDIATE,
    -- The lexical half of hybrid retrieval. The heading trail is indexed with
    -- the body for the same reason it is embedded with it: a passage is about
    -- its headings as much as about its own words.
    --
    -- 'english' stems and drops stopwords, which is what makes a naturally
    -- worded question match a differently worded passage. A corpus that is
    -- mostly another language wants a different configuration here; that is a
    -- migration, not a setting, because the column is stored.
    search_vector tsvector GENERATED ALWAYS AS (
        to_tsvector('english', heading_trail || ' ' || body)
    ) STORED
);

CREATE INDEX IF NOT EXISTS rag_chunks_corpus_idx
    ON rag_chunks (corpus_id, organization_id);

CREATE INDEX IF NOT EXISTS rag_chunks_search_idx
    ON rag_chunks USING gin (search_vector);

-- Unchanged chunks keep their vectors across a re-ingest, which is looked up
-- by this hash. On a document where one paragraph changed it is the
-- difference between re-embedding one chunk and re-embedding all of them.
CREATE INDEX IF NOT EXISTS rag_chunks_hash_idx
    ON rag_chunks (corpus_id, sha256);

CREATE TABLE IF NOT EXISTS rag_outbox (
    id text PRIMARY KEY,
    detail_type text NOT NULL,
    detail jsonb NOT NULL,
    occurred_at timestamptz NOT NULL,
    available_at timestamptz NOT NULL DEFAULT now(),
    locked_until timestamptz,
    published_at timestamptz,
    attempts integer NOT NULL DEFAULT 0,
    last_error text
);

CREATE INDEX IF NOT EXISTS rag_outbox_pending_idx
    ON rag_outbox (available_at, occurred_at, id)
    WHERE published_at IS NULL;

CREATE TABLE IF NOT EXISTS rag_organizations (
    organization_id text PRIMARY KEY,
    name text NOT NULL DEFAULT '',
    updated_at timestamptz NOT NULL
);
