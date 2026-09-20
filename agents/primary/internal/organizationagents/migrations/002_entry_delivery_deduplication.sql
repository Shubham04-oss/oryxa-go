-- EventBridge can redeliver the same Teamspace entry under a new event ID.
-- Keep the oldest delivery and make the durable entry identity idempotent too,
-- so an organization synthesis advances exactly once per shared publication.

DELETE FROM agents_primary.organization_agent_deliveries duplicate
USING agents_primary.organization_agent_deliveries keeper
WHERE duplicate.organization_id = keeper.organization_id
  AND duplicate.entry_id = keeper.entry_id
  AND (duplicate.recorded_at, duplicate.event_id) > (keeper.recorded_at, keeper.event_id);

CREATE UNIQUE INDEX IF NOT EXISTS organization_agent_deliveries_entry_idx
    ON agents_primary.organization_agent_deliveries (organization_id, entry_id);
