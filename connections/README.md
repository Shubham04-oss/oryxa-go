# Connections service

Status: built. Apps; OAuth 2.0, GitHub App installation, client-credentials and
key-based connections for Airtable, Asana, Atlassian (Jira and Confluence),
Box, ClickUp, Dropbox, Figma, GitHub, Google, Linear, Microsoft, Notion and
Slack;
reconnect, labels, defaults and disconnect; token issuance with refresh and
minting under a lease; holds, revocation and credential deletion; the
heartbeat; the auth and teams event consumer; and service identity. Not built
yet: custom providers from OpenAPI specs (see Later). The service replaces the
OAuth, token and connection code of the integrations service, which has been
removed. It held no production data, so nothing migrates.

Connections owns how an organization reaches third-party accounts: the apps an
organization enables, the connections its members, teams and admins make, the
credentials behind them, and whether each one still works. Auth remains the
identity and organization/membership boundary. Teams remains the team graph:
connections forwards the caller's credentials to teams when a connection hangs
on a team, and never copies the tree. Provider operations — actions, triggers,
webhooks and tools — belong to the services that call providers. They never
store a credential; they ask connections for one whenever they need it.

A credential leaves this service only in a token-issuance response. Client
secrets, private keys and refresh tokens never leave it at all.

```text
app ── apps, connect, reconnect, disconnect ──▶ connections ── token, identity, revoke ──▶ provider
                                                   │
                                                   ├── GET auth  /v1/organizations/{org}
                                                   ├── GET teams /v1/organizations/{org}/teams/{team}
                                                   └── oryxa.connections ──▶ EventBridge
service ── POST /v1/internal/connections/{id}/token ──▶ connections
auth ── organization deleted, member removed ──▶ connections-events queue
teams ── team deleted ──▶ connections-events queue
```

## Apps

An app is one organization enabling one provider. Only org admins create,
change, disable or delete apps.

| Field | Holds |
| --- | --- |
| `provider` | A built-in provider: `airtable`, `asana`, `atlassian`, `box`, `clickup`, `dropbox`, `figma`, `github`, `google`, `linear`, `microsoft`, `notion` or `slack` |
| `credential_type` | How connections to this app authenticate (below). Defaults to the provider's `oauth2` |
| Client | `oauth2` and `oauth2_client_credentials` apps: `client_id` and an encrypted `client_secret`. `installation` apps: also the GitHub App's `github_app_id`, `github_app_slug` and encrypted `private_key`. Key-based apps take no client |
| `scopes` | What `oauth2` connections may be granted. Never beyond the provider's ceiling; always includes its required scopes. Client-credentials apps carry the provider's fixed app-only scopes; installation and key-based apps carry none |
| `settings` | What is not secret about the client, such as the GitHub App's ID and slug, and the app's `domain` where the provider needs one |
| `status` | `active` or `disabled` |

- Narrowing `scopes` applies at once: tokens are issued only for scopes the app still allows. Widening needs each connection to reconnect to gain the new scopes.
- Rotating the secret or private key keeps every connection. Changing the client ID moves the app's connections to `needs_reconnect`, because grants belong to the client that issued them.
- Disabling blocks every connection of the app without changing its status; enabling restores them. Deleting the app disconnects them. A deleted app keeps its row, because revoking its connections' grants still needs its client.

## Credential types

| Type | Connection holds | Provider account | Issued to callers | Renewal |
| --- | --- | --- | --- | --- |
| `oauth2` | The grant: access and refresh tokens, or a token set (Slack's bot token and the member's user token) | The consenting user (Slack: workspace and user) | The access token; `token: "user"` picks Slack's user token | Refreshed before expiry |
| `oauth2_client_credentials` | The tenant, and the last token minted in it | The tenant | A minted app-only token | Minted again before expiry |
| `installation` | A GitHub App installation ID, and the last installation token | The installation, named by its account | An installation token, minted with the app's private key | Minted again before expiry (hourly) |
| `api_key`, `bearer`, `basic` | The secret | The account the provider's identity check returns | The secret, with where it goes on a request | None; the owner replaces it |

