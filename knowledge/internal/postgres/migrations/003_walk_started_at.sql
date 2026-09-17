-- walk_started_at is when the walk this cursor is mid-way through began: the
-- first of what can be several runs, when a source outgrows one run's page
-- budget. A full pass retracts what it did not see against this, not
-- against whichever run happens to finish it — using the finishing run's
-- own start would retract documents an earlier run of the same walk already
-- read.
ALTER TABLE knowledge_cursors ADD COLUMN IF NOT EXISTS walk_started_at timestamptz;
