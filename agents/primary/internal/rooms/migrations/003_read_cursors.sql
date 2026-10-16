-- How far each member has read each room, by message sequence: what unread
-- counts are measured from. It is separate from room_presence, which says who
-- is reading a room now and is refreshed by every read; this moves only when
-- a member's client says they have seen up to a message
-- (POST …/teamspaces/{id}/read), and only forward.
--
-- Like every table here it carries organization_id, derived from the room's
-- teamspace at write time, so Forget can scope by it.

CREATE TABLE IF NOT EXISTS agents_primary.room_read_cursors (
    room_id text NOT NULL,
    organization_id text NOT NULL,
    user_id text NOT NULL,
    -- 0 rather than null means nothing read yet: seq starts at 1.
    last_read_seq bigint NOT NULL DEFAULT 0 CHECK (last_read_seq >= 0),
    updated_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (room_id, user_id)
);

CREATE INDEX IF NOT EXISTS room_read_cursors_forget_idx
    ON agents_primary.room_read_cursors (organization_id, user_id);

-- Without a cursor a member has read nothing, so on the day this ships every
-- room would show its whole history unread, rooms read yesterday included.
-- Whoever has read a room (room_presence) starts as having read all of it.
-- Somebody who joins a team later starts with no cursor, and so with its
-- history unread: that is the catch-up they came for, and the count stops at
-- 100 (rooms.UnreadCap). Safe to repeat: a cursor already there is kept.
INSERT INTO agents_primary.room_read_cursors (room_id, organization_id, user_id, last_read_seq)
SELECT presence.room_id, presence.organization_id, presence.user_id, counter.next_seq - 1
FROM agents_primary.room_presence presence
JOIN agents_primary.room_message_seqs counter ON counter.room_id = presence.room_id
ON CONFLICT (room_id, user_id) DO NOTHING;
