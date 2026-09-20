-- Agents owned by one member share that member's app connection. Multi-account
-- routing is deliberately deferred: there may be only one connection being
-- established or usable for a member and toolkit at a time. Expired, failed,
-- and removed rows remain as history and do not prevent reconnecting.
CREATE UNIQUE INDEX IF NOT EXISTS apps_connections_one_current_per_member_app_idx
    ON apps_connections (organization_id, member_id, toolkit)
    WHERE status IN ('pending', 'active');
