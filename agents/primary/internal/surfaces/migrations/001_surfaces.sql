-- The Slack surfaces service's work, and its memory of what it said where.
--
-- surface_jobs holds one row for each agent request asked in Slack that has
-- something to be said about it. The bus delivers an event at least once and
-- says nothing the ledger does not, so an event only wakes the request's row
-- (wanted counts the events and sweeps that asked for a pass), and a pass
-- re-reads the ledger and says whatever Slack does not yet say: handled is the
-- count a finished pass covered, so events that arrive while one runs leave
-- the row due again. A request is worked on by one pass at a time, which is
-- what keeps a notice from being posted after the decision that settled it.
--
-- surface_links is one row for each message that was posted, or that had to be
-- given up on, by request and role (the owner's notice, the answer, a status,
-- a note in the asker's agent chat...). It holds where the message is in Slack,
-- so it can be updated, and the state it says, so it is not updated to what it
-- already says. It holds no text: what a message says is rendered in apps from
-- a re-read of the ledger, and Slack is the only place it is kept.

CREATE SCHEMA IF NOT EXISTS agents_primary;

CREATE TABLE IF NOT EXISTS agents_primary.surface_jobs (
    request_id text PRIMARY KEY,
    organization_id text NOT NULL,
    -- The members the last event named, so that a member's departure can find
    -- their rows. They decide nothing: a pass reads the parties from the ledger.
    asker_user_id text NOT NULL DEFAULT '',
    owner_user_id text NOT NULL DEFAULT '',
    wanted bigint NOT NULL DEFAULT 1 CHECK (wanted >= 1),
    handled bigint NOT NULL DEFAULT 0 CHECK (handled >= 0),
    last_event_id text NOT NULL DEFAULT '',
    recorded_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    available_at timestamptz NOT NULL DEFAULT now(),
    claimed_until timestamptz,
    attempts integer NOT NULL DEFAULT 0 CHECK (attempts >= 0),
    -- A row that was given up on is left, and woken again by the next event.
    gave_up boolean NOT NULL DEFAULT false,
    last_error text NOT NULL DEFAULT ''
);

CREATE INDEX IF NOT EXISTS surface_jobs_due_idx
    ON agents_primary.surface_jobs (available_at, recorded_at, request_id)
    WHERE handled < wanted;

CREATE INDEX IF NOT EXISTS surface_jobs_finished_idx
    ON agents_primary.surface_jobs (updated_at)
    WHERE handled >= wanted;

CREATE INDEX IF NOT EXISTS surface_jobs_organization_idx
    ON agents_primary.surface_jobs (organization_id);

CREATE TABLE IF NOT EXISTS agents_primary.surface_links (
    request_id text NOT NULL,
    role text NOT NULL,
    organization_id text NOT NULL,
    -- The Slack workspace the message is in, so that uninstalling the app
    -- there can find it.
    team_id text NOT NULL DEFAULT '',
    asker_user_id text NOT NULL DEFAULT '',
    owner_user_id text NOT NULL DEFAULT '',
    -- Where the message is, empty for a role that was given up on before
    -- anything was sent.
    channel_id text NOT NULL DEFAULT '',
    ts text NOT NULL DEFAULT '',
    state text NOT NULL DEFAULT '',
    -- For a notice that still has its buttons: when its request lapses, which
    -- is when nothing else would take the buttons away.
    expires_at timestamptz,
    swept_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (request_id, role)
);

CREATE INDEX IF NOT EXISTS surface_links_expiring_idx
    ON agents_primary.surface_links (expires_at)
    WHERE role = 'notice' AND state = 'pending';

CREATE INDEX IF NOT EXISTS surface_links_organization_idx
    ON agents_primary.surface_links (organization_id, team_id);

CREATE INDEX IF NOT EXISTS surface_links_updated_idx
    ON agents_primary.surface_links (updated_at);
