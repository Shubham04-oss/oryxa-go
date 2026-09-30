-- The Slack consent loop. A member's Slack message that names several
-- owners makes one request for each of them, and the requests of one message
-- are an ask (interaction_asks): the first delivered answer settles it, and
-- the requests that did not answer are closed. An owner's approval can leave
-- a lease (interaction_leases), which approves the same member's follow-ups
-- in that Slack thread for an hour.
--
-- agent_interactions gains the ask a request belongs to ('' for every request
-- an agent made), where it was made ('agent' for every request made before
-- Slack asks, which the column default backfills without an UPDATE), how far
-- its owner let the answer go ('summary' or 'access', '' until decided),
-- where it was decided ('web' or 'slack' for an owner in person, 'lease' and
-- 'bypass' for the two decisions no click makes) and why, and when the
-- member who asked opened the one-time secret their owner shared.
--
-- As in 002, every check on the existing table is added NOT VALID, so nothing
-- is scanned under this file's ACCESS EXCLUSIVE lock; 014 validates them and
-- 015 builds the index. The two new tables are empty, so their checks and
-- indexes cost nothing to build here. Every statement is safe to repeat.

ALTER TABLE agents_primary.agent_interactions
    ADD COLUMN IF NOT EXISTS ask_id text NOT NULL DEFAULT '';

ALTER TABLE agents_primary.agent_interactions
    ADD COLUMN IF NOT EXISTS origin text NOT NULL DEFAULT 'agent';

ALTER TABLE agents_primary.agent_interactions
    ADD COLUMN IF NOT EXISTS consent text NOT NULL DEFAULT '';

ALTER TABLE agents_primary.agent_interactions
    ADD COLUMN IF NOT EXISTS decided_via text NOT NULL DEFAULT '';

ALTER TABLE agents_primary.agent_interactions
    ADD COLUMN IF NOT EXISTS decided_reason text NOT NULL DEFAULT '';

ALTER TABLE agents_primary.agent_interactions
    ADD COLUMN IF NOT EXISTS secret_viewed_at timestamptz;

DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint
        WHERE conname='agent_interactions_origin_check'
          AND conrelid='agents_primary.agent_interactions'::regclass
    ) THEN
        ALTER TABLE agents_primary.agent_interactions
            ADD CONSTRAINT agent_interactions_origin_check CHECK (origin IN ('agent', 'slack')) NOT VALID;
    END IF;
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint
        WHERE conname='agent_interactions_consent_check'
          AND conrelid='agents_primary.agent_interactions'::regclass
    ) THEN
        ALTER TABLE agents_primary.agent_interactions
            ADD CONSTRAINT agent_interactions_consent_check CHECK (consent IN ('', 'summary', 'access')) NOT VALID;
    END IF;
    -- Consent is what an approval allowed, so a request that is still
    -- pending, or was rejected, has none.
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint
        WHERE conname='agent_interactions_consent_decision_check'
          AND conrelid='agents_primary.agent_interactions'::regclass
    ) THEN
        ALTER TABLE agents_primary.agent_interactions
            ADD CONSTRAINT agent_interactions_consent_decision_check
            CHECK (consent = '' OR status NOT IN ('pending', 'rejected')) NOT VALID;
    END IF;
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint
        WHERE conname='agent_interactions_decided_via_check'
          AND conrelid='agents_primary.agent_interactions'::regclass
    ) THEN
        ALTER TABLE agents_primary.agent_interactions
            ADD CONSTRAINT agent_interactions_decided_via_check
            CHECK (decided_via IN ('', 'web', 'slack', 'lease', 'bypass')) NOT VALID;
    END IF;
    -- A lease decision and a bypass decision are made where they say, and
    -- only an owner in person decides on the web or in Slack.
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint
        WHERE conname='agent_interactions_decided_surface_check'
          AND conrelid='agents_primary.agent_interactions'::regclass
    ) THEN
        ALTER TABLE agents_primary.agent_interactions
            ADD CONSTRAINT agent_interactions_decided_surface_check
            CHECK ((decided_by_kind = 'lease') = (decided_via = 'lease')
               AND (decided_by_kind = 'bypass') = (decided_via = 'bypass')
               AND (decided_via NOT IN ('web', 'slack') OR decided_by_kind = 'member')) NOT VALID;
    END IF;
    -- 005's decider check knows three kinds. It is replaced, under its own
    -- name so that 005 finds it and leaves it alone if it runs again, by one
    -- that also knows the lease, which is its owner's decision (so it names
    -- them) and no agent's, and the bypass, which is nobody's (so it names no
    -- one). Dropping and adding happen in this one transaction, so no other
    -- session ever writes to the table without one, and a database that ran
    -- this already has the new check, which is left as it is.
    IF EXISTS (
        SELECT 1 FROM pg_constraint
        WHERE conname='agent_interactions_decided_by_check'
          AND conrelid='agents_primary.agent_interactions'::regclass
          AND pg_get_constraintdef(oid) NOT LIKE '%bypass%'
    ) THEN
        ALTER TABLE agents_primary.agent_interactions
            DROP CONSTRAINT agent_interactions_decided_by_check;
    END IF;
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint
        WHERE conname='agent_interactions_decided_by_check'
          AND conrelid='agents_primary.agent_interactions'::regclass
    ) THEN
        ALTER TABLE agents_primary.agent_interactions
            ADD CONSTRAINT agent_interactions_decided_by_check CHECK (
                (decided_by_kind = '' AND decided_by_user_id = '' AND decided_by_agent_id = '')
                OR (decided_by_kind = 'member' AND decided_by_user_id <> '' AND decided_by_agent_id = '')
                OR (decided_by_kind = 'agent' AND decided_by_user_id <> '' AND decided_by_agent_id <> '')
                OR (decided_by_kind = 'system' AND decided_by_user_id = '' AND decided_by_agent_id = '')
                OR (decided_by_kind = 'lease' AND decided_by_user_id <> '' AND decided_by_agent_id = '')
                OR (decided_by_kind = 'bypass' AND decided_by_user_id = '' AND decided_by_agent_id = '')
            ) NOT VALID;
    END IF;
