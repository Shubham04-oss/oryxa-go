CREATE TABLE IF NOT EXISTS auth_users (
    id text PRIMARY KEY,
    email text NOT NULL,
    display_name text NOT NULL DEFAULT '',
    avatar_url text NOT NULL DEFAULT '',
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL
);

CREATE UNIQUE INDEX IF NOT EXISTS auth_users_email_unique
    ON auth_users (lower(email));

CREATE TABLE IF NOT EXISTS auth_organizations (
    id text PRIMARY KEY,
    name text NOT NULL CHECK (length(name) BETWEEN 1 AND 120),
    slug text NOT NULL CHECK (length(slug) BETWEEN 2 AND 63),
    created_by text NOT NULL REFERENCES auth_users(id),
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL
);

CREATE UNIQUE INDEX IF NOT EXISTS auth_organizations_slug_unique
    ON auth_organizations (lower(slug));

CREATE TABLE IF NOT EXISTS auth_memberships (
    organization_id text NOT NULL REFERENCES auth_organizations(id) ON DELETE CASCADE,
    user_id text NOT NULL REFERENCES auth_users(id) ON DELETE CASCADE,
    role text NOT NULL CHECK (role IN ('owner', 'admin', 'member')),
    joined_at timestamptz NOT NULL,
    PRIMARY KEY (organization_id, user_id)
);

CREATE INDEX IF NOT EXISTS auth_memberships_user_idx
    ON auth_memberships (user_id, organization_id);

CREATE TABLE IF NOT EXISTS auth_invitations (
    id text PRIMARY KEY,
    organization_id text NOT NULL REFERENCES auth_organizations(id) ON DELETE CASCADE,
    email text NOT NULL,
    role text NOT NULL CHECK (role IN ('admin', 'member')),
    token_hash text NOT NULL UNIQUE,
    invited_by text NOT NULL REFERENCES auth_users(id),
    status text NOT NULL CHECK (status IN ('pending', 'accepted', 'revoked', 'expired')),
    expires_at timestamptz NOT NULL,
    created_at timestamptz NOT NULL,
    accepted_at timestamptz,
    accepted_by text REFERENCES auth_users(id)
);

CREATE UNIQUE INDEX IF NOT EXISTS auth_invitations_pending_unique
    ON auth_invitations (organization_id, lower(email))
    WHERE status = 'pending';

CREATE TABLE IF NOT EXISTS auth_login_flows (
    state_hash text PRIMARY KEY,
    code_verifier text NOT NULL,
    nonce text NOT NULL,
    return_to text NOT NULL,
    expires_at timestamptz NOT NULL
);

CREATE INDEX IF NOT EXISTS auth_login_flows_expiry_idx
    ON auth_login_flows (expires_at);

CREATE TABLE IF NOT EXISTS auth_action_tokens (
    id text PRIMARY KEY,
    user_id text NOT NULL,
    email text NOT NULL,
    purpose text NOT NULL CHECK (purpose IN ('email_verification', 'password_reset')),
    token_hash text NOT NULL UNIQUE,
    status text NOT NULL CHECK (status IN ('pending', 'processing', 'completed', 'expired')),
    expires_at timestamptz NOT NULL,
    created_at timestamptz NOT NULL,
    processing_until timestamptz,
    completed_at timestamptz
);

CREATE UNIQUE INDEX IF NOT EXISTS auth_action_tokens_pending_unique
    ON auth_action_tokens (lower(email), purpose)
    WHERE status IN ('pending', 'processing');

CREATE INDEX IF NOT EXISTS auth_action_tokens_expiry_idx
    ON auth_action_tokens (expires_at)
    WHERE status = 'pending';

CREATE TABLE IF NOT EXISTS auth_outbox (
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

CREATE INDEX IF NOT EXISTS auth_outbox_pending_idx
    ON auth_outbox (available_at, occurred_at)
    WHERE published_at IS NULL;
