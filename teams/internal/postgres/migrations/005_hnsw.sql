-- Cosine distance (`<=>`) is what TeamsNearest orders by. Partial so teams
-- without an embedding stay out of the graph. Iterative scans for the
-- organization / visibility filters are set per query, not here.
CREATE INDEX IF NOT EXISTS teams_embedding_hnsw_idx
    ON teams
    USING hnsw (embedding vector_cosine_ops)
    WITH (m = 16, ef_construction = 64)
    WHERE embedding IS NOT NULL;
