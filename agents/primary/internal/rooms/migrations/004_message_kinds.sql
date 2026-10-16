-- A room message is either something said (kind 'message', every message
-- before this one) or the notes a parley left behind ('parley_notes'), which
-- the organization agent posts when a call in the teamspace ends. data holds
-- what a client draws from such a message: for parley notes, the call, how
-- long it ran, what was decided and who owes what. It is null for anything
-- said. Both are added with defaults, so a release that does not know them
-- reads and writes messages as before.

ALTER TABLE agents_primary.room_messages
    ADD COLUMN IF NOT EXISTS kind text NOT NULL DEFAULT 'message',
    ADD COLUMN IF NOT EXISTS data jsonb;

DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint
        WHERE conname='room_messages_kind_check'
          AND conrelid='agents_primary.room_messages'::regclass
    ) THEN
        ALTER TABLE agents_primary.room_messages
            ADD CONSTRAINT room_messages_kind_check CHECK (kind IN ('message', 'parley_notes'));
    END IF;
END $$;

-- One set of notes per call in a room: the edge may send them again, and
-- AppendNotes answers a repeat with the notes already posted. This index is
-- the safety net behind that check, as UNIQUE (room_id, seq) is behind
-- Append's.
CREATE UNIQUE INDEX IF NOT EXISTS room_messages_parley_call_idx
    ON agents_primary.room_messages (room_id, (data->>'call_id'))
    WHERE kind = 'parley_notes';
