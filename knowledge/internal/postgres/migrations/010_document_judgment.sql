-- The judge's own answer about a document's content (what kind of document
-- it is, and how sure), from which its verdict can be drawn again when the
-- source's choice of kinds changes, without asking the judge again.
ALTER TABLE knowledge_documents ADD COLUMN IF NOT EXISTS judgment jsonb;

-- What a source indexes, by kind of document; NULL is the default choice.
ALTER TABLE knowledge_sources ADD COLUMN IF NOT EXISTS include_kinds text[];
