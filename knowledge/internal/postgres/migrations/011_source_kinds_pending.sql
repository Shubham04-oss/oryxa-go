-- A source whose member changed what kinds of document it indexes has its
-- documents decided again, from their kept judgments, on its next pass.
ALTER TABLE knowledge_sources ADD COLUMN IF NOT EXISTS kinds_pending boolean NOT NULL DEFAULT false;
