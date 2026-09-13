-- Run as the database owner with:
-- psql "$DATABASE_URL" -v connection_reader_role=oryxa_connection_reader \
--   -f integrations/infra/postgres-grants.sql
--
-- The integrations runtime role is intentionally configured separately: it
-- needs the private tables, while ordinary services receive this view only.
BEGIN;

REVOKE ALL ON integrations_oauth_apps FROM PUBLIC;
REVOKE ALL ON integrations_connections FROM PUBLIC;
REVOKE ALL ON integrations_triggers FROM PUBLIC;
REVOKE ALL ON integrations_trigger_executions FROM PUBLIC;
REVOKE ALL ON integrations_action_executions FROM PUBLIC;
REVOKE ALL ON integrations_outbox FROM PUBLIC;
REVOKE ALL ON integrations_connection_catalog FROM PUBLIC;

GRANT SELECT ON integrations_connection_catalog TO :"connection_reader_role";

COMMIT;
