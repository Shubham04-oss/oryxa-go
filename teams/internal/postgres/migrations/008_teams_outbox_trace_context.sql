-- The event pipeline now carries a W3C trace context end to end: an outbox
-- row records the trace of the transaction that wrote it, so the dispatcher's
-- publisher can link its producer span back to that write, and a consumer can
-- become its child. NULL means the write carried no span, which is ordinary
-- for anything that runs unsampled or outside a traced request.
ALTER TABLE teams_outbox ADD COLUMN IF NOT EXISTS trace_context text;
