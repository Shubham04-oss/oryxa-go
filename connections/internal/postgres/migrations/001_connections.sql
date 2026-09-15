-- Apps are organizations enabling providers. Client secrets and private keys
-- are envelope-encrypted; settings hold what is not secret, such as a GitHub
-- App's ID and slug. A deleted app keeps its row, because revoking its
-- connections' grants still needs its client.
CREATE TABLE IF NOT EXISTS connections_apps (
    id text PRIMARY KEY,
    organization_id text NOT NULL,
    provider text NOT NULL CHECK (length(provider) BETWEEN 1 AND 80),
    credential_type text NOT NULL,
    client_id text NOT NULL DEFAULT '',
    encrypted_secret jsonb,
    settings jsonb NOT NULL DEFAULT '{}'::jsonb,
    provider_definition jsonb,
    scopes text[] NOT NULL DEFAULT '{}',
    status text NOT NULL DEFAULT 'active',
    revision bigint NOT NULL DEFAULT 1 CHECK (revision > 0),
    created_by text NOT NULL,
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    deleted_at timestamptz,
    UNIQUE (organization_id, id),
    CONSTRAINT connections_apps_credential_type_check CHECK (credential_type IN (
        'oauth2', 'oauth2_client_credentials', 'installation', 'api_key', 'bearer', 'basic'
    )),
    CONSTRAINT connections_apps_status_check CHECK (status IN ('active', 'disabled'))
);

CREATE INDEX IF NOT EXISTS connections_apps_org_idx
    ON connections_apps (organization_id, provider, id)
    WHERE deleted_at IS NULL;

-- A connection is one provider account reached from a member, team or
-- organization scope. Credentials are envelope-encrypted and removed once a
-- disconnected connection's holds are released.
CREATE TABLE IF NOT EXISTS connections (
    id text PRIMARY KEY,
    organization_id text NOT NULL,
    app_id text NOT NULL,
    provider text NOT NULL,
    credential_type text NOT NULL,
    scope text NOT NULL,
    team_id text,
    member_id text,
    usage text NOT NULL DEFAULT 'members',
    provider_account_id text NOT NULL CHECK (provider_account_id <> ''),
    provider_account_name text NOT NULL DEFAULT '',
    label text NOT NULL DEFAULT '' CHECK (char_length(label) <= 120),
    is_default boolean NOT NULL DEFAULT false,
    scopes text[] NOT NULL DEFAULT '{}',
    status text NOT NULL DEFAULT 'active',
    status_reason text NOT NULL DEFAULT '',
    status_changed_at timestamptz NOT NULL,
    encrypted_credentials jsonb,
    credentials_deleted_at timestamptz,
    version bigint NOT NULL DEFAULT 1 CHECK (version > 0),
    -- One refresh, mint or credential deletion runs at a time per connection.
    refresh_lease_token text,
    refresh_lease_until timestamptz,
    last_used_at timestamptz,
    last_reported_at timestamptz,
    created_by text NOT NULL,
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    UNIQUE (organization_id, id),
    FOREIGN KEY (organization_id, app_id) REFERENCES connections_apps (organization_id, id),
    CONSTRAINT connections_credential_type_check CHECK (credential_type IN (
        'oauth2', 'oauth2_client_credentials', 'installation', 'api_key', 'bearer', 'basic'
    )),
    CONSTRAINT connections_scope_check CHECK (scope IN ('member', 'team', 'organization')),
    CONSTRAINT connections_placement_check CHECK (
        (scope = 'member' AND member_id IS NOT NULL AND team_id IS NULL)
        OR (scope = 'team' AND team_id IS NOT NULL AND member_id IS NULL)
        OR (scope = 'organization' AND team_id IS NULL AND member_id IS NULL)
    ),
    CONSTRAINT connections_usage_check CHECK (
        usage = 'members' OR (usage = 'admins' AND scope = 'organization')
    ),
    CONSTRAINT connections_client_credentials_check CHECK (
        credential_type <> 'oauth2_client_credentials' OR scope <> 'member'
    ),
    CONSTRAINT connections_status_check CHECK (status IN ('active', 'needs_reconnect', 'disconnected')),
    CONSTRAINT connections_reason_check CHECK (
        (status = 'active' AND status_reason = '')
        OR (status = 'needs_reconnect' AND status_reason IN (
            'refresh_rejected', 'unauthorized', 'account_changed', 'client_changed',
            'installation_removed', 'credential_rejected'
        ))
        OR (status = 'disconnected' AND status_reason IN (
            'disconnected', 'member_left', 'team_deleted', 'organization_deleted', 'app_deleted'
        ))
    ),
    CONSTRAINT connections_credentials_check CHECK (
        (credentials_deleted_at IS NULL
            AND (encrypted_credentials IS NOT NULL OR credential_type = 'oauth2_client_credentials'))
        OR (credentials_deleted_at IS NOT NULL AND status = 'disconnected' AND encrypted_credentials IS NULL)
    ),
    CONSTRAINT connections_default_check CHECK (NOT is_default OR status <> 'disconnected')
);

-- One live connection per provider account in a scope. A scope may still hold
-- several accounts for the same app.
CREATE UNIQUE INDEX IF NOT EXISTS connections_live_account_unique
    ON connections (organization_id, app_id, scope, COALESCE(team_id, ''), COALESCE(member_id, ''), provider_account_id)
    WHERE status <> 'disconnected';

