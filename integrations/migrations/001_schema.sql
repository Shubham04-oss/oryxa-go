-- The integrations service keeps everything in its own schema, so its own
-- database login can be granted that schema and nothing else. Later
-- migrations name their tables integrations.<table>: the migration records
-- (oryxa_migrations) are the host's and live in public.

CREATE SCHEMA IF NOT EXISTS integrations;