Built-in key-based providers are Airtable, Asana and GitHub personal access
tokens and Notion internal integration tokens (`bearer`); ClickUp personal
tokens, Figma personal access tokens (sent in `X-Figma-Token`) and Linear API
keys (`api_key`); and Atlassian API tokens (`basic`, the account's email and
the token). Microsoft offers client credentials; GitHub offers installations.
Client-credentials connections are team or organization scope only, because no
person consents to them.

Only `oauth2` connections carry scopes connections can see. An installation
token has the permissions set on the GitHub App, a client-credentials token the
application permissions an admin granted, and a key whatever its owner gave it,
so requested scopes are checked only for `oauth2` connections.

Some providers differ in ways callers notice:

- `atlassian` covers Jira and Confluence Cloud. An `oauth2` grant reaches every site the member lets the app use; callers list those sites, with the cloud IDs their API URLs need, at `https://api.atlassian.com/oauth/token/accessible-resources`. `offline_access` and `read:me` are always requested, and refresh tokens rotate on every refresh. Classic and granular scopes are both offered; Jira Software's boards, sprints and epics have only granular ones. A `basic` app names its site as its `domain`, such as `acme.atlassian.net`, and its API tokens work there alone.
- `asana` apps either have full permissions, allowing `default` on its own, or allow specific scopes such as `tasks:read`, which the Asana app must also enable, with `users:read` always included for the identity check. `default` covers every specific scope a caller asks for.
- `clickup` has no OAuth scopes: the member picks the workspaces an app may reach while consenting. Its access tokens do not expire, so there is nothing to refresh.
- `airtable` requires PKCE, and its refresh tokens rotate. The member picks the bases and workspaces an app may reach while consenting. `user.email:read` is always requested: Airtable refuses an empty scope, and the identity check names the account by its email.
- `box` refresh tokens work once; each refresh returns a new one. An enterprise's admin may have to authorize the Box app before its members can connect.
- `dropbox` always asks for offline access, so it issues a refresh token, which does not rotate. `account_info.read` is always requested. Revoking refreshes the grant first, because Dropbox revokes only with a live access token.
- `figma` refreshes at its own endpoint and keeps the refresh token. It keeps one access token per app and account: refreshing one connection's token ends the previous one, so the same account connected twice to one app interrupts itself. `current_user:read` is always requested.
- `notion` has no OAuth scopes: an integration's capabilities are set in Notion, and the member picks the pages it may reach while consenting. A connection is the integration's bot in the member's workspace. Its `metadata` carries `workspace_id`, `workspace_name`, `bot_id` and, when the member duplicated a template, `duplicated_template_id`. Connections calls Notion with `Notion-Version: 2025-09-03`.

## Ownership

Privilege and placement are separate, as on files. **Scope** is who a
connection belongs to.

| Scope | Who may create | Who may use | Who may see | Who may change, reconnect or disconnect |
| --- | --- | --- | --- | --- |
| `member` (default) | Any org member; belongs to the caller | The owner | Owner; org admins see metadata | Owner; org admins may disconnect |
| `team` | Team `can_manage` or org admin | Anyone who can **see** the team | Anyone who can see the team | Team `can_manage` or org admin |
| `organization` | Org admin | Any org member, or only org admins when `usage` is `admins` | Any org member | Org admin |

A caller who is not in the organization gets `404`, as auth conceals which
organizations exist. A team the caller cannot see is `404`, as teams conceals
it. A connection the caller cannot see is also `404`. Creating an organization
connection without being an org admin is `403`.

A scope can hold several connections to the same app, one per provider
account, so a member can connect both a work and a personal Gmail. Connecting
an account that already has a live connection in that scope is
`409 already_connected`; reconnect it instead. Each scope marks one connection
per app as `default` (the first one connected), which tools use when the caller
does not name a connection. `PATCH` moves the default.

