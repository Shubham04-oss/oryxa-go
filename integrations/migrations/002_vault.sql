-- The vault: the org's apps, the connections made through them, their
-- sealed credentials, and the sign-ins in flight (design §6). Every sealed
-- column holds an internal/secrets envelope as JSON, bound to service
-- integrations, the table, and the org and row it sits in (see
-- integrations/internal/vault), so a copy in another org or table won't open.

-- An OAuth or key-based app per org, or Oryxa's own where org_id IS NULL.
CREATE TABLE IF NOT EXISTS integrations.org_apps (
    id text PRIMARY KEY,
    org_id text,
    app text NOT NULL,
    kind text NOT NULL CHECK (kind IN ('oauth_client', 'github_app', 'slack_app', 'api_key')),
    client_id text NOT NULL DEFAULT '',
    secret jsonb,
    webhook_secret jsonb,
    -- GitHub App id, tenant id and the like: nothing secret.
    extra jsonb NOT NULL DEFAULT '{}'::jsonb,
    expires_at timestamptz,
    status text NOT NULL CHECK (status IN ('ok', 'expiring', 'broken', 'retired')),
    created_by text NOT NULL DEFAULT '',
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL
);

-- One live row per (org, app); a replaced row stays for the connections
-- pinned to it. Oryxa's own app (org_id NULL) is one per app too.
CREATE UNIQUE INDEX IF NOT EXISTS org_apps_live_org_idx
    ON integrations.org_apps (org_id, app)
    WHERE status <> 'retired' AND org_id IS NOT NULL;
CREATE UNIQUE INDEX IF NOT EXISTS org_apps_live_oryxa_idx
    ON integrations.org_apps (app)
    WHERE status <> 'retired' AND org_id IS NULL;

-- One member's (or the org's) connection to an app.
CREATE TABLE IF NOT EXISTS integrations.connections (
    id text PRIMARY KEY,
    org_id text NOT NULL,
    -- NULL for an org-level connection.
    member_id text,
    level text NOT NULL CHECK (level IN ('personal', 'org')),
    app text NOT NULL,
    org_app_id text NOT NULL REFERENCES integrations.org_apps (id),
    mode text NOT NULL CHECK (mode IN ('oryxa', 'byo', 'user_key')),
    -- The app's account id and name.
    account jsonb NOT NULL DEFAULT '{}'::jsonb,
    scopes text[] NOT NULL DEFAULT '{}',
    status text NOT NULL CHECK (status IN ('ok', 'needs_reauth', 'degraded', 'error', 'revoked')),
    failures integer NOT NULL DEFAULT 0,
    cooldown_until timestamptz,
    last_ok_at timestamptz,
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    CHECK ((level = 'org') = (member_id IS NULL))
);

CREATE INDEX IF NOT EXISTS connections_org_idx ON integrations.connections (org_id, app);

-- Sealed tokens. version moves on with every refresh.
CREATE TABLE IF NOT EXISTS integrations.credentials (
    connection_id text NOT NULL REFERENCES integrations.connections (id),
    kind text NOT NULL CHECK (kind IN ('access', 'refresh', 'api_key', 'installation')),
    sealed jsonb NOT NULL,
    expires_at timestamptz,
    version bigint NOT NULL DEFAULT 1,
    PRIMARY KEY (connection_id, kind)
);

-- Sign-ins in flight. Only the hash of state is kept, so a read of this
-- table can't finish someone's sign-in; used_at makes it single-use.
CREATE TABLE IF NOT EXISTS integrations.oauth_states (
    state_hash text PRIMARY KEY,
    org_id text NOT NULL,
    member_id text NOT NULL,
    app text NOT NULL,
    org_app_id text NOT NULL REFERENCES integrations.org_apps (id),
    pkce_verifier jsonb,
    return_to text NOT NULL DEFAULT '',
    expires_at timestamptz NOT NULL,
    used_at timestamptz
);