END $$;

-- One row for each Slack message that asked one or more owners. The Slack
-- thread is where the answer goes; the trigger is the message itself, which
-- is what a retried delivery is recognised by. winner_request_id is the first
-- request completed with a delivered answer, set once, under the row's lock,
-- by the answer that wins (Store.Respond). bypass says a request of the ask
-- was approved by a bypass.
CREATE TABLE IF NOT EXISTS agents_primary.interaction_asks (
    id text PRIMARY KEY,
    organization_id text NOT NULL,
    origin text NOT NULL,
    team_id text NOT NULL DEFAULT '',
    channel_id text NOT NULL DEFAULT '',
    thread_ts text NOT NULL DEFAULT '',
    trigger_ts text NOT NULL DEFAULT '',
    asker_user_id text NOT NULL,
    kind text NOT NULL DEFAULT 'question',
    winner_request_id text NOT NULL DEFAULT '',
    bypass boolean NOT NULL DEFAULT false,
    created_at timestamptz NOT NULL DEFAULT now(),
    settled_at timestamptz,
    CONSTRAINT interaction_asks_origin_check CHECK (origin IN ('slack')),
    CONSTRAINT interaction_asks_kind_check CHECK (kind IN ('question', 'secret')),
    CONSTRAINT interaction_asks_slack_check CHECK (
        origin <> 'slack' OR (team_id <> '' AND channel_id <> '' AND thread_ts <> '' AND trigger_ts <> '')),
    CONSTRAINT interaction_asks_settled_check CHECK ((winner_request_id = '') = (settled_at IS NULL))
);

-- A message asks once, however many times its delivery is retried.
CREATE UNIQUE INDEX IF NOT EXISTS interaction_asks_trigger_idx
    ON agents_primary.interaction_asks (organization_id, origin, team_id, channel_id, trigger_ts, asker_user_id);

-- The asks an asker has open, which the router caps: only those with no
-- winner yet, which are few at any time.
CREATE INDEX IF NOT EXISTS interaction_asks_open_idx
    ON agents_primary.interaction_asks (organization_id, asker_user_id)
    WHERE winner_request_id = '';

-- What an owner gave one asker in one Slack thread: follow-ups there are
-- approved, with no click, until expires_at or revoked_at, whichever comes
-- first. request_id is the request whose approval gave it. At most one lease
-- is live for a thread and a pair of members: a second approval renews it.
CREATE TABLE IF NOT EXISTS agents_primary.interaction_leases (
    id text PRIMARY KEY,
    organization_id text NOT NULL,
    asker_user_id text NOT NULL,
    owner_user_id text NOT NULL,
    channel_id text NOT NULL,
    thread_ts text NOT NULL,
    request_id text NOT NULL DEFAULT '',
    created_at timestamptz NOT NULL DEFAULT now(),
    expires_at timestamptz NOT NULL,
    revoked_at timestamptz,
    CONSTRAINT interaction_leases_members_check CHECK (asker_user_id <> owner_user_id),
    CONSTRAINT interaction_leases_thread_check CHECK (channel_id <> '' AND thread_ts <> ''),
    CONSTRAINT interaction_leases_expiry_check CHECK (expires_at > created_at)
);

CREATE UNIQUE INDEX IF NOT EXISTS interaction_leases_live_idx
    ON agents_primary.interaction_leases (organization_id, asker_user_id, owner_user_id, channel_id, thread_ts)
    WHERE revoked_at IS NULL;

-- The leases a member gave, and the ones they hold, newest first: their
-- activity.
CREATE INDEX IF NOT EXISTS interaction_leases_owner_idx
    ON agents_primary.interaction_leases (organization_id, owner_user_id, created_at DESC, id DESC);

CREATE INDEX IF NOT EXISTS interaction_leases_asker_idx
    ON agents_primary.interaction_leases (organization_id, asker_user_id, created_at DESC, id DESC);