"Use" means tools may act with a connection for that member. The raw token goes
only to a service, never to the member: a member cannot pull a team or
organization connection's token with their own credentials.

When a member triggers work, the service carrying it out forwards the member's
bearer token, and connections checks that member's access against auth and
teams on the spot. Work that runs later without a member, such as trigger
deliveries and cleanup, runs as the member who set it up. Connections cannot
ask auth or teams on that member's behalf, so a service acting on its own is
trusted with team and organization access, and the service that owns the work
stops it when `teams.member.removed.v1` or `auth.member.removed.v1` arrives for
its creator. Two rules still hold: a member connection serves only its owner,
and an admins-only organization connection, whose admin check needs auth,
serves a service acting on its own only for `cleanup`.

## Status and health

| Status | Meaning |
| --- | --- |
| `active` | Usable |
| `needs_reconnect` | A permanent failure. `reason` is `refresh_rejected`, `unauthorized`, `account_changed`, `client_changed`, `installation_removed`, or `credential_rejected`. Reconnecting (or replacing a key) returns it to `active` with the same ID |
| `disconnected` | Final. `reason` is `disconnected`, `member_left`, `team_deleted`, `organization_deleted`, or `app_deleted` |

Health is separate and never blocks use on its own: `unknown` until the first
check, then `healthy` or `degraded`. Connections of a disabled app list
`"blocked": "app_disabled"`.

## Connect

OAuth consent, GitHub App installations and Microsoft admin consent finish
from the app, not from the provider's redirect:

```text
app ── POST …/connections/authorize ──▶ connections ── session ──▶ authorization_url
browser ──▶ provider consent ──▶ GET /v1/oauth/callback/{provider}
connections ── keep what came back on the session ──▶ redirect to return_uri?session_id=…
app ── POST …/connections/complete {session_id} ──▶ connections ── exchange, identity ──▶ connection
```

- The session is single-use, stored hashed, and expires after 10 minutes. It records the member, app, scope, team, usage, return URL, requested scopes and encrypted PKCE verifier. PKCE (`S256`) is used whenever the provider supports it. A member holds at most 20 unexpired sessions in an organization; past that, `authorize` answers `429 rate_limited`.
- Only the member who started a session can `complete` it, and nothing is exchanged before then. A forwarded authorization link cannot attach someone else's account.
- The callback always sends the browser back to the session's return URL: with `session_id` on success; with `error=provider_denied` when the provider refused; with `error=installation_pending` when a GitHub App installation waits for an organization owner's approval; or with `error=session_expired` when the session was already called back or has expired. Only an unknown session gets a plain error page. The provider's own error text is never kept or passed on. Return URLs must be `https` (or `http` on localhost) on a configured origin, without user info or a fragment.
- `complete` decides access again, because the member may have lost it since authorizing. It claims the session before exchanging anything, so a repeated request never exchanges a code twice. For `oauth2` it checks that every requested scope was granted and resolves the provider account; it then consumes the session and creates the connection in one transaction. Any failure after the claim uses the session up; the member starts again.
- GitHub's installation redirect carries an `installation_id` anyone can forge. The GitHub App requests user authorization during installation (its callback URL is connections' callback), and `complete` accepts the installation only if it appears in that user's `GET /user/installations`. It then mints an installation token to prove the private key works.
- Authorizing a client-credentials app sends the admin to the provider's admin consent. `complete` mints a token in the tenant the consent came back for, and the connection belongs to that tenant.

Key-based and client-credentials connections can skip the redirect.
`POST …/connections` with the credential runs the provider's identity check (or
mints a token in the given tenant), then saves the connection as `active` or
answers `422 credential_rejected`.

## Reconnect

