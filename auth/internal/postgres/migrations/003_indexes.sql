-- Indexes that match the queries. Outbox claim orders by occurred_at, id after
-- available_at; 001 used to omit id, so replace that definition once.

CREATE INDEX IF NOT EXISTS auth_memberships_owners_idx
    ON auth_memberships (organization_id)
    WHERE role = 'owner';

CREATE INDEX IF NOT EXISTS auth_invitations_org_created_idx
    ON auth_invitations (organization_id, created_at DESC, id);

CREATE INDEX IF NOT EXISTS auth_invitations_pending_expiry_idx
    ON auth_invitations (organization_id, expires_at)
    WHERE status = 'pending';

DO $$
BEGIN
    IF EXISTS (
        SELECT 1
        FROM pg_indexes
        WHERE schemaname = current_schema()
          AND indexname = 'auth_outbox_pending_idx'
          AND indexdef NOT LIKE '%(available_at, occurred_at, id)%'
    ) THEN
        DROP INDEX auth_outbox_pending_idx;
    END IF;
END $$;

CREATE INDEX IF NOT EXISTS auth_outbox_pending_idx
    ON auth_outbox (available_at, occurred_at, id)
    WHERE published_at IS NULL;
