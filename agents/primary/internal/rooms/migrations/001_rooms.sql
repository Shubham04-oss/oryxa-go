-- A room is a teamspace: room_id below is always a teamspace id, resolved
-- against agents_primary.teamspaces at write time rather than mirrored into
-- a table of its own. There is deliberately no "rooms" table; everything
-- here is only what a room adds on top of the teamspace it already is, and
-- every table carries organization_id so Forget can scope by it without
-- having to ask teamspaces, which may already be gone by the time it runs.

CREATE SCHEMA IF NOT EXISTS agents_primary;

-- One counter row per room. Not a rooms table: it exists purely to make seq
-- allocation race-safe (see store.go, Append) and carries no identity of its
-- own beyond the room_id it counts for.
CREATE TABLE IF NOT EXISTS agents_primary.room_message_seqs (
    room_id text PRIMARY KEY,
    organization_id text NOT NULL,
    next_seq bigint NOT NULL DEFAULT 1 CHECK (next_seq > 0)
);

CREATE INDEX IF NOT EXISTS room_message_seqs_org_idx
    ON agents_primary.room_message_seqs (organization_id);

CREATE TABLE IF NOT EXISTS agents_primary.room_messages (
    id text PRIMARY KEY,
    room_id text NOT NULL,
    organization_id text NOT NULL,
    seq bigint NOT NULL CHECK (seq > 0),
    author_handle text NOT NULL,
    author_kind text NOT NULL CHECK (author_kind IN ('person', 'agent')),
    author_user_id text NOT NULL,
    -- Empty when a person spoke; set only when their agent did.
    author_agent_id text NOT NULL DEFAULT '',
    body text NOT NULL,
    -- The handles the body named, the author excluded; empty rather than
    -- null when nobody in particular was addressed.
    addressed_to text[] NOT NULL DEFAULT '{}',
    -- 0 rather than null: seq starts at 1, so 0 means "not a reply" without
    -- a nullable column and the scanning that would need.
    in_reply_to_seq bigint NOT NULL DEFAULT 0,
    created_at timestamptz NOT NULL DEFAULT now(),
    -- A safety net behind Append's own race-safety, not a substitute for it:
    -- see store.go for why two concurrent Appends can never try to use the
    -- same seq in the first place.
    UNIQUE (room_id, seq)
);

CREATE INDEX IF NOT EXISTS room_messages_room_idx
    ON agents_primary.room_messages (room_id, seq);

CREATE INDEX IF NOT EXISTS room_messages_forget_idx
    ON agents_primary.room_messages (organization_id, author_user_id);

CREATE TABLE IF NOT EXISTS agents_primary.room_briefs (
    room_id text PRIMARY KEY,
    organization_id text NOT NULL,
    -- 0 means no brief has ever been saved. SaveBrief's compare-and-set
    -- treats that as the revision a first save must match.
    revision bigint NOT NULL DEFAULT 0 CHECK (revision >= 0),
    through_seq bigint NOT NULL DEFAULT 0,
    summary text NOT NULL DEFAULT '',
    decided jsonb NOT NULL DEFAULT '[]'::jsonb CHECK (jsonb_typeof(decided) = 'array'),
    -- Named open_items rather than open: OPEN is SQL cursor syntax, not
    -- worth the quoting a bare "open" column would demand everywhere else.
    open_items jsonb NOT NULL DEFAULT '[]'::jsonb CHECK (jsonb_typeof(open_items) = 'array'),
    owed jsonb NOT NULL DEFAULT '[]'::jsonb CHECK (jsonb_typeof(owed) = 'array'),
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS room_briefs_org_idx
    ON agents_primary.room_briefs (organization_id);

CREATE TABLE IF NOT EXISTS agents_primary.room_presence (
    room_id text NOT NULL,
    organization_id text NOT NULL,
    user_id text NOT NULL,
    last_read_at timestamptz NOT NULL,
    PRIMARY KEY (room_id, user_id)
);

CREATE INDEX IF NOT EXISTS room_presence_watching_idx
    ON agents_primary.room_presence (room_id, last_read_at);

CREATE INDEX IF NOT EXISTS room_presence_forget_idx
    ON agents_primary.room_presence (organization_id, user_id);

-- One row per room and agent: the thread that agent's seat has been using
-- there, so a seat that speaks twice carries on rather than starting again
-- (rooms.Threads, implemented in store.go). Last writer wins, with no
-- revision column, because two turns of the same agent in one room are
-- already serialized by the relay that runs them.
CREATE TABLE IF NOT EXISTS agents_primary.room_agent_threads (
    room_id text NOT NULL,
    organization_id text NOT NULL,
    agent_id text NOT NULL,
    thread_id text NOT NULL DEFAULT '',
    updated_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (room_id, agent_id)
);

CREATE INDEX IF NOT EXISTS room_agent_threads_forget_idx
    ON agents_primary.room_agent_threads (organization_id, agent_id);