`POST …/connections/{id}/reconnect` starts the same flow for an existing
connection. `complete` must see the same provider account, or it answers
`409 account_changed` and revokes the grant it just received, unless another
connection of the same app and account holds credentials. It then replaces the
credentials and granted scopes in place, returns the connection to `active`,
and publishes `connections.connection.reconnected.v1`. Reconnecting is also how
a connection gains scopes its app newly allows. Replacing a key with
`PUT …/credential` follows the same account rule.

## Disconnect

1. `DELETE …/connections/{id}` sets `disconnected` and publishes `connections.connection.disconnected.v1`.
2. A service that created remote resources with the connection, such as webhooks at the provider, registered a hold when it created them. It cleans up with `cleanup` tokens, then releases its hold.
3. When no holds remain, or 7 days after the disconnect, a cleanup worker revokes the grant at the provider where the provider supports it, deletes the credentials, and publishes `connections.connection.credentials-deleted.v1` saying whether it revoked.

Providers revoke per account and client, so a grant is left alone while another
connection of the same app and provider account still holds credentials; the
last one to be deleted revokes it. Slack's bot token is shared by everyone who
connected the workspace, so only the member's user token is revoked. Keys cannot
be revoked from connections, and minted tokens expire on their own. A provider
that cannot answer is asked again every couple of minutes; a day past the
7-day hold window the credentials are deleted anyway. The same worker removes expired
authorization sessions.

## Token issuance

Services ask for a credential whenever they need one:

```text
POST /v1/internal/connections/{connectionID}/token
```

```json
{
  "organization_id": "org_…",
  "purpose": "action",
  "acting_member_id": "user-1",
  "scopes": ["https://www.googleapis.com/auth/gmail.send"]
}
```

`purpose` is `action`, `trigger`, `cleanup` or `heartbeat` (connections' own).
An optional `token` picks one from a token set, such as Slack's `user`; without
it the primary token is issued.

```json
{
  "token_type": "Bearer",
  "access_token": "…",
  "expires_at": "2026-09-14T13:00:00Z",
  "placement": { "in": "header", "name": "Authorization", "prefix": "Bearer " },
  "scopes": ["https://www.googleapis.com/auth/gmail.send"],
  "provider_account": { "id": "1098…", "name": "asha@example.com" }
}
```

`placement` says where the credential goes: a header or query parameter, with
an optional prefix. A key-based connection's `token_type` is its credential
type, and it has no `expires_at`. Two fields appear only when they apply:
`domain`, the app's domain for a provider reached through one, such as
`acme.atlassian.net`; and `metadata`, what the provider returned with an
`oauth2` token that callers need and that is not secret, such as Salesforce's
`instance_url`, kept through refreshes.

Checks, in order:

1. The caller is a service allowed this purpose, and its token names the same purpose. A member's own credentials are never enough.
2. The connection is in `organization_id`, and the member the work runs as may use it. With a forwarded bearer token, that member's access is checked with auth and teams, and `acting_member_id` may only name them. Without one, the service must name `acting_member_id`, and the ownership rules for work without a member apply.
3. The connection and its app are both `active`. Purpose `cleanup` is the exception: it also works after a permanent failure, while the app is disabled, and on a disconnected connection with an open hold, because remote resources still have to be removed.
4. For `oauth2` connections, every requested scope is granted and still allowed by the app, counting scopes that imply others. Otherwise `422 scope_required` lists what is missing.
5. If the token expires within 2 minutes, it is refreshed or minted first. One renewal runs at a time per connection, under a lease and version check; other callers wait for it, up to 10 seconds, instead of renewing again. A rotated refresh token is saved before the new access token is returned. A refused refresh moves the connection to `needs_reconnect` (`409`). A provider that cannot answer still leaves an unexpired token usable; otherwise it is `503`.

Callers may keep an access token in memory until 2 minutes before `expires_at`.
Key-based secrets are never cached. A caller that sees the provider reject a
credential reports it with `POST /v1/internal/connections/{id}/report`, which
brings the connection's check forward to now.

## Heartbeat

A worker inside connections checks connections before a tool finds out they are
broken:

- It claims active connections whose next check is due, in batches (`FOR UPDATE SKIP LOCKED`), so every replica can run it. The interval is 24 hours with 10% jitter either way.
- It skips a connection that issued an `action` or `trigger` token in the last 12 hours with no failure reported since, and one whose app is disabled. Real use is the heartbeat.
- An `oauth2` check renews the token as needed, which keeps idle refresh tokens alive, then runs the provider's identity check and confirms the account has not changed (Slack uses `auth.test`). A client-credentials check mints a token. An installation check confirms the installation still exists. A key-based check runs the identity check with the key.
- A timeout, 5xx or 429 marks the connection `degraded` and retries with backoff, from 5 minutes doubling up to 6 hours.
- A refused refresh (`refresh_rejected`), a refused credential (`unauthorized` for `oauth2`, `credential_rejected` otherwise), a different account (`account_changed`) or a removed installation (`installation_removed`) moves the connection to `needs_reconnect`.
- `connections.connection.health-changed.v1` is published only when health changes.

## Calls from other services

Every call to the internal API carries a service identity, because those
routes hand out credentials. The caller signs a JWT (`ES256`) that lives at
most 60 seconds with its own KMS asymmetric key (`ECC_NIST_P256`, `kms:Sign`),
naming itself (`iss`), the audience `connections` (`aud`), and the purpose,
and sends it in the `X-Oryxa-Service-Token` header.
`internal/serviceauth` has the signer callers use and the verifier connections
uses. Connections checks the signature with that caller's public key (fetched
once with `kms:GetPublicKey`), the audience, the lifetime with 30 seconds of
clock skew, and a per-caller allowlist of purposes from
`CONNECTIONS_SERVICE_CALLERS`:

