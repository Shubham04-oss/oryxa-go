CREATE TABLE IF NOT EXISTS integrations_oauth_apps (
    id text PRIMARY KEY,
    organization_id text NOT NULL,
    provider text NOT NULL CHECK (length(provider) BETWEEN 1 AND 80),
    piece_name text NOT NULL CHECK (length(piece_name) BETWEEN 1 AND 200),
    piece_version text NOT NULL CHECK (length(piece_version) BETWEEN 1 AND 80),
    mode text NOT NULL CHECK (mode IN ('byoc', 'managed')),
    client_id text NOT NULL,
    encrypted_secret jsonb NOT NULL,
    scopes text[] NOT NULL,
    status text NOT NULL CHECK (status IN ('active', 'disabled')),
    created_by text NOT NULL,
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    credential_revision bigint NOT NULL CHECK (credential_revision > 0),
    UNIQUE (organization_id, id)
);

CREATE INDEX IF NOT EXISTS integrations_oauth_apps_member_catalog_idx
    ON integrations_oauth_apps (organization_id, provider, id)
    WHERE status = 'active';

CREATE TABLE IF NOT EXISTS integrations_connections (
    id text PRIMARY KEY,
    organization_id text NOT NULL,
    member_id text NOT NULL,
    profile_id text NOT NULL,
    provider text NOT NULL,
    piece_name text NOT NULL,
    piece_version text NOT NULL,
    provider_subject_id text NOT NULL CHECK (provider_subject_id <> ''),
    provider_account_id text NOT NULL DEFAULT '',
    provider_account_name text NOT NULL DEFAULT '',
    provider_tenant_id text NOT NULL DEFAULT '',
    provider_tenant_name text NOT NULL DEFAULT '',
    granted_scopes text[] NOT NULL,
    encrypted_credentials jsonb NOT NULL,
    status text NOT NULL CHECK (status IN ('active', 'reauthorization_required', 'disconnected')),
    version bigint NOT NULL CHECK (version > 0),
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    UNIQUE (organization_id, id),
    FOREIGN KEY (organization_id, profile_id)
        REFERENCES integrations_oauth_apps (organization_id, id)
);

-- V1 intentionally presents one active connection per member and org-enabled
-- app. A disconnected or reauthorization-required historical row does not
-- prevent a new successful consent.
CREATE UNIQUE INDEX IF NOT EXISTS integrations_connections_member_profile_active_unique
    ON integrations_connections (organization_id, member_id, profile_id)
    WHERE status = 'active';

CREATE INDEX IF NOT EXISTS integrations_connections_org_catalog_idx
    ON integrations_connections (organization_id, provider, member_id)
    WHERE status = 'active';

CREATE TABLE IF NOT EXISTS integrations_outbox (
    id text PRIMARY KEY,
    source text NOT NULL,
    detail_type text NOT NULL,
    version integer NOT NULL CHECK (version > 0),
    organization_id text NOT NULL,
    detail jsonb NOT NULL,
    occurred_at timestamptz NOT NULL,
    attempts integer NOT NULL DEFAULT 0,
    available_at timestamptz NOT NULL DEFAULT now(),
    locked_until timestamptz,
    published_at timestamptz,
    last_error text
);

CREATE INDEX IF NOT EXISTS integrations_outbox_pending_idx
    ON integrations_outbox (available_at, occurred_at)
    WHERE published_at IS NULL;

-- This is the cross-service read surface. It deliberately excludes OAuth app
-- secrets, access/refresh tokens, ciphertext, encrypted data keys and KMS
-- context. Production grants should allow service roles to read this view, not
-- the underlying integrations_connections table.
CREATE OR REPLACE VIEW integrations_connection_catalog AS
SELECT
    id AS connection_id,
    organization_id,
    member_id,
    profile_id,
    provider,
    piece_name,
    piece_version,
    provider_subject_id,
    provider_account_id,
    provider_account_name,
    provider_tenant_id,
    provider_tenant_name,
    granted_scopes,
    created_at AS connected_at,
    updated_at
FROM integrations_connections
WHERE status = 'active';

COMMENT ON VIEW integrations_connection_catalog IS
    'Credential-free active integrations for approved cross-service consumption';
