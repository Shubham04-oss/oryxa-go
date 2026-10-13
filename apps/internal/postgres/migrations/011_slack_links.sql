-- A member's Slack account, linked by the member signing in to Slack as
-- themself while signed in to Oryxa (Sign in with Slack). The Slack consent
-- loop trusts these links, and not a match on email, which whoever
-- administers the workspace can make by changing an account's address.
--
-- A workspace's user is linked to one member, and a member to one user of a
-- workspace. Nothing here is used until a member links, and nothing trusts it
-- until AGENTS_PRIMARY_SLACK_LINKED_ONLY is set.
CREATE TABLE IF NOT EXISTS apps_slack_links (
    team_id text NOT NULL,
    slack_user_id text NOT NULL,
    organization_id text NOT NULL,
    member_id text NOT NULL,
    linked_at timestamptz NOT NULL,
    PRIMARY KEY (team_id, slack_user_id)
);

CREATE UNIQUE INDEX IF NOT EXISTS apps_slack_links_member_idx
    ON apps_slack_links (team_id, member_id);

CREATE INDEX IF NOT EXISTS apps_slack_links_organization_member_idx
    ON apps_slack_links (organization_id, member_id);

-- A link in flight: ten minutes, spent once, as an install's. Only the state's
-- SHA-256 is kept.
CREATE TABLE IF NOT EXISTS apps_slack_link_flows (
    state_hash text PRIMARY KEY,
    organization_id text NOT NULL,
    member_id text NOT NULL,
    team_id text NOT NULL,
    return_to text NOT NULL DEFAULT '',
    created_at timestamptz NOT NULL,
    expires_at timestamptz NOT NULL,
    consumed_at timestamptz
);

CREATE INDEX IF NOT EXISTS apps_slack_link_flows_expires_idx
    ON apps_slack_link_flows (expires_at);