```json
[{"service": "worker", "key_id": "arn:aws:kms:ap-south-1:…:key/…", "purposes": ["action", "trigger", "cleanup"]}]
```

IAM decides which runtime can sign with a key. Without
`CONNECTIONS_SERVICE_CALLERS`, the internal API refuses every call.

When a member triggers the work, the service also forwards the member's
`Authorization: Bearer` token, and connections asks auth and teams about that
member exactly as files does. A member's own bearer token or session cookie is
never enough on an internal route.

## API

Cookie-authenticated mutations on public routes must come from an origin in
`CONNECTIONS_ALLOWED_ORIGINS`. Team-scoped operations need `TEAMS_URL`.

| Method | Path | Who | Purpose |
| --- | --- | --- | --- |
| `GET` | `/healthz` | Anyone | Liveness |
| `GET` | `/v1/organizations/{organizationID}/apps` | Members | Enabled apps: provider, credential type, allowed scopes, settings, status. Never secrets |
| `POST` | `/v1/organizations/{organizationID}/apps` | Org admins | Enable a provider: `{provider, credential_type, client_id, client_secret, scopes}`, plus `{github_app_id, github_app_slug, private_key}` for an installation app, and `domain` where the provider needs one |
| `PATCH` | `/v1/organizations/{organizationID}/apps/{appID}` | Org admins | `{scopes}`, `{client_secret}`, `{private_key}`, or `{client_id, client_secret}` |
| `POST` | `/v1/organizations/{organizationID}/apps/{appID}/disable` | Org admins | Block its connections |
| `POST` | `/v1/organizations/{organizationID}/apps/{appID}/enable` | Org admins | Unblock them |
| `DELETE` | `/v1/organizations/{organizationID}/apps/{appID}` | Org admins | Disconnect its connections and remove the app |
| `GET` | `/v1/organizations/{organizationID}/connections` | Members | Live connections the caller can see, with status, health and granted scopes. Query `app_id`, `scope`, `team_id` |
| `POST` | `/v1/organizations/{organizationID}/connections/authorize` | Per scope | Start OAuth, an installation or admin consent: `{app_id, scope, team_id, usage, return_uri}` → `{authorization_url, expires_at}`. `scope` defaults to `member`, `usage` to `members` |
| `GET` | `/v1/oauth/callback/{provider}` | The provider's redirect | Keep what came back on the session and `303` to its return URL |
| `POST` | `/v1/organizations/{organizationID}/connections/complete` | The member who started | Finish: `{session_id}` → `201` with the new connection, or `200` with a reconnected one |
| `POST` | `/v1/organizations/{organizationID}/connections` | Per scope | Create a key-based or client-credentials connection: `{app_id, scope, team_id, usage, label, credential: {api_key \| token \| username, password \| tenant}}` → `201` |
| `GET` | `/v1/organizations/{organizationID}/connections/{connectionID}` | Per scope | Metadata, status and health, in any status |
| `PATCH` | `/v1/organizations/{organizationID}/connections/{connectionID}` | Per scope | `{label}` and/or `{default: true}` |
| `POST` | `/v1/organizations/{organizationID}/connections/{connectionID}/reconnect` | Per scope | Start reconnect: `{return_uri}` → `{authorization_url, expires_at}` |
| `PUT` | `/v1/organizations/{organizationID}/connections/{connectionID}/credential` | Per scope | Replace a key-based secret: `{credential}` |
| `DELETE` | `/v1/organizations/{organizationID}/connections/{connectionID}` | Per scope | Disconnect |
| `POST` | `/v1/internal/connections/{connectionID}/token` | Services, optionally forwarding a member | Issue a credential |
| `GET` | `/v1/internal/connections/{connectionID}?organization_id=…` | Services, optionally forwarding a member | Status, scope, owner, granted scopes and provider account |
| `POST` | `/v1/internal/connections/{connectionID}/holds` | Services | Keep credentials after disconnect until released: `{organization_id, holder}` → `204` |
| `DELETE` | `/v1/internal/connections/{connectionID}/holds/{holder}?organization_id=…` | Services | Release a hold → `204` |
| `POST` | `/v1/internal/connections/{connectionID}/report` | Services | Report a credential the provider rejected: `{organization_id}` → `204` |

