-- full_sweep_at is when a source was last read from the beginning and told
-- what it no longer holds there. NULL means never: every source that existed
-- before this column is due for one immediately, which is the correct
-- correction for however long they have silently gone without one.
ALTER TABLE knowledge_sources ADD COLUMN IF NOT EXISTS full_sweep_at timestamptz;
