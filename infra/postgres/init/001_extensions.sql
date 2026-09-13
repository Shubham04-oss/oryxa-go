-- Applied only when the data volume is first created. Service migrations
-- also CREATE EXTENSION IF NOT EXISTS, so an existing volume still gets
-- vector, ltree and pg_trgm the next time teams migrates.
CREATE EXTENSION IF NOT EXISTS vector;
CREATE EXTENSION IF NOT EXISTS ltree;
CREATE EXTENSION IF NOT EXISTS pg_trgm;
