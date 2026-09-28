-- Oryxa's own sign-in, beside Composio's. A connection is made through one
-- broker: composio, which holds the credential itself, or oryxa, whose
-- credential apps keeps here, sealed. Every sealed column holds
-- internal/secrets envelopes bound to service oryxa-apps, the resource type
-- and the resource id, one envelope per value.

ALTER TABLE apps_connections
    ADD COLUMN IF NOT EXISTS broker text NOT NULL DEFAULT 'composio'
        CHECK (broker IN ('composio', 'oryxa'));

-- The OAuth clients members sign in through: an organization's own, or
-- Oryxa's default where organization_id is ''. client is the registration,
-- shared across toolkits (every Google toolkit signs in through google).
-- A replaced or removed client stays, so the connections made through it
-- still refresh and revoke with it.
CREATE TABLE IF NOT EXISTS apps_oauth_clients (
    id text PRIMARY KEY,
    organization_id text NOT NULL,
    client text NOT NULL,
    -- '' on a row that only refuses Oryxa's default and brings no client.
    client_id text NOT NULL,
    sealed_secret jsonb,
    -- The client-level fields that are not secret, and the switch that
    -- refuses Oryxa's default.
    settings jsonb NOT NULL DEFAULT '{}'::jsonb,
    -- The secret client-level fields, by name.
    sealed_fields jsonb NOT NULL DEFAULT '{}'::jsonb,
    status text NOT NULL CHECK (status IN ('active', 'removed')),
    created_by text NOT NULL DEFAULT '',
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL
);

-- One live client per organization and registration.
CREATE UNIQUE INDEX IF NOT EXISTS apps_oauth_clients_live_idx
    ON apps_oauth_clients (organization_id, client)
    WHERE status = 'active';

-- A connection's credential. kind is checked against the manifest in Go,
-- where the core adds kinds. version moves on with every refresh and every
-- replacement, and a refresh is saved only by compare-and-swap on it, under
-- the lease. Once the connection has ended, next_check_at and
-- check_failures schedule the retries of a revocation the app did not
-- answer.
CREATE TABLE IF NOT EXISTS apps_credentials (
    connection_id text PRIMARY KEY REFERENCES apps_connections (id),
    kind text NOT NULL,
    oauth_client_id text REFERENCES apps_oauth_clients (id),
    -- Each secret value on its own, by name: the OAuth tokens, a kind's
    -- secret standard fields, and the method's secret fields.
    sealed jsonb NOT NULL,
    -- The values that are not secret, and the account's label and ref.
    settings jsonb NOT NULL DEFAULT '{}'::jsonb,
    scopes text[] NOT NULL DEFAULT '{}',
    expires_at timestamptz,
    version bigint NOT NULL,
    lease_token text,
    lease_until timestamptz,
    next_check_at timestamptz NOT NULL,
    check_failures integer NOT NULL DEFAULT 0,
    updated_at timestamptz NOT NULL
);

CREATE INDEX IF NOT EXISTS apps_credentials_next_check_idx
    ON apps_credentials (next_check_at);

-- Which credentials came through a client, for the grants several
-- connections share.
CREATE INDEX IF NOT EXISTS apps_credentials_oauth_client_idx
    ON apps_credentials (oauth_client_id)
    WHERE oauth_client_id IS NOT NULL;

-- An OAuth sign-in in flight: ten minutes, and spent once. Only the state's
-- SHA-256 is kept. toolkit, oauth_client_id, settings and sealed carry what
-- the member typed and the client the authorization request named across to
-- the callback, so the code is exchanged by the client it was issued to.
CREATE TABLE IF NOT EXISTS apps_signin_flows (
    state_hash text PRIMARY KEY,
    organization_id text NOT NULL,
    member_id text NOT NULL,
    connection_id text NOT NULL REFERENCES apps_connections (id),
    toolkit text NOT NULL,
    oauth_client_id text NOT NULL REFERENCES apps_oauth_clients (id),
    -- '' when the manifest turns PKCE off.
    code_verifier text NOT NULL DEFAULT '',
    redirect_uri text NOT NULL,
    return_to text NOT NULL DEFAULT '',
    settings jsonb NOT NULL DEFAULT '{}'::jsonb,
    sealed jsonb NOT NULL DEFAULT '{}'::jsonb,
    expires_at timestamptz NOT NULL,
    consumed_at timestamptz
);

CREATE INDEX IF NOT EXISTS apps_signin_flows_expires_idx
    ON apps_signin_flows (expires_at);
