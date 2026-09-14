CREATE TABLE IF NOT EXISTS files_organizations (
    organization_id text PRIMARY KEY,
    name text NOT NULL DEFAULT '',
    name_updated_at timestamptz,
    deleted_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS files_objects (
    id text PRIMARY KEY,
    organization_id text NOT NULL REFERENCES files_organizations(organization_id),
    scope text NOT NULL DEFAULT 'member',
    team_id text,
    member_id text,
    filename text NOT NULL CHECK (length(filename) BETWEEN 1 AND 255),
    content_type text NOT NULL,
    purpose text NOT NULL,
    size_bytes bigint NOT NULL CHECK (size_bytes > 0),
    sha256 text NOT NULL CHECK (length(sha256) = 64),
    created_by text NOT NULL,
    created_at timestamptz NOT NULL,
    expires_at timestamptz NOT NULL,
    CONSTRAINT files_objects_purpose_check CHECK (purpose IN (
        'upload',
        'integrations.piece',
        'integrations.webhook-payload',
        'integrations.attachment',
        'pipeline.artifact',
        'billing.attachment'
    )),
    CONSTRAINT files_objects_scope_check CHECK (scope IN ('member', 'team', 'organization')),
    CONSTRAINT files_objects_placement_check CHECK (
        (scope = 'member' AND member_id IS NOT NULL AND team_id IS NULL)
        OR (scope = 'team' AND team_id IS NOT NULL AND member_id IS NULL)
        OR (scope = 'organization' AND team_id IS NULL AND member_id IS NULL)
    )
);

CREATE INDEX IF NOT EXISTS files_objects_org_created_idx
    ON files_objects (organization_id, created_at DESC, id);

CREATE INDEX IF NOT EXISTS files_objects_org_creator_idx
    ON files_objects (organization_id, created_by, created_at DESC, id);

CREATE INDEX IF NOT EXISTS files_objects_expiry_idx
    ON files_objects (expires_at, id);

CREATE TABLE IF NOT EXISTS files_outbox (
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

CREATE INDEX IF NOT EXISTS files_outbox_pending_idx
    ON files_outbox (available_at, occurred_at, id)
    WHERE published_at IS NULL;
