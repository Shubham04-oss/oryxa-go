-- What billing entitles each organization to, as its entitlements events last
-- said. An organization without a row has no limits.
CREATE TABLE IF NOT EXISTS auth_organization_entitlements (
    organization_id text PRIMARY KEY REFERENCES auth_organizations(id) ON DELETE CASCADE,
    revision bigint NOT NULL,
    member_limit integer CHECK (member_limit IS NULL OR member_limit > 0),
    updated_at timestamptz NOT NULL
);