-- At most one default connection per app in a scope.
CREATE UNIQUE INDEX IF NOT EXISTS connections_default_unique
    ON connections (organization_id, app_id, scope, COALESCE(team_id, ''), COALESCE(member_id, ''))
    WHERE is_default;

CREATE INDEX IF NOT EXISTS connections_org_member_idx
    ON connections (organization_id, member_id, app_id)
    WHERE member_id IS NOT NULL AND status <> 'disconnected';

CREATE INDEX IF NOT EXISTS connections_org_team_idx
    ON connections (organization_id, team_id, app_id)
    WHERE team_id IS NOT NULL AND status <> 'disconnected';

CREATE INDEX IF NOT EXISTS connections_org_scope_idx
    ON connections (organization_id, scope, app_id)
    WHERE status <> 'disconnected';

-- Disconnected connections whose credentials are still to be revoked and
-- deleted.
CREATE INDEX IF NOT EXISTS connections_credentials_due_idx
    ON connections (status_changed_at, id)
    WHERE status = 'disconnected' AND credentials_deleted_at IS NULL;

-- Authorization sessions are single-use and keyed by the OAuth state sent to
-- the provider. The callback issues the handle the app completes with. Both
-- are stored only as SHA-256 hashes, and the code the provider returned is
-- encrypted. A completion claims the session before it exchanges the code, so
-- a code is never exchanged twice.
CREATE TABLE IF NOT EXISTS connections_sessions (
    state_hash text PRIMARY KEY CHECK (length(state_hash) = 64),
    handle_hash text UNIQUE CHECK (handle_hash IS NULL OR length(handle_hash) = 64),
    organization_id text NOT NULL,
    member_id text NOT NULL,
    app_id text NOT NULL,
    intent text NOT NULL,
    connection_id text,
    scope text NOT NULL,
    team_id text,
    usage text NOT NULL DEFAULT 'members',
    return_uri text NOT NULL,
    redirect_uri text NOT NULL,
    requested_scopes text[] NOT NULL DEFAULT '{}',
    encrypted_verifier jsonb,
    encrypted_result jsonb,
    provider_error text NOT NULL DEFAULT '',
    created_at timestamptz NOT NULL,
    expires_at timestamptz NOT NULL,
    callback_at timestamptz,
    claimed_at timestamptz,
    completed_at timestamptz,
    FOREIGN KEY (organization_id, app_id) REFERENCES connections_apps (organization_id, id),
    CONSTRAINT connections_sessions_intent_check CHECK (
        (intent = 'connect' AND connection_id IS NULL)
        OR (intent = 'reconnect' AND connection_id IS NOT NULL)
    ),
    CONSTRAINT connections_sessions_scope_check CHECK (scope IN ('member', 'team', 'organization')),
    CONSTRAINT connections_sessions_usage_check CHECK (
        usage = 'members' OR (usage = 'admins' AND scope = 'organization')
    ),
    CONSTRAINT connections_sessions_expiry_check CHECK (expires_at > created_at),
    CONSTRAINT connections_sessions_callback_check CHECK (
        callback_at IS NOT NULL
        OR (handle_hash IS NULL AND encrypted_result IS NULL AND provider_error = '' AND claimed_at IS NULL)
    )
);

CREATE INDEX IF NOT EXISTS connections_sessions_expiry_idx
    ON connections_sessions (expires_at);

CREATE INDEX IF NOT EXISTS connections_sessions_member_idx
    ON connections_sessions (organization_id, member_id, expires_at);

-- Health is kept apart from the connection row, whose version guards refresh,
-- so frequent checks never contend with credential updates.
CREATE TABLE IF NOT EXISTS connections_health (
    organization_id text NOT NULL,
    connection_id text NOT NULL,
    state text NOT NULL DEFAULT 'unknown',
    reason text NOT NULL DEFAULT '',
    consecutive_failures integer NOT NULL DEFAULT 0 CHECK (consecutive_failures >= 0),
    last_checked_at timestamptz,
    last_success_at timestamptz,
    next_check_at timestamptz NOT NULL,
    claim_token text,
    locked_until timestamptz,
    PRIMARY KEY (organization_id, connection_id),
    FOREIGN KEY (organization_id, connection_id) REFERENCES connections (organization_id, id),
    CONSTRAINT connections_health_state_check CHECK (state IN ('unknown', 'healthy', 'degraded'))
);

CREATE INDEX IF NOT EXISTS connections_health_due_idx
    ON connections_health (next_check_at, connection_id);

-- A hold keeps a disconnected connection's credentials while the holder
-- removes remote resources it created with them.
CREATE TABLE IF NOT EXISTS connections_holds (
    organization_id text NOT NULL,
    connection_id text NOT NULL,
    holder text NOT NULL CHECK (length(holder) BETWEEN 1 AND 80),
    created_at timestamptz NOT NULL,
    PRIMARY KEY (organization_id, connection_id, holder),
    FOREIGN KEY (organization_id, connection_id) REFERENCES connections (organization_id, id)
);

CREATE TABLE IF NOT EXISTS connections_outbox (
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

CREATE INDEX IF NOT EXISTS connections_outbox_pending_idx
    ON connections_outbox (available_at, occurred_at, id)
    WHERE published_at IS NULL;
