-- The endpoints apps' own integrations receive webhooks on: one URL each,
-- POST /v1/apps/hooks/{id}. Every delivery is verified by the endpoint's
-- scheme before anything in it is used, and deduped in apps_handled_events
-- under hook:{endpoint}:{delivery}, which maintenance forgets after 7 days.

CREATE TABLE IF NOT EXISTS apps_hook_endpoints (
    -- 120 random bits, and the only thing in the endpoint's URL.
    id text PRIMARY KEY,
    toolkit text NOT NULL,
    -- Whose endpoint it is: one subscription made with a connection, an
    -- organization's own app, or the deployment's app for everyone.
    scope text NOT NULL CHECK (scope IN ('connection', 'organization', 'deployment')),
    organization_id text NOT NULL DEFAULT '',
    connection_id text REFERENCES apps_connections (id),
    -- The manifest's webhook section when the endpoint was made. It verifies
    -- the endpoint's deliveries, so a later manifest cannot change how.
    scheme jsonb NOT NULL,
    -- internal/secrets envelopes, bound to service oryxa-apps, resource type
    -- hook_endpoint and the endpoint's id. A deployment endpoint's secret is
    -- in configuration instead, unless its app sent one to be captured.
    sealed_secret jsonb,
    secret_version integer NOT NULL DEFAULT 0,
    -- A rotated-out secret, which still verifies until previous_secret_until.
    previous_sealed_secret jsonb,
    previous_secret_until timestamptz,
    -- When a captured verification token was shown to an admin, once.
    secret_revealed_at timestamptz,
    -- The app's own id for the subscription delivering here.
    remote_id text NOT NULL DEFAULT '',
    -- When the app's subscription lapses, if it does; the endpoint answers
    -- nothing from then on.
    expires_at timestamptz,
    status text NOT NULL CHECK (status IN ('active', 'replaced')),
    -- The endpoint a renewal made to take over from this one.
    replaced_by text REFERENCES apps_hook_endpoints (id) ON DELETE SET NULL,
    -- When the app first proved the endpoint with a handshake.
    validated_at timestamptz,
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    CONSTRAINT apps_hook_endpoints_owner_check CHECK (
        (scope = 'connection' AND connection_id IS NOT NULL AND organization_id <> '')
        OR (scope = 'organization' AND connection_id IS NULL AND organization_id <> '')
        OR (scope = 'deployment' AND connection_id IS NULL AND organization_id = '')
    )
);

-- One deployment endpoint per toolkit: its URL is in the app's own settings.
CREATE UNIQUE INDEX IF NOT EXISTS apps_hook_endpoints_deployment_idx
    ON apps_hook_endpoints (toolkit)
    WHERE scope = 'deployment' AND status = 'active';

-- One endpoint per organization's own app.
CREATE UNIQUE INDEX IF NOT EXISTS apps_hook_endpoints_organization_idx
    ON apps_hook_endpoints (organization_id, toolkit)
    WHERE scope = 'organization' AND status = 'active';

CREATE INDEX IF NOT EXISTS apps_hook_endpoints_connection_idx
    ON apps_hook_endpoints (connection_id)
    WHERE connection_id IS NOT NULL;

-- What renewal and the sweep look for.
CREATE INDEX IF NOT EXISTS apps_hook_endpoints_expires_idx
    ON apps_hook_endpoints (expires_at)
    WHERE expires_at IS NOT NULL;

-- Deleting a replacement finds the endpoint it replaced.
CREATE INDEX IF NOT EXISTS apps_hook_endpoints_replaced_by_idx
    ON apps_hook_endpoints (replaced_by)
    WHERE replaced_by IS NOT NULL;
