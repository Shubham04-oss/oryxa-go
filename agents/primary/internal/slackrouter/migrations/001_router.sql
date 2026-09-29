-- The Slack router's work, and its memory of who is who.
--
-- slack_jobs holds one row for each verified Slack delivery that apps kept,
-- keyed by the inbound row's id. The bus delivers an event at least once, and
-- a redelivered one, or a second event for the same inbound row, finds the
-- row already there and does nothing. A job carries ids and state only: what
-- a member wrote, and what they clicked, stay in apps' inbound row until apps
-- purges them, and are read from there when the job runs.
--
-- slack_identities is the match between a Slack user and a member of the
-- organization, made on the Slack user's confirmed sign-in address. It is
-- written each time a person is resolved, so it is as fresh as their last
-- message or click; nothing is authorized from it. Whoever needs to say which
-- Slack user a member is, to send them a direct message, reads it here.

CREATE SCHEMA IF NOT EXISTS agents_primary;

CREATE TABLE IF NOT EXISTS agents_primary.slack_jobs (
    inbound_id text PRIMARY KEY,
    kind text NOT NULL CHECK (kind IN ('message', 'action')),
    organization_id text NOT NULL,
    team_id text NOT NULL,
    -- The event that first recorded the job, for tracing; later events for
    -- the same inbound row are dropped, not recorded.
    event_id text NOT NULL DEFAULT '',
    received_at timestamptz NOT NULL,
    recorded_at timestamptz NOT NULL DEFAULT now(),
    available_at timestamptz NOT NULL DEFAULT now(),
    claimed_until timestamptz,
    attempts integer NOT NULL DEFAULT 0 CHECK (attempts >= 0),
    handled_at timestamptz,
    dropped boolean NOT NULL DEFAULT false,
    -- The ask a mention was turned into, set as soon as it exists, so that a
    -- retry does not make it again.
    ask_id text NOT NULL DEFAULT '',
    last_error text NOT NULL DEFAULT ''
);

CREATE INDEX IF NOT EXISTS slack_jobs_waiting_idx
    ON agents_primary.slack_jobs (available_at, recorded_at, inbound_id)
    WHERE handled_at IS NULL;

CREATE INDEX IF NOT EXISTS slack_jobs_organization_idx
    ON agents_primary.slack_jobs (organization_id);

CREATE TABLE IF NOT EXISTS agents_primary.slack_identities (
    organization_id text NOT NULL,
    team_id text NOT NULL,
    slack_user_id text NOT NULL,
    member_user_id text NOT NULL,
    -- The address the match was made on, lower-cased.
    email text NOT NULL,
    display_name text NOT NULL DEFAULT '',
    resolved_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (team_id, slack_user_id)
);

-- A member is one Slack user in a workspace, so the way back from a member to
-- their Slack user is unambiguous.
CREATE UNIQUE INDEX IF NOT EXISTS slack_identities_member_unique
    ON agents_primary.slack_identities (team_id, member_user_id);

CREATE INDEX IF NOT EXISTS slack_identities_organization_idx
    ON agents_primary.slack_identities (organization_id, member_user_id);
