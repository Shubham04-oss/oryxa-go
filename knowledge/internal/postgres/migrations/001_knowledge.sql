-- Sources are what an organization asked to be indexed: a repository, a
-- workspace, a channel, its own files. The member who registered one owns it,
-- and extraction reads as them. A source keeps its row after it is removed
-- until its documents have been retracted.
CREATE TABLE IF NOT EXISTS knowledge_sources (
    id text PRIMARY KEY,
    organization_id text NOT NULL,
    -- connection_id and app_id are empty for an internal source, such as the
    -- organization's own files.
    connection_id text NOT NULL DEFAULT '',
    app_id text NOT NULL DEFAULT '',
    provider text NOT NULL CHECK (length(provider) BETWEEN 1 AND 80),
    target jsonb NOT NULL DEFAULT '{}'::jsonb,
    scope text NOT NULL CHECK (scope IN ('member', 'team', 'organization')),
    team_id text,
    member_id text,
    created_by text NOT NULL,
    status text NOT NULL CHECK (status IN ('active', 'paused', 'removed')),
    reason text NOT NULL DEFAULT '',
    interval_seconds integer NOT NULL CHECK (interval_seconds > 0),
    -- sync_at is when the next run is due; NULL once nothing is scheduled.
    sync_at timestamptz,
    last_synced_at timestamptz,
    attempts integer NOT NULL DEFAULT 0,
    lease_token text,
    lease_until timestamptz,
    version bigint NOT NULL DEFAULT 1 CHECK (version > 0),
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    UNIQUE (organization_id, id),
    CONSTRAINT knowledge_sources_placement_check CHECK (
        (scope = 'member' AND member_id IS NOT NULL AND team_id IS NULL)
        OR (scope = 'team' AND team_id IS NOT NULL AND member_id IS NULL)
        OR (scope = 'organization' AND team_id IS NULL AND member_id IS NULL)
    )
);

-- The same thing is indexed once. A removed source leaves the name free.
CREATE UNIQUE INDEX IF NOT EXISTS knowledge_sources_live_target_unique
    ON knowledge_sources (organization_id, provider, connection_id, target)
    WHERE status <> 'removed';

CREATE INDEX IF NOT EXISTS knowledge_sources_organization_idx
    ON knowledge_sources (organization_id, status, created_at DESC, id);

CREATE INDEX IF NOT EXISTS knowledge_sources_connection_idx
    ON knowledge_sources (connection_id)
    WHERE status <> 'removed' AND connection_id <> '';

CREATE INDEX IF NOT EXISTS knowledge_sources_app_idx
    ON knowledge_sources (app_id)
    WHERE status <> 'removed' AND app_id <> '';

CREATE INDEX IF NOT EXISTS knowledge_sources_creator_idx
    ON knowledge_sources (organization_id, created_by)
    WHERE status <> 'removed';

-- Due sources are claimed in sync_at order under a lease.
CREATE INDEX IF NOT EXISTS knowledge_sources_due_idx
    ON knowledge_sources (sync_at, id)
    WHERE status = 'active' AND sync_at IS NOT NULL;

-- Documents are what a source holds, normalized. content_hash over the body
-- is the version: an unchanged document is never published twice.
CREATE TABLE IF NOT EXISTS knowledge_documents (
    id text PRIMARY KEY,
    organization_id text NOT NULL,
    source_id text NOT NULL REFERENCES knowledge_sources (id) ON DELETE CASCADE,
    external_id text NOT NULL CHECK (length(external_id) BETWEEN 1 AND 400),
    kind text NOT NULL,
    title text NOT NULL DEFAULT '',
    url text NOT NULL DEFAULT '',
    mime text NOT NULL,
    content_hash text NOT NULL CHECK (length(content_hash) = 64),
    body text NOT NULL DEFAULT '',
    byte_size integer NOT NULL DEFAULT 0 CHECK (byte_size >= 0),
    metadata jsonb NOT NULL DEFAULT '{}'::jsonb,
    source_updated_at timestamptz,
    extracted_at timestamptz NOT NULL,
    -- last_seen_at is when a run last read this document at its source. A
    -- document a full pass did not see is one the source no longer holds.
    last_seen_at timestamptz NOT NULL,
    -- removed_at marks a document its source no longer holds. The row stays
    -- so a document that comes back is recognised as the same one.
    removed_at timestamptz,
    UNIQUE (source_id, external_id)
);

CREATE INDEX IF NOT EXISTS knowledge_documents_unseen_idx
    ON knowledge_documents (source_id, last_seen_at)
    WHERE removed_at IS NULL;

CREATE INDEX IF NOT EXISTS knowledge_documents_source_idx
    ON knowledge_documents (source_id, extracted_at DESC, id)
    WHERE removed_at IS NULL;

CREATE INDEX IF NOT EXISTS knowledge_documents_organization_idx
    ON knowledge_documents (organization_id, id);

-- A run that ends early leaves its cursor, so the next one carries on rather
-- than reading the whole source again.
CREATE TABLE IF NOT EXISTS knowledge_cursors (
    source_id text PRIMARY KEY REFERENCES knowledge_sources (id) ON DELETE CASCADE,
    value text NOT NULL DEFAULT '',
    watermark timestamptz,
    updated_at timestamptz NOT NULL
);

-- Runs are what each extraction pass did, kept so a source's history explains
-- what it holds.
CREATE TABLE IF NOT EXISTS knowledge_sync_runs (
    id text PRIMARY KEY,
    organization_id text NOT NULL,
    source_id text NOT NULL REFERENCES knowledge_sources (id) ON DELETE CASCADE,
    kind text NOT NULL CHECK (kind IN ('backfill', 'incremental')),
    status text NOT NULL CHECK (status IN ('running', 'completed', 'failed')),
    documents_seen integer NOT NULL DEFAULT 0,
    documents_published integer NOT NULL DEFAULT 0,
    documents_removed integer NOT NULL DEFAULT 0,
    error text NOT NULL DEFAULT '',
    retryable boolean NOT NULL DEFAULT false,
    started_at timestamptz NOT NULL,
    finished_at timestamptz
);

CREATE INDEX IF NOT EXISTS knowledge_sync_runs_source_idx
    ON knowledge_sync_runs (source_id, started_at DESC, id);

CREATE INDEX IF NOT EXISTS knowledge_sync_runs_started_idx
    ON knowledge_sync_runs (started_at);

-- The bus delivers at least once, and pausing a source or re-reading a
-- document is not idempotent on its own. Events handled are remembered, and
-- rows older than a week are removed.
CREATE TABLE IF NOT EXISTS knowledge_handled_events (
    event_id text PRIMARY KEY,
    detail_type text NOT NULL,
    handled_at timestamptz NOT NULL
);

CREATE INDEX IF NOT EXISTS knowledge_handled_events_handled_idx
    ON knowledge_handled_events (handled_at);

CREATE TABLE IF NOT EXISTS knowledge_outbox (
    id text PRIMARY KEY,
    detail_type text NOT NULL,
    detail jsonb NOT NULL,
    occurred_at timestamptz NOT NULL,
    attempts integer NOT NULL DEFAULT 0,
    available_at timestamptz NOT NULL DEFAULT now(),
    locked_until timestamptz,
    published_at timestamptz,
    last_error text
);

CREATE INDEX IF NOT EXISTS knowledge_outbox_pending_idx
    ON knowledge_outbox (available_at, occurred_at, id)
    WHERE published_at IS NULL;
