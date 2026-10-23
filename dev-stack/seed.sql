-- The stub's organization and people (test/identities), in the auth tables
-- agents reads directly. The tables are made only if real auth never ran
-- here; the shape is test/cmd/stack's seedAuth.
CREATE TABLE IF NOT EXISTS auth_users (
    id text PRIMARY KEY,
    email text NOT NULL,
    display_name text NOT NULL DEFAULT '',
    avatar_url text NOT NULL DEFAULT '',
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL
);
CREATE UNIQUE INDEX IF NOT EXISTS auth_users_email_unique ON auth_users (lower(email));
CREATE TABLE IF NOT EXISTS auth_organizations (
    id text PRIMARY KEY,
    name text NOT NULL CHECK (length(name) BETWEEN 1 AND 120),
    slug text NOT NULL CHECK (length(slug) BETWEEN 2 AND 63),
    created_by text NOT NULL REFERENCES auth_users(id),
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL
);
CREATE UNIQUE INDEX IF NOT EXISTS auth_organizations_slug_unique ON auth_organizations (lower(slug));
CREATE TABLE IF NOT EXISTS auth_memberships (
    organization_id text NOT NULL REFERENCES auth_organizations(id) ON DELETE CASCADE,
    user_id text NOT NULL REFERENCES auth_users(id) ON DELETE CASCADE,
    role text NOT NULL CHECK (role IN ('owner', 'admin', 'member')),
    joined_at timestamptz NOT NULL,
    PRIMARY KEY (organization_id, user_id)
);

INSERT INTO auth_users (id, email, display_name, created_at, updated_at) VALUES
  ('user-owner', 'owner@sim.oryxa', 'Owner', now(), now()),
  ('user-admin', 'admin@sim.oryxa', 'Org Admin', now(), now()),
  ('user-member', 'member@sim.oryxa', 'Member', now(), now()),
  ('user-outsider', 'outsider@sim.oryxa', 'Outsider', now(), now())
  ON CONFLICT (id) DO NOTHING;
INSERT INTO auth_organizations (id, name, slug, created_by, created_at, updated_at)
  VALUES ('org_sim', 'Simulated Org', 'org-sim', 'user-owner', now(), now())
  ON CONFLICT (id) DO NOTHING;
INSERT INTO auth_memberships (organization_id, user_id, role, joined_at) VALUES
  ('org_sim', 'user-owner', 'owner', now()),
  ('org_sim', 'user-admin', 'admin', now()),
  ('org_sim', 'user-member', 'member', now()),
  ('org_sim', 'user-outsider', 'member', now())
  ON CONFLICT DO NOTHING;
