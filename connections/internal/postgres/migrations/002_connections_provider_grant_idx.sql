-- SharesProviderGrant asks whether another connection of the same app and
-- provider account still holds credentials, so that revoking the grant behind
-- a deleted connection would not cut that one off. It filters on the
-- organization, the app, the provider account and "credentials not deleted
-- yet", and names no status, so none of the partial indexes that carry
-- `status <> 'disconnected'` can serve it: without this index the planner
-- walks every connection the organization has, in every status.
--
-- The question is asked on the serialized path, inside the provider-grant
-- lock, once per credential deletion and once per refused reconnect, so the
-- scan grows with the organization's whole connection history rather than
-- with the grant being examined.
CREATE INDEX IF NOT EXISTS connections_provider_grant_idx
    ON connections (organization_id, app_id, provider_account_id)
    WHERE credentials_deleted_at IS NULL;
