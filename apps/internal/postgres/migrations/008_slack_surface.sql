-- The Slack surface: Oryxa's one Slack app, installed by a workspace for one
-- organization. Nothing here is used until APPS_SLACK_SURFACE is set.
--
-- What people typed and clicked is kept only as long as the services that act
-- on it need it: apps_slack_inbound is emptied after 7 days, and profiles,
-- posts and finished install flows are forgotten on their own schedules. The
-- bus carries ids alone, so nothing else holds these words.

-- A workspace's install of the app, keyed by the workspace's Slack team id and
-- linked to the organization it serves. A workspace has one install and it
-- survives whoever ran it leaving; an organization has one active
-- workspace. app_uninstalled and tokens_revoked end an install, which drops
-- its token and keeps the row, so the workspace can install again.
CREATE TABLE IF NOT EXISTS apps_slack_installs (
    team_id text PRIMARY KEY,
    organization_id text NOT NULL,
    status text NOT NULL CHECK (status IN ('active', 'ended')),
    end_reason text NOT NULL DEFAULT '',
    team_name text NOT NULL DEFAULT '',
    bot_user_id text NOT NULL,
    app_id text NOT NULL DEFAULT '',
    scopes text[] NOT NULL DEFAULT '{}',
    -- The bot token: an internal/secrets envelope bound to service
    -- oryxa-apps, resource type slack_install and the team id. NULL once the
    -- install ended.
    sealed_bot_token jsonb,
    installed_by text NOT NULL DEFAULT '',
    installed_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    ended_at timestamptz,
    CONSTRAINT apps_slack_installs_token_check CHECK (
        (status = 'active' AND sealed_bot_token IS NOT NULL)
        OR (status = 'ended' AND sealed_bot_token IS NULL)
    )
);

-- One active workspace per organization.
CREATE UNIQUE INDEX IF NOT EXISTS apps_slack_installs_organization_idx
    ON apps_slack_installs (organization_id)
    WHERE status = 'active';

-- An install in flight: ten minutes, and spent once. Only the state's SHA-256
-- is kept.
CREATE TABLE IF NOT EXISTS apps_slack_install_flows (
    state_hash text PRIMARY KEY,
    organization_id text NOT NULL,
    member_id text NOT NULL,
    return_to text NOT NULL DEFAULT '',
    created_at timestamptz NOT NULL,
    expires_at timestamptz NOT NULL,
    consumed_at timestamptz
);

CREATE INDEX IF NOT EXISTS apps_slack_install_flows_expires_idx
    ON apps_slack_install_flows (expires_at);

-- The channels an organization's admins marked high priority. Only private
-- channels that are not shared can be, which is checked with Slack when they
-- are marked and again every time a mark is used.
CREATE TABLE IF NOT EXISTS apps_slack_channels (
    organization_id text NOT NULL,
    team_id text NOT NULL,
    channel_id text NOT NULL,
    name text NOT NULL DEFAULT '',
    high_priority boolean NOT NULL DEFAULT false,
    marked_by text NOT NULL DEFAULT '',
    marked_at timestamptz NOT NULL,
    PRIMARY KEY (organization_id, channel_id)
);

-- What verified deliveries became: a message that mentioned the bot, or a
-- click on one of its buttons. The bus events name a row here by id, and
-- the services that act on them read it with a service token. A row is
-- forgotten 7 days after it arrived, text, tickets and all.
CREATE TABLE IF NOT EXISTS apps_slack_inbound (
    id text PRIMARY KEY,
    organization_id text NOT NULL,
    team_id text NOT NULL,
    kind text NOT NULL CHECK (kind IN ('message', 'action')),
    -- Slack's event id for a message; empty for a click.
    event_id text NOT NULL DEFAULT '',
    channel_id text NOT NULL,
    user_id text NOT NULL,
    -- The message's own timestamp (for a click, the message clicked) and the
    -- root of its thread, which is the same when it started one.
    message_ts text NOT NULL,
    thread_ts text NOT NULL,
    channel_type text NOT NULL DEFAULT '',
    ext_shared boolean NOT NULL DEFAULT false,
    mentions text[] NOT NULL DEFAULT '{}',
    body text NOT NULL DEFAULT '',
    action_id text NOT NULL DEFAULT '',
    action_value text NOT NULL DEFAULT '',
    received_at timestamptz NOT NULL
);

-- Posts go only into threads seen here, found by root or by message.
CREATE INDEX IF NOT EXISTS apps_slack_inbound_thread_idx
    ON apps_slack_inbound (organization_id, channel_id, thread_ts);
CREATE INDEX IF NOT EXISTS apps_slack_inbound_message_idx
    ON apps_slack_inbound (organization_id, channel_id, message_ts);
CREATE INDEX IF NOT EXISTS apps_slack_inbound_received_idx
    ON apps_slack_inbound (received_at);

-- The messages apps posted, once each per idempotency key, and where they
-- went, so one can be updated and only one of these can.
CREATE TABLE IF NOT EXISTS apps_slack_posts (
    organization_id text NOT NULL,
    idempotency_key text NOT NULL,
    -- SHA-256 of the request, so a key reused for something else is refused.
    request_hash text NOT NULL,
    team_id text NOT NULL,
    template text NOT NULL,
    kind text NOT NULL CHECK (kind IN ('message', 'ephemeral')),
    channel_id text NOT NULL DEFAULT '',
    ts text NOT NULL DEFAULT '',
    claimed_at timestamptz NOT NULL,
    completed_at timestamptz,
    PRIMARY KEY (organization_id, idempotency_key)
);

CREATE INDEX IF NOT EXISTS apps_slack_posts_message_idx
    ON apps_slack_posts (organization_id, channel_id, ts)
    WHERE completed_at IS NOT NULL;
CREATE INDEX IF NOT EXISTS apps_slack_posts_claimed_idx
    ON apps_slack_posts (claimed_at);

-- What Slack said about a person, kept for a day so a busy channel does not
-- ask again for each mention, and forgotten after a week.
CREATE TABLE IF NOT EXISTS apps_slack_profiles (
    team_id text NOT NULL,
    user_id text NOT NULL,
    email text NOT NULL DEFAULT '',
    -- Slack's is_email_confirmed: only a confirmed address is one to match a
    -- person to a member by.
    email_confirmed boolean NOT NULL DEFAULT false,
    name text NOT NULL DEFAULT '',
    title text NOT NULL DEFAULT '',
    is_guest boolean NOT NULL DEFAULT false,
    is_bot boolean NOT NULL DEFAULT false,
    is_deleted boolean NOT NULL DEFAULT false,
    is_stranger boolean NOT NULL DEFAULT false,
    fetched_at timestamptz NOT NULL,
    PRIMARY KEY (team_id, user_id)
);

CREATE INDEX IF NOT EXISTS apps_slack_profiles_fetched_idx
    ON apps_slack_profiles (fetched_at);
