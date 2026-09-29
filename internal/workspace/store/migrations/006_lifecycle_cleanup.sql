-- Event IDs are opaque deduplication receipts, never copies of event payloads.
CREATE TABLE workspace.lifecycle_receipts (
    event_id text PRIMARY KEY,
    organization_id text NOT NULL,
    handled_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX workspace_lifecycle_receipts_age ON workspace.lifecycle_receipts (handled_at);

-- Retain only the deleted organization's opaque ID to prevent an in-flight
-- authenticated request from recreating its documents after cleanup.
CREATE TABLE workspace.deleted_organizations (
    organization_id text PRIMARY KEY,
    deleted_at timestamptz NOT NULL
);

CREATE FUNCTION workspace.reject_deleted_organization() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF EXISTS (SELECT 1 FROM workspace.deleted_organizations WHERE organization_id=NEW.organization_id) THEN
        RAISE EXCEPTION 'workspace organization was deleted';
    END IF;
    RETURN NEW;
END
$$;

CREATE TRIGGER workspace_configs_not_deleted BEFORE INSERT OR UPDATE ON workspace.configs
    FOR EACH ROW EXECUTE FUNCTION workspace.reject_deleted_organization();
CREATE TRIGGER workspace_versions_not_deleted BEFORE INSERT OR UPDATE ON workspace.versions
    FOR EACH ROW EXECUTE FUNCTION workspace.reject_deleted_organization();
CREATE TRIGGER workspace_mappings_not_deleted BEFORE INSERT OR UPDATE ON workspace.external_mappings
    FOR EACH ROW EXECUTE FUNCTION workspace.reject_deleted_organization();
CREATE TRIGGER workspace_plans_not_deleted BEFORE INSERT OR UPDATE ON workspace.reviewed_plans
    FOR EACH ROW EXECUTE FUNCTION workspace.reject_deleted_organization();
CREATE TRIGGER workspace_attempts_not_deleted BEFORE INSERT OR UPDATE ON workspace.apply_attempts
    FOR EACH ROW EXECUTE FUNCTION workspace.reject_deleted_organization();
