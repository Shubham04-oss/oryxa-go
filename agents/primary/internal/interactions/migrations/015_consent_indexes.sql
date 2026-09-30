-- The requests of one ask, which settling an ask reads (Store.Respond closes
-- the ones that did not answer) and a listing by ?ask_id= asks for. Only the
-- requests that belong to an ask are indexed, which is none of the ones an
-- agent made. CREATE INDEX holds a SHARE lock while it builds, which lets
-- reads go on, as 004's and 007's do. Safe to repeat.

CREATE INDEX IF NOT EXISTS agent_interactions_ask_idx
    ON agents_primary.agent_interactions (ask_id, id)
    WHERE ask_id <> '';
