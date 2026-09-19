-- token is watermark's counterpart for a provider whose watermark is an
-- opaque cursor rather than a moment: Drive's changes.startPageToken, Gmail's
-- historyId. At most one of watermark and token is ever set for one source,
-- by whichever extractor reads it.
ALTER TABLE knowledge_cursors ADD COLUMN IF NOT EXISTS token text NOT NULL DEFAULT '';