A holder is 1 to 80 lowercase letters, digits, `.`, `_`, `:` or `-`, such as
`worker:webhooks`. Adding a hold twice, or releasing one that is gone,
changes nothing.

Apps list their provider, credential type, allowed scopes, settings and status.
Org admins also see `client_id`; no one sees a secret. A connection, owned as
on files by `member_id` or `team_id`:

```json
{
  "id": "con_…",
  "organization_id": "org_…",
  "app_id": "app_…",
  "provider": "google",
  "credential_type": "oauth2",
  "scope": "member",
  "member_id": "user-1",
  "usage": "members",
  "provider_account": { "id": "1098…", "name": "asha@example.com" },
  "label": "Work Gmail",
  "default": true,
  "scopes": ["https://www.googleapis.com/auth/gmail.send"],
  "status": "active",
  "status_changed_at": "2026-09-14T10:00:00Z",
  "last_used_at": "2026-09-14T10:30:00Z",
  "created_by": "user-1",
  "connected_at": "2026-09-14T10:00:00Z",
  "updated_at": "2026-09-14T10:00:00Z",
  "health": { "state": "healthy", "checked_at": "2026-09-14T11:00:00Z" }
}
```

`reason` appears with `needs_reconnect` and `disconnected`, and
`"blocked": "app_disabled"` on live connections of a disabled app.

Errors use the `{"error": {"code": "…", "message": "…"}}` body:

| Code | Status | When |
| --- | --- | --- |
| `invalid_request` | 400 | The body or a field is invalid; `message` says which |
| `unauthorized` | 401 | No credentials, auth refused them, or an internal route without a valid service token |
| `forbidden` | 403 | Visible, but the caller may not do this |
| `not_found` | 404 | Not in the organization, or not visible to the caller |
| `conflict` | 409 | The resource changed during the request; retry |
| `already_connected` | 409 | That provider account already has a live connection in this scope |
| `account_changed` | 409 | Reconnect or key replacement returned a different account |
| `needs_reconnect` | 409 | The connection must be reconnected first |
| `app_disabled` | 409 | The connection's app is disabled |
| `scope_required` | 422 | Missing scopes are listed in `message` |
| `credential_rejected` | 422 | The provider refused a key or client |
| `rate_limited` | 429 | Too many open authorization sessions |
| `session_expired` | 400 | The authorization session is used up or expired |
| `provider_unavailable` | 503 | The provider timed out or failed; retry |
| `unavailable` | 503 | A service token could not be checked; retry |

## Events

Connections publishes source `oryxa.connections` to `EVENT_BUS_NAME`, in the
envelope every Oryxa publisher uses. Contracts are in
`contracts/events/connections/v1`.

| Detail type | When |
| --- | --- |
| `connections.app.created.v1` | A provider is enabled |
| `connections.app.updated.v1` | Allowed scopes, secret, key or client change |
| `connections.app.disabled.v1` | An app is disabled |
| `connections.app.enabled.v1` | A disabled app is enabled again |
| `connections.app.deleted.v1` | An app is deleted, or its organization is |
| `connections.connection.connected.v1` | A connection is created |
| `connections.connection.reconnected.v1` | A connection is reconnected, or its key replaced, in place |
| `connections.connection.needs-reconnect.v1` | A permanent failure |
| `connections.connection.disconnected.v1` | A connection is disconnected |
| `connections.connection.credentials-deleted.v1` | Its credentials are deleted, and whether the grant was revoked |
| `connections.connection.health-changed.v1` | Its health changes |

Events carry ids, scope, owner, provider, provider account, scopes, status and
reason. They never carry a token, secret, key, or provider error body.

Connections consumes `auth.organization.deleted.v1`, `auth.member.removed.v1`,
and `teams.team.deleted.v1` from `CONNECTIONS_EVENTS_QUEUE_URL`. Organization
deletion disconnects everything and deletes the apps. A member leaving the
organization disconnects their **member-scoped** connections with reason
`member_left`; team and organization connections they created stay.
`auth.member.removed.v1` with `reason` `organization_deleted` is ignored,
because `auth.organization.deleted.v1` already owns that. Deleting a team
disconnects its connections with reason `team_deleted`. Every handler is safe
to redeliver. `go run ./connections/infra/events/cmd/provision` creates the
queue, its dead-letter queue and the EventBridge rule.

## Database

Connections uses `/internal/postgreskit`. Other services never read these
tables; they use the internal API.

| Table | Holds |
| --- | --- |
| `connections_apps` | Enabled providers per organization: credential type, client ID, encrypted secret and private key, settings, allowed scopes, status |
| `connections` | Scope, owner, provider account, label, default flag, granted scopes, status and reason, encrypted credentials, version, renewal lease, last use, last report |
| `connections_sessions` | Single-use authorization sessions, hashed, with the encrypted PKCE verifier and, after the callback, what the provider sent back, encrypted. Claimed once before anything is exchanged |
| `connections_health` | Health, last check, last success, consecutive failures, reason, next check, claim |
| `connections_holds` | Holds that keep credentials after disconnect |
| `connections_outbox` | Events written in the same transaction as each change |

There is one live connection per organization, app, scope, owner and provider
account, and one default per organization, app, scope and owner.

## Security

- Every secret is envelope-encrypted with connections' own KMS key. The encryption context binds the service, organization, resource type and ID, so ciphertext cannot be moved between organizations or records. Plaintext buffers are wiped after use.
- Only the token-issuance response carries a credential, and only a service with a valid service token receives it. A member's own bearer token or session cookie never reaches a raw token. Logs, events, errors and the public API never carry one, and provider error bodies are replaced with stable reasons.
- Every outbound provider call (token, identity, revoke) has a 10-second timeout, a 1 MiB response cap, no redirects, and an address check after DNS resolution, so an endpoint cannot reach private, loopback or cloud-metadata addresses.
- An app never allows more than its provider's ceiling, and `oauth2` tokens are issued only for scopes that are both granted and still allowed.
- A connection's provider account never changes.

## Built from integrations

Most of the machinery existed in the integrations service and moved here
before it was removed: envelope encryption, refresh under a lease, the OAuth
flow, the provider OAuth adapters, the outbox dispatcher, and the auth event
consumer. New here: sessions finished from the
app, team and organization scopes, holds, statuses and health, key-based,
client-credentials and installation connections, token issuance, and the
service identity.

## Adding a provider

A provider is one file in `internal/providers` returning a `Provider`,
registered in `Builtin()`. The application, store and API need no change. What
a definition can say:

- Credential types, and for each the URLs it uses and an identity check that returns the account a grant or key belongs to.
- Scope rules: the ceiling, required scopes, implications between scopes, and exclusive scopes allowed only on their own.
- OAuth 2.0 differences: fixed authorization parameters, PKCE, the scope separator, scopes adjusted before sending or read back from the token response, JSON token requests (`JSONTokenRequests`), client credentials sent as HTTP Basic (`ClientSecretBasic`), fixed headers on token requests (`TokenHeaders`), a separate refresh endpoint (`RefreshURL`), a token response that is not RFC 6749 (`DecodeToken`), permanent refresh errors, and whether a refresh token is required.
- `Metadata`: token response fields that are not secret and that callers need with the token.
- A `DomainRule` when URLs name `{domain}`: each app supplies its domain once, validated against the rule, and every flow for the app runs with it filled in. The provider HTTP client still refuses private addresses.
- A revoke hook, where the provider can end a grant.

`TestBuiltinProvidersAreConsistent` checks every definition; each provider also
gets a flow test against a fake server.

## Run locally

Copy `.env.example` to `connections/.env`, then:

```sh
docker compose up -d
(set -a && . connections/.env && go run ./connections/cmd/connections)
```

`CONNECTIONS_KMS_KEY_ID` must name a KMS key the service can use: the local
stack runs no KMS, so use a key in AWS, or add `kms` to LocalStack's
`SERVICES` and set `AWS_ENDPOINT_URL_KMS`. Register
`{CONNECTIONS_PUBLIC_BASE_URL}/v1/oauth/callback/{provider}` as the redirect URL
of each OAuth app, and as the callback URL of each GitHub App (with user
authorization during installation turned on). Set
`CONNECTIONS_SERVICE_CALLERS` for the services that call the internal API (it
refuses every call without it), and `CONNECTIONS_EVENTS_QUEUE_URL` to hear about
deleted organizations, members and teams.

Unit tests need nothing else. The store, application and end-to-end tests need
PostgreSQL. Each migrates a throwaway schema and drops it afterwards, so they
are safe to point at a shared development database. The end-to-end tests run
the HTTP API, the store, envelope encryption, service identity and the provider
client against fake auth and a fake GitHub:

```sh
go test ./connections/... ./internal/serviceauth
CONNECTIONS_TEST_DATABASE_URL='postgres://oryxa:oryxa-dev-password@localhost:5435/oryxa?sslmode=disable' go test ./connections/...
```

`TestLiveGitHubPersonalToken` runs the same flow against the real GitHub when
`CONNECTIONS_LIVE_GITHUB_TOKEN` also holds a personal access token: it connects
the token, has a service request it, and calls GitHub with it through the
generated clients in `internal/providerapi/github`. It only reads from GitHub
and never prints the token.

## Later

- Custom providers from OpenAPI specs, called through the same hardened client.
- Oryxa-managed apps, so an organization can connect without registering its own OAuth client.
- Slack token rotation, and Google domain-wide delegation and other service-account grants.
