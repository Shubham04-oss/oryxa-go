# Apps service

A member's third-party apps — GitHub, Gmail, Slack, Notion, and the rest of
Composio's catalog — and the safety layer around them: the connections a member
makes, the tools an agent may run through them, the events those apps deliver,
and the `oryxa.apps` events the rest of Oryxa acts on. It mounts into the
single `oryxa` binary like every other service — see
[Mounting it](#mounting-it) and [Run locally](#run-locally).

Apps replaces the connections service, which was archived on 2026-09-17 at tag
`archive/connections-and-integrations-2026-09-17`. A connection is made through
one of two **brokers**. [Composio](https://composio.dev), the default, holds
its credentials, refreshes them, and runs the calls. **Oryxa's own sign-in**,
switched on per toolkit and off everywhere today, keeps them here, sealed — see
[Oryxa's own sign-in](#oryxas-own-sign-in). Either way, what Oryxa keeps is the
part no vendor can decide — who may use which connection, which tools may run
unconfirmed, and what is announced when something happens.

```text
member ── connect an app ──▶ apps ── hosted link ──▶ Composio ──▶ the app
                              │
agent ── MCP ─────────────────┤
                              ├── GET auth /v1/organizations/{org}
                              └── oryxa.apps ──▶ the bus ──▶ agents, knowledge

the app ── event ──▶ Composio ── signed webhook ──▶ apps
auth ── organization deleted, member removed ──▶ apps-events queue

member ── connect with Oryxa ──▶ apps ── authorization URL ──▶ the app
the app ── code ──▶ browser ──▶ app.oryxa.in/v1/apps/oauth/callback ──▶ apps
the app ── webhook ──▶ api.oryxa.in/v1/apps/hooks/{endpoint} ──▶ apps

Slack ── @-mention, click ──▶ api.oryxa.in/v1/apps/slack/{events,interactivity} ──▶ apps
apps ── apps.slack.message.received.v1 (ids only) ──▶ the bus ──▶ agents
agents ── internal API, purpose slack ──▶ apps ── posts, updates ──▶ Slack
```

Beside all that, apps is the gateway of **Oryxa's Slack bot**: one Slack app
that workspaces install, through which people ask Oryxa a question by mentioning
it and owners answer with three buttons. It is off unless `APPS_SLACK_SURFACE`
is set, and is described in [The Slack surface](#the-slack-surface).

## Composio holds the credentials, apps holds the rules

Composio answers to one project API key that reaches **every** user in the
project. Apps reads it from its own environment and never passes it on: not to
a caller, not to an agent, not into an event, because anything holding it could
act as anyone. Knowledge holds a copy of its own today, which is a gap, not the
design — see [Reading an app's own API](#reading-an-apps-own-api).

So nothing outside this service chooses whose account a call uses. Every call
asks auth who the caller is, and derives the Composio user from that:

```text
user_id = oryxa.{organizationID}.{memberID}
```

A member reaches only their own connections. An org admin can see the
organization's and remove them, never act through them. An agent acts as the
member whose session it runs in, and may not connect or remove an app at all.
Every primary and specialist agent owned by that member therefore resolves the
same connection. For now there is one pending or active connection per member,
organization and app; account assignment and multiple connections to one app
are deliberately deferred.

| Refused | Because |
| --- | --- |
| An agent connecting or removing an app | Standing access to a member's account is the member's decision |
| A tool run through another member's connection | A connection is not a shared credential |
| A write through `apps_run_read_tool` | Composio's own read-only mark decides, and the server enforces it |
| A write without an idempotency key | A retry must not become a second change |
| A webhook whose signature does not verify | Nothing in the body is read at all |
| An app needing an OAuth app of Oryxa's own, through Composio | Composio has none for it, so the member would reach a broken page; Oryxa's own sign-in is the route for it once switched on |
| An agent replacing a key or changing an organization's OAuth app | The same: those are a member's and an admin's decisions |
| Anything through an Oryxa connection on a Composio path | Composio holds no account of it; its tools arrive with phase 2 |

When an organization applies `tools.offer` in `oryxa.yaml`, apps reads the
last applied workspace version before connecting, running tools, reading an
app, creating subscriptions, and publishing trigger deliveries. Missing
`offer` allows the catalog; `offer: []` allows none. Existing connections stay
visible so members can disconnect them, but a restricted app cannot act or
deliver events. `GET /v1/organizations/{organizationID}/apps/offer` gives a
member the current list and auto-subscribe default so the web catalog offers
only allowed apps. The deployment's `APPS_AUTO_SUBSCRIBE=false` remains a
global off switch; `tools.autoSubscribe` sets the default for new connections
within it, and an explicit per-connection choice takes precedence.

## Connecting an app

Composio's hosted flow does the work. Apps asks for a link, records the
connection as pending with the **hash** of an opaque state, and sends the
member to Composio. When Composio returns them, the callback's query is only a
hint: the connected account is read back from Composio and must belong to the
member and app the connection was started for. A callback that names anything
else fails the connection and spends its state, so the same trick cannot be
tried twice.

Composio sometimes redirects a moment before the account is active, so the
callback reads it back up to five times, a second apart. A connection still
initiating stays pending and can be finished later; the maintenance sweep fails
one whose member never came back after 24 hours.

Starting another connection to the same app while one is pending or active is
refused before another provider flow is created. Failed, expired and removed
connections remain as history but release the slot, allowing the member to
retry or reconnect.

The project's auth config for an app is found or made the first time anyone
connects it: Composio's own app where Composio has one, otherwise a scheme
whose credential the member types into Composio's page (`API_KEY`,
`BEARER_TOKEN`, `BASIC`). An app that would need an OAuth app of Oryxa's own is
refused as not connectable rather than half-connected.

## Oryxa's own sign-in

The second broker. It is `docs/design/integrations-plan.md`'s phase 1: one
sign-in and token system for every credential kind (`apps/internal/signin`)
and one webhook ingress (`apps/internal/hooks`), wired in here. A connection
made through it has `broker: "oryxa"`; its credential lives in
`apps_credentials`, each secret value sealed on its own with the KMS key in
`APPS_SIGNIN_KMS_KEY_ID`, bound to `service=oryxa-apps`, the resource type and
the resource id. Composio's connections, routes and events are untouched.

**It ships switched off.** A toolkit takes this route only once
`APPS_ORYXA_TOOLKITS` names it, which happens at the plan's gate G4. With the
list empty every Oryxa request is refused as `not_connectable`, and the web
app keeps connecting everything through Composio. A connection already made
through Oryxa can still be removed, checked and revoked for as long as
`APPS_SIGNIN_KMS_KEY_ID` is set.

### Connecting

`POST …/apps/connections` with `"broker": "oryxa"`. The toolkit must be
switched on and registered, and `kind` picks the manifest's way in
(`oauth2_code`, `api_token`, `basic`, `oauth2_client_credentials`; empty is
the manifest's first). The one-current-connection rule holds across brokers:
a member with a pending or active Composio connection to an app cannot start
an Oryxa one to it, and the other way round.

- **OAuth.** The pending connection is recorded, `signin.StartOAuth` makes
  the flow (32 random bytes of state, only its SHA-256 kept, PKCE S256, ten
  minutes, single use), and the answer is
  `{"connection": …, "redirect_url": "<the app's authorization URL>"}`, exactly
  as a Composio connect answers its link. The same answer sets the **state
  cookie**: `oryxa_apps_signin_<hash>` = the state, `HttpOnly; Secure;
  SameSite=Lax; Path=/v1/apps/oauth/callback; Max-Age=600`. Its name carries
  a hash of the state, so two sign-ins started in one browser each keep
  their own.
- **A key, a password or client credentials.** `fields` carries the kind's
  standard fields (`token`; `username` and `password`; `client_id` and
  `client_secret`) and the manifest's connection-level ones. They are proved
  with the manifest's test call and kept sealed at once, so the answer is
  `{"connection": …}` already `active`. A key the app refuses fails the
  connection and answers `invalid_credentials`, naming the field, never its
  value.

The member comes back to `GET /v1/apps/oauth/callback`. The callback reads
the state cookie, clears it, and has signin check it against the state,
spend the flow, exchange the code and run the test call. If the browser
carries an access cookie, it must belong to the member and organization that
began the sign-in, or the sign-in fails as `wrong_account`. The connection
becomes active (labelled with the account the test call named) or failed, and
the browser is sent to `return_to` with **exactly** `app_connection` and
`status`, as the Composio callback does, so the web app's
`parseConnectReturn` reads either. Without a return page it answers the
connection as JSON. A state that is missing, not the cookie's, spent or
expired is an error: `forbidden` or `not_found`.

**The cookie comes back only to the host that set it.** The state cookie is
set on the connect answer, so `APPS_SIGNIN_REDIRECT_URI` must be on the host
the browser sends the connect request to. web-next calls apps on its own
origin — `app.oryxa.in/v1/…` goes through the Cloudflare tunnel to the tasks —
so production's callback is `https://app.oryxa.in/v1/apps/oauth/callback`,
not the API host Composio's callback uses. Apps refuses to start with a
redirect URI whose path is not `/v1/apps/oauth/callback` or whose host is
neither an `APPS_ALLOWED_ORIGINS` host nor `APPS_PUBLIC_BASE_URL`'s, and
refuses an OAuth connect sent to any other host than the redirect URI's
(ports aside: a cookie is a host's), since that sign-in could never finish.
That check reads the request's `Host`, which cloudflared forwards unchanged:
the tunnel's ingress sets no `httpHostHeader`.

Every connection's view carries `broker` (`composio` or `oryxa`) and, for an
Oryxa connection holding a credential, `credential_kind`, which says whether
it has a key to replace.

A key is replaced with `PUT …/apps/connections/{id}/credentials`
(`{"kind"?, "fields"}`), by the connection's own member only: the new one is
kept only once the app accepts it in the test call, the connection's label
follows the account it names, and `apps.credential.replaced.v1` says so. An
OAuth connection, or an expired one, is connected again instead.

Pending sign-ins that never come back are failed as `abandoned` fifteen
minutes after they began (the flow lasts ten), which frees the member's slot.

A failed or expired Oryxa connection's `status_reason` is in a member's words,
never an app's: `the app did not grant access`, `the sign-in came back to
another member`, `the app did not answer`, `there is no OAuth app to sign in
through`, `link expired`, a field's own rule such as `token was refused by
the app`, and for an expired one `the app no longer accepts the sign-in` or
`the app no longer accepts the credential`.

### What an Oryxa connection can do today

Only exist, be listed, be removed. Its integration's tools, readers and
triggers are phase 2, and every Composio path refuses it rather than calling
Composio with an account Composio never had: running or searching tools, the
read proxy, targets and triggers answer `not_connectable` ("… not available
yet"), and auto-subscribe never claims it.

### Disconnecting

Removing an Oryxa connection — by the member, an admin, or with the member
(`auth.member.removed`) or the organization (`auth.organization.deleted`) —
has signin revoke the credential at the app in the manifest's revoke style,
deletes the connection's webhook endpoints, and records it removed.
`apps.connection.removed.v1` carries `provider_revoked`: `true` when the app
confirmed it, `false` for a key or a password (only its owner can revoke
those), a grant another connection still shares, or an app that did not
answer. That last kind never blocks the removal: signin keeps the sealed
credential and tries again, backing off, until the app answers or eight
attempts have passed. A deleted organization's own OAuth clients are retired
and its own webhook endpoints deleted with it.

### Organizations' own OAuth apps

`GET|PUT|DELETE /v1/organizations/{org}/apps/oauth-clients/{client}` is the
organization's own OAuth app for a registration (`github`, `slack`,
`notion`, `google` — shared across toolkits), for its admins only, never an
agent. The secret is write-only: what comes back is `secret_set` and, for a
secret of 16 characters or more, its last four characters. `refuse_default`
is the "don't use Oryxa's app" switch. The view also carries what an admin
copies into their app: `redirect_uri`, and for apps whose deliveries are the
whole app's (Slack, Notion) the URL of an endpoint of the organization's own.
Slack's signing secret is entered as `webhook_secrets: {"slack": "…"}`, which
makes that endpoint or rotates its secret (the old one verifies for a further
24 hours); Notion sends its own verification token to the endpoint once, and
an admin reveals it once, with `POST …/apps/webhooks/notion/verification-token`,
to paste back into Notion. Saving and removing are announced as
`apps.oauth_client.saved.v1` and `apps.oauth_client.removed.v1`, never with a
secret. Removing a client retires it; connections made through it go on
refreshing and revoking through it.

Oryxa's own OAuth apps — what a member signs in through unless their
organization brings its own or refuses it — come from
`APPS_DEFAULT_OAUTH_CLIENTS`, which the upkeep saves, sealed, when apps
starts. Changing a secret there saves it over the old one; changing a client
id makes a new live client, and connections made through the old one keep
refreshing and revoking through it.

### The form the web app renders

`GET /v1/apps/integrations` lists the toolkits switched on, with the kinds each
signs in with; `GET /v1/apps/integrations/{toolkit}/manifest` is one toolkit's
sign-in as a form: each method's kind, the OAuth client registration and
scopes, and every field with its label, help, `secret`, `level`
(`connection` for the member, `client` for an admin, `discovered` from the
app) and validation, the standard ones marked `standard`; the `redirect_uri`;
and whether its webhook needs an organization's endpoint, secret or
verification token. Neither needs a caller, and neither carries a secret, an
app's URLs, or where a value goes on a request.

### The webhook ingress

`POST /v1/apps/hooks/{endpointID}` takes every delivery Oryxa's own
integrations' apps push. The id is 120 random bits and the only thing in the
URL; an unknown one is 404 before the body is read; each endpoint takes 50
deliveries a second per replica; bodies stop at 1 MiB; the manifest's scheme
verifies each one in constant time before anything in it is used (401
otherwise); handshakes (Slack's `url_verification`, GitHub's ping, Graph's
validation, Google's sync, Notion's token) are answered by the core; and
deliveries are deduplicated for 7 days in `apps_handled_events`. Until
Oryxa's integrations have triggers (phase 2), no subscription is found for a
verified delivery, so it is acknowledged, recorded and publishes nothing.

An endpoint is a connection's (made for one subscription, secret generated),
an organization's (its own app; secret entered by its admin) or the
deployment's (Oryxa's own app for everyone; secret in `APPS_HOOK_SECRETS`,
current and previous during a rotation). The deployment's endpoint for a
toolkit, and a token its app sent it, are an operator's, served on apps'
internal routes to a service token alone, never to a member:

```text
GET  /v1/internal/apps/hooks/{toolkit}                     the URL to put in Oryxa's own app, made the first time
POST /v1/internal/apps/hooks/{toolkit}/verification-token  Notion's token, once
```

They exist only where `APPS_SERVICE_CALLERS` names the services allowed to
call them, each signing an `X-Oryxa-Service-Token` for audience `apps` with
purpose `hooks` (`internal/serviceauth`).

### Limits

Per replica, with teams' token bucket: **10 a minute per member** for
connecting (either broker) and replacing a key, and **30 a minute per
address** for the OAuth callback, the address being Cloudflare's
`CF-Connecting-IP`. Over either, `429 rate_limited`. A Cloudflare rule on
the callback and hook paths belongs in front of both.

### What runs in the background

`RunOryxa`, a worker of its own wherever `APPS_SIGNIN_KMS_KEY_ID` is set,
every minute on every replica (signin claims each check and revocation
first):

| Does | So that |
| --- | --- |
| Saves `APPS_DEFAULT_OAUTH_CLIENTS` until each has been saved once | Oryxa's own apps are there to sign in through |
| Fails pending Oryxa sign-ins older than fifteen minutes, as `abandoned` | A member who never came back can connect again |
| Runs the daily health checks that are due (`signin.CheckDue`) | A connection the app refuses expires, announced with `refresh_rejected` or `credential_rejected` |
| Tries again the revocations an app did not answer (`signin.RetryRevocations`) | A removed connection's credential is revoked, or forgotten after eight attempts |
| Deletes webhook endpoints whose subscriptions lapsed (`hooks.SweepExpired`) | A lapsed endpoint answers as an unknown one |

## The Slack surface

One Slack app, installed by a workspace for one organization. Slack carries
three things and nothing else: the **entry** (an @-mention of the bot), the
**owner's notice** (a direct message with three buttons) and the **answer**.
Everything that decides anything happens in agents, which reads what arrived
from apps and asks apps to post. Apps is the gateway and keeps four
promises: it answers Slack inside its three seconds without calling anything;
it puts ids and nothing else on the bus; it is the only place the bot's token
exists; and it says only what its templates let it say.

It is off unless `APPS_SLACK_SURFACE=true`. Off, no route below is served, no
worker runs and none of its variables is read, so a deployment that has not
adopted it is exactly what it was. It is no integration: nothing about it is in
the registry that members' own Slack sign-in (`integrations/slack`) belongs to,
and the two Slack apps share nothing. The design is
[`docs/design/slack-consent-loop.md`](../docs/design/slack-consent-loop.md);
the code is `internal/slackbot`, with `internal/hooks` in front of it and
`internal/postgres/slack.go` behind.

### The Slack app is code

`internal/slackbot/manifest.yaml` is the app's manifest and the only way its
scopes, events and URLs change. `manifest_test.go` pins every value in it, and
holds `slackbot.BotScopes` (which the Add to Slack link asks for) and
`slackfake`'s default scopes to it, so they cannot drift.

- **Bot scopes**, each with its reason in the file: `app_mentions:read`,
  `chat:write`, `im:write`, `users:read`, `users:read.email`, `channels:read`,
  `groups:read`. No user scope, no channel-history scope: the bot reads only
  messages that mention it.
- **Events** (`bot_events`): `app_mention`, `app_uninstalled`, `tokens_revoked`.
  Interactivity on. Messages tab read-only, no Home tab. Org deploy, socket mode
  and **token rotation off**: the install refuses a token that rotates, because
  Slack turns rotation on for good and nothing here refreshes a token yet.
- **URLs**, under `https://api.oryxa.in` (production's `APPS_PUBLIC_BASE_URL`):
  `/v1/apps/slack/events`, `/v1/apps/slack/interactivity`, and the OAuth
  redirect `/v1/apps/slack/install/callback`. A deployment on another host
  changes the manifest and its test together.

Slack does not check a request URL when an app is created from a manifest, so
the owner verifies both in the app's settings after the first deploy. The
owner's steps: create the app from the manifest; put its Client ID, Client
Secret and Signing Secret in Secrets Manager as the JSON `APPS_SLACK_APP`
reads; deploy with `APPS_SLACK_SURFACE=true`; verify the two URLs; activate
public distribution.

### Ingress

`POST /v1/apps/slack/events` and `POST /v1/apps/slack/interactivity` are static
aliases of hooks' ingress for the toolkit `slackbot`, whose deployment endpoint
is made the first time either is asked (hooks' `Surface` port, `hooks.Alias`).
Hooks verifies the delivery exactly as it does an integration's, with the v0
scheme (`X-Slack-Signature`, `v0:{timestamp}:{body}`, hex HMAC-SHA256, five
minutes' tolerance), the signing secret from `APPS_SLACK_APP`, and answers
Slack's `url_verification`. Only then does `slackbot` see it.

- **Taken:** `app_mention`, `app_uninstalled`, `tokens_revoked`, and a click on
  a button (`block_actions`, form-encoded with the JSON in `payload`). Anything
  else is acknowledged and dropped, as is a delivery for a workspace with no
  active install, a mention by the bot itself, and one whose ids are not Slack's.
- **Deduped** on the event's `event_id`, or for a click on the SHA-256 of its
  body. A retry, a replay and a race are one delivery.
- **Recorded in one transaction:** the dedupe mark, an `apps_slack_inbound` row
  (or, for an uninstall, the install ended) and the outbox event. Then 200.
- **The ack path calls nothing.** No Slack call, no KMS call, no other service,
  and the database gets a **2 s deadline** (`hooks.DefaultSurfaceDeadline`).
  When it cannot record in time the answer is 503 and nothing is marked, so
  Slack's retry (at once, after a minute, after five) is handled.
- `app_uninstalled` ends the install, and so does `tokens_revoked` when it names
  the bot's own user (a revoked user token is no concern of a bot that holds
  none). Either may arrive first; the second finds nothing to end.
- A mention is `channel_id`, `user_id`, its `ts`, its thread's root
  (`thread_ts`, or its own `ts` when it started one) and the people it names.
  Text and tickets are in the record and nowhere else: not in the event, not in
  a log line.

### The events

Published on `oryxa.apps`, ids only (`contracts/events/apps/v1/slack.go`):

| Detail type | Data |
| --- | --- |
| `apps.slack.message.received.v1` | `inbound_id`, `organization_id`, `team_id`, `received_at` |
| `apps.slack.action.received.v1` | the same four |
| `apps.slack.install.changed.v1` | `organization_id`, `team_id`, `state` (`active` or `ended`), `reason`, `actor_id` when a member did it, `changed_at`. Reasons: `installed`, `reinstalled`, `app_uninstalled`, `tokens_revoked`, `removed_by_admin`, `token_invalid` |

### Installing

An organization's owner or admin (a member's session, never an agent's):

| Route | Does |
| --- | --- |
| `GET /v1/organizations/{org}/apps/slack/install` | The install's status (`not_installed`, `active`, `ended`, with the workspace, bot user, scopes and dates) and `add_to_slack_url`, Slack's OAuth v2 consent page asking for the bot scopes, with a **single-use state** good for ten minutes. `?return_to=` names a page under `APPS_ALLOWED_ORIGINS` for the browser to come back to |
| `GET /v1/apps/slack/install/callback` | Where Slack sends the browser. Spends the state, exchanges the code, checks the token with `auth.test`, seals it, and answers JSON or, with a `return_to`, redirects there with `slack=connected` or `slack=error&slack_error={reason}` |
| `DELETE /v1/organizations/{org}/apps/slack/install` | Disconnect: `apps.uninstall` at Slack (best effort), the token discarded, the install ended |
| `GET /v1/organizations/{org}/apps/slack/channels` | The private channels the bot is in, each with whether it can be marked, and the ones marked |
| `PUT /v1/organizations/{org}/apps/slack/channels` | `{"high_priority": ["G…", …]}` makes exactly those the high-priority channels. Each must be a **private channel shared with nobody**, checked with Slack now; otherwise 400 and nothing changes |

The state binds the install to the organization and to the admin who asked; only
its hash is kept. An install is refused, with nothing kept and no token
revoked, when the workspace is **active for another organization** (409
`conflict`, reason `team_bound`), when the organization **already serves another
workspace** (409, `organization_bound`), for an Enterprise Grid workspace
(`enterprise`), a token that rotates (`rotation`), an install not granted every
scope (`scopes`; 422 `not_connectable`), and when Slack refuses or cannot be
reached (`slack_refused`, `unavailable`). A workspace whose install ended can be
installed again, by the same organization or another. The bot token is sealed
with `APPS_SIGNIN_KMS_KEY_ID`, bound to `service=oryxa-apps`, resource type
`slack_install` and the team id.

The link's state is bound to the admin who asked, not to their browser: the
callback lands on `APPS_PUBLIC_BASE_URL`, a host the web app's session cookie
does not reach. Its lifetime, single use and organization binding are what
limit it.

### The internal API

For services with the purpose `slack`: a service token
(`X-Oryxa-Service-Token`, audience `apps`, purpose `slack`, `internal/serviceauth`)
from a caller `APPS_SERVICE_CALLERS` allows that purpose. A token for another
purpose is 403 here, and a token for `slack` is 403 on the deployment-endpoint
routes. Only apps knows the bot token.

`GET /v1/internal/apps/slack/inbound/{id}` — one record, until it is forgotten
(7 days; then 404):

```json
{"id":"asi_…","organization_id":"…","team_id":"T…","kind":"message","received_at":"…",
 "channel_id":"C…","user_id":"U…","message_ts":"…","thread_ts":"…","bot_user_id":"U…",
 "event_id":"Ev…","text":"<@Ubot> ask <@Uraya> …","mentions":["Uraya"],"channel_type":"…","ext_shared":false}
```

For a click, `kind` is `action` and `message`'s fields are replaced by
`action_id` (`consent_summary`, `consent_access` or `consent_deny`) and
`action_value`, the button's opaque ticket. `message_ts` and `thread_ts` are
the message that was clicked. `mentions` leaves the bot out; `channel_type` and
`ext_shared` are there only when Slack said.

`POST /v1/internal/apps/slack/profiles` — `{"organization_id","user_ids":[…]}`
(one to twenty) answers `{"profiles":[{"user_id","team_id","email","email_confirmed",
"name","title","is_guest","is_bot","is_deleted","is_stranger","fetched_at"}],
"missing":[…]}` from a 24-hour cache. `email_confirmed` is Slack's
`is_email_confirmed`, false when Slack does not say: only a confirmed address
is one to match a person to a member by. `is_stranger` is Slack's flag or a
`team_id` that is not the workspace's; `missing` is people Slack does not know.

`GET /v1/internal/apps/slack/channels/{id}?organization_id=` — read from Slack
now, every time: `{"channel_id","name","is_private","is_shared","is_mpim","is_im",
"is_archived","high_priority"}`. `is_shared` is every kind of sharing. A
conversation the bot's scopes cannot describe (a group message needs
`mpim:read`, which the app does not ask for) is 422 `channel_unreadable`, and
a channel Slack does not know is 404.

`POST /v1/internal/apps/slack/post`:

```json
{"organization_id":"…","idempotency_key":"…8 to 128 characters…",
 "target":{"channel_id":"C…","thread_ts":"…","user_id":"U…"},
 "template":"answer","params":{…}}
```

answers `{"channel_id","ts"}`, and `"ephemeral":true` for one only a person sees,
`"replayed":true` for a key already used. The target is one of a **thread**
(`channel_id`, `thread_ts`), a **person in a thread** (adding `user_id`: an
ephemeral message) or a **person** (`user_id` alone: a direct message, opened
first). Posts go only where people opened a conversation with the bot: the
thread must be one a verified message or click was in, and the person one who
wrote or was named in one, within the last 7 days (403 `forbidden` otherwise).
Naming a thread by any message in it posts to its root.

`POST /v1/internal/apps/slack/update` —
`{"organization_id","channel_id","ts","template","params"}` replaces a message
apps posted (and only that; not an ephemeral one) with `chat.update`, always
with blocks, so a decided notice loses its buttons. Answers `{"channel_id","ts"}`.

Failures: 400 `invalid_request` (params, target or a template that may not go
there), 403 `forbidden` (a conversation never seen), 404 `not_found`, 409
`not_connected` (no active install, or Slack said the token is dead, which
also ends the install as `token_invalid`), 409 `conflict` (a key reused for a
different message, or still being posted), 422 `channel_unreadable`, **429
`rate_limited` with `Retry-After`**, 502 `slack_error` (Slack refused; the
message names its code), 503 `unavailable`. A failed post gives its key back,
so a retry with the same key posts.

**Rate limits.** Each organization has a token bucket of 3 posts a second with
a burst of 15 (per replica); over it, 429 with the wait. Slack's own
`ratelimited` (HTTP 429 with `Retry-After`, or HTTP 200 with that error) is
passed on the same way, with its `Retry-After`, and the key is free to try
again.

### Templates

Every message is a template rendered here from typed parameters, with link and
media unfurling **off**: a caller supplies text and ids and never Block Kit, so
nothing but a template's own structure reaches Slack. Every text is escaped for
Slack's markup (`&`, `<`, `>`), so it cannot become `@channel`, a mention or a
link; scrubbed of anything that looks like a credential (Slack, AWS, GitHub and
Google keys, private keys, JWTs, `password: …`) and of the app's own secrets,
which become `[credential removed]`; and cut to length. Nothing has a place for
a secret, and none is ever shown. Parameters with a field a template does not
have are 400.

| Template | Goes to | Params | Says |
| --- | --- | --- | --- |
| `owner_notice` | a direct message | `asker_user_id` or `asker_name`; `question`; `tickets` `{summary, access, deny}` | The asker, the question, and three buttons: **Share AI summary only**, **Give access for 1 hour**, **Deny**, whose values are the tickets, passed along and never read or logged. Nothing else |
| `notice_settled` | a direct message | asker; `question`; `decision` (`approved_summary`, `approved_access`, `denied`, `answered_by_other`, `expired`, `withdrawn`); `decided_by_user_id` or `_name`; `decided_at` | The same asker and question, no buttons, and "Approved by … at …". For `chat.update` |
| `ask_ack` | a thread or one person in it | `owners` `[{user_id\|name}]` (1–5); `open_url` (a page of the web app, or nothing) | "Asked Raya and Maya · Open in Oryxa" |
| `answer` | a thread or a direct message | `text`; `owner_name`; `mode` (`summary`, `access`, `lease`, `bypass`) | The text, and "From Raya's agent · approved by Raya (AI summary only)". Names the owner and never mentions them |
| `status` | a thread, one person in it, or a direct message | `state` (`waiting`, `working`, `held`, `denied`, `expired`, `answered_elsewhere`); `owner_name` | One content-free line, such as "The answer is held for Raya's review." |
| `refusal` | a thread or one person in it | `reason` (`not_member`, `owner_not_member`, `group_dm`, `external_channel`, `no_owner`, `too_many_asks`; another is a plain no); `organization_name`; `user_id` or `name` | Fixed wording, never the question |
| `private_note` | a thread or one person in it | none | "Answered privately." |
| `bypass_notice` | a direct message | asker; `question`; `reason` (`priority_channel`, `org_chart`); `channel_id` | Tells an owner at once that their agent answered without asking |

### Retention

An inbound record, with what was typed and clicked, is forgotten **7 days**
after it arrived (and a thread with it: apps posts nowhere it cannot show it
saw). Profiles are used for a day and forgotten after a week; posts are
remembered for 30 days, for their keys and for updates; a lapsed install state
is gone within the hour. Dedupe marks go with the rest of `apps_handled_events`
after 7 days. `RunMaintenance` does the sweep, every 15 minutes, in a worker
of its own that exists only with the surface on.

### Settings

| Variable | Is |
| --- | --- |
| `APPS_SLACK_SURFACE` | `true` switches it on. Off unless set |
| `APPS_SLACK_APP` | The app, JSON `{"client_id","client_secret","signing_secret"}`, from Secrets Manager. Required with the surface |
| `APPS_SLACK_API_BASE_URL` | Slack's Web API, for a fake only: **loopback only**, refused otherwise. Empty is Slack itself |

With the surface on, apps also needs `APPS_SIGNIN_KMS_KEY_ID` (it seals bot
tokens), an `APPS_SERVICE_CALLERS` caller allowed the purpose `slack`, and an
`APPS_PUBLIC_BASE_URL` Slack can reach (https, or http on loopback); it refuses
to start without them. The `oryxa connect slack` command the design imagines
would need `GET …/apps/slack/install` added to `internal/cliscope`'s allowlist.

`internal/slackfake` is a fake Slack for tests, including other services': it
serves the Web API methods the client calls on a loopback address (point
`APPS_SLACK_API_BASE_URL` at `fake.URL()`), records every call and the messages
posted, answers any error or a 429 on demand, and builds the signed mentions,
clicks, uninstalls and `url_verification` requests Slack sends.

## Running tools

Composio's catalog runs to thousands of tools, far more than a model can hold,
so nothing lists them all. A caller searches one app's tools, then runs one.

Composio marks each tool with the same hints MCP defines. A tool marked
`readOnlyHint` may run unconfirmed; everything else is a write, needs an
idempotency key, and — through the agent toolbox — the member's confirmation. A
tool with no mark is treated as a write.

A write's answer is kept, so the same key asked again answers what happened the
first time instead of doing it twice. A run still in flight under that key is
refused rather than repeated: whether it reached the app is not known. A read
keeps nothing and is simply run again.

A tool that ran and failed **at the app** is an answer, not an error: the
caller, usually a model, needs the app's own reason to try again differently.
The API answers 200 with `successful: false`; MCP answers a failed result whose
code is `app_error`.

## MCP

Agents reach apps over MCP, at `POST /v1/mcp/apps` with the organization in an
`X-Oryxa-Organization` header, or at
`POST /v1/organizations/{organizationID}/apps/mcp`. The endpoint is stateless —
the 2026-07-28 protocol keeps no session — so any replica may answer any
request, and every request carries its own member.

| Tool | Does |
| --- | --- |
| `apps_connections` | The member's connected apps, with each app's status and account label |
| `apps_search_tools` | One app's tools, with what each takes and whether it only reads |
| `apps_run_read_tool` | Runs a tool Composio marks read-only; refuses anything else |
| `apps_run_write_tool` | Runs a change, with an idempotency key |

A client the SDK builds needs nothing more than the URL and the headers above.
One written by hand does: the stateless protocol carries what a session used to,
so every request needs `Mcp-Protocol-Version: 2026-07-28`, `Mcp-Method` naming
the JSON-RPC method, `Mcp-Name` naming the tool for a `tools/call`, and
`params._meta` carrying `io.modelcontextprotocol/protocolVersion` and
`io.modelcontextprotocol/clientCapabilities`. Without them the endpoint answers
a JSON-RPC error saying which is missing.

Failures come back as codes a model can act on rather than prose:
`not_connected`, `needs_reconnect`, `not_read_only`,
`app_error`, `rate_limited`. Krude's side of this is
`agents/primary/internal/agents/apps.go` and its skill in
`toolboxes/apps.md`.

## Reading an app's own API

A tool run answers what a tool returns. Paging through what an app *holds* —
a repository's files, a channel's history, a workspace's pages — is a different
shape, so `POST /v1/organizations/{org}/apps/read` proxies one request to the
app's own API through the caller's connection, with Composio placing the
credential.

Only reads pass. Slack RPC methods use an explicit read list for GET, HEAD and
POST: HTTP GET can mutate state there, including token revocation. Other apps
allow GET and HEAD; POST is limited to known read APIs such as Notion search
and database queries. Anything else is refused as `not_read_only`, because a change
is a tool run: recorded, keyed, and confirmed by the member. An endpoint must
be a path under the app's own API — another host, a relative path or one
climbing out with dot segments is refused — and `Authorization`, `Cookie`,
`Host` and `X-API-Key` are not headers a caller may set.

**Knowledge does not use this yet.** Its provider sources read Composio's proxy
directly, with `COMPOSIO_API_KEY` of their own, because a scheduled pass has no
member to act as: this route decides the account from who is asking, and nobody
is. So the project key is in two services today rather than one. It moves
behind this route once knowledge can mint a member token under a grant — the
same thing its files source waits on
([docs/REMAINING-WORK.md §3.2](../docs/REMAINING-WORK.md#32-what-a-member-agrees-to-when-knowledge-reads-files-as-them)).

## What a connection holds

Indexing a repository, a channel or a database used to mean knowing its
identifier — a Slack channel id, a Notion database id — and typing it into a
box that could not tell a wrong one from a right one until the first pass
failed. The connection can simply be asked:

```text
GET /v1/organizations/{org}/apps/connections/{id}/targets
```

It reads the app's own API through the same proxy as everything else, and
answers what a member could index, each with the `target` knowledge registers
a source with — so a caller passes it on rather than assembling it.

| App | Lists | From |
| --- | --- | --- |
| `github` | Repositories, most recently pushed first, archived ones left out | `GET /user/repos` |
| `slack` | Channels it is in, by name rather than id | `GET /api/users.conversations` |
| `notion` | Databases, one per data source found and de-duplicated by parent database (a query reads a data source since the 2025-09-03 split, but a database id is what knowledge's extractor takes) | `POST /v1/search` |
| `googledrive` | The whole of the account's Drive (always, no read needed) plus any shared drive it can see | `GET https://www.googleapis.com/drive/v3/drives` |
| `gmail` | `INBOX` and `SENT` (fixed; no read at all — knowledge's own default is already `INBOX`) | Nothing asked |

An app nothing can be indexed for answers an empty list rather than an error:
that is a fact about the app, not a failure, and a picker wants to say
"nothing to index here". An app that refuses the read — a missing scope, a
credential it no longer likes — says so as `unavailable`, since an empty
workspace and a refused one are not the same answer. Google Drive is the one
exception: a refused or missing shared-drive listing still answers the whole
Drive alone rather than `unavailable`, since My Drive is always there to
offer and a shared drive is only ever the cheap extra.

`googledrive`'s listing is the one call here made with an absolute URL rather
than a path under an assumed base: unlike github, slack and notion, this
project has not proxied a real Drive account through Composio to confirm what
toolkit base URL `googledrive` resolves a relative path against, so an
absolute endpoint (`composio.ProxyRequest.Endpoint` takes either) sidesteps
the question rather than guessing at it.

## Triggers

A member subscribes one of their own active connections to an event its app
delivers, such as `GITHUB_COMMIT_EVENT`. Composio watches the app — pushing
webhooks where the app supports them, polling where it does not — and delivers
to one project-wide subscription:

### Connecting an app subscribes it

A member who connects GitHub means to hear about GitHub, so connecting
subscribes to every event the app can deliver rather than handing them a list
of forty-six and asking which. What it cannot subscribe to is the events whose
schema names a repository, a channel or a database: there is nothing to put
there that would not be a guess at what the member meant, so those are left
for them to add, and named in the log rather than counted.

It does not happen in the callback — a toolkit's events are subscribed one
Composio call at a time, and a member's browser is waiting on that redirect.
The connection records that it is owed and the maintenance worker does it,
which also survives a restart part-way through and can be retried without the
member noticing. A connection carries `auto_subscribe`, `auto_subscribed_at`
and `auto_subscribe_attempts`. `auto_subscribed_at` unset does not by itself
mean still being set up, though — it also means every attempt has been spent
and auto-subscribe has given up, which `auto_subscribe_attempts` reaching the
limit below says, and which is logged as a warning at the moment it happens.

Every subscription that fires wakes Krude, and every wake-up is a run, so
connecting an app has a ceiling on what it can commit someone to:
`AutoSubscribeLimit`, 25 events. Past it the rest are left for the member, the
same as one needing configuration. An app the pass cannot read is tried again
next tick, three times before it is left alone — the member can always
subscribe by hand, and nothing here retries it further.

Two ways to turn it off:

| Off | How |
| --- | --- |
| For one connection | `"auto_subscribe": false` in the connect request |
| For the deployment | `APPS_AUTO_SUBSCRIBE=false` |

These are not the same kind of switch. A connection's own `auto_subscribe` is
recorded either way and is the only thing a member chooses. The deployment's
is a kill switch checked at subscribe time, not a default the connection's
choice overrides: off means nothing is auto-subscribed, whatever any
connection asked for; on, a connection that said no still gets nothing.

Opting out afterwards, rather than at connect time, is per event: remove the
triggers you do not want (`DELETE .../apps/triggers/{id}`). Auto-subscribe
reads a connection's whole trigger history - live or removed - before every
pass, so a slug already subscribed (even with the member's own filters) is
left as it is, and one the member removed is never re-added on a later pass.

### A subscription is checked against the trigger's own definition

Composio accepts a subscription it will never deliver anything for, and there
is no way to tell afterwards: the trigger sits there active and silent. So
every subscription is checked against the catalog's own definition of that
trigger first, and a member finds out here instead of by waiting.

| Refused | Because |
| --- | --- |
| An event the connection's app does not deliver | A GitHub connection cannot be subscribed to `SLACK_RECEIVE_MESSAGE`; it is `not_found`, naming the app that was asked |
| A config missing a field the trigger requires | `GITHUB_COMMIT_EVENT` without `owner` is accepted at Composio and delivers nothing |
| A field whose type contradicts the trigger's schema | Likewise, and the message names the field |
| A value outside a declared enum | Likewise |

Only what is certainly wrong is refused. Composio publishes thousands of
triggers whose schemas use keywords `domain.CheckTriggerConfig` does not
model — `anyOf`, `$ref`, a nested `items` — and a valid subscription refused
is worse than an invalid one passed on, since Composio checks it again anyway.
Anything it cannot judge, it allows.

Two things a member is told rather than refused, because the subscription is
real either way and only the waiting differs:

- **`manual_setup`** — the app needs the webhook Composio issues registered in
  it by hand, as Slack's event subscriptions do. Nothing arrives until someone
  does that. The catalog carries the app's own instructions.
- **`kind: poll`** — the app pushes nothing, so Composio polls and an event
  arrives on its interval rather than at once.

Both are recorded on the trigger, and a trigger answers `setup` saying which
applies in a member's own terms.

### Whether a trigger has ever delivered

A trigger carries `delivery_count` and `last_delivery_at`, written in the same
transaction as the delivery's event, so what a trigger says it delivered and
what reached the bus cannot disagree — and a redelivery counts once. This is
the only way to tell a live subscription from a silent one, which matters most
for the two cases above.

### Checking the whole catalog

The tests prove the pipeline against a trigger of every shape the catalog
holds. They cannot prove the catalog holds only those shapes, so a command
walks the real one and reports every trigger apps could not subscribe a member
to, through the very code the service runs:

```sh
(set -a && . ./.env && go run ./apps/infra/composio/cmd/triggercheck)
TRIGGERCHECK_TOOLKITS=github,slack,gmail go run ./apps/infra/composio/cmd/triggercheck
```

It only reads the catalog — nothing is subscribed and no account is touched —
and exits non-zero if it finds a trigger that would be refused, so it can run
as a check rather than only be read.

The report also counts how much of an app a member gets for nothing:
`auto_subscribable`, the triggers connecting subscribes to unasked, and
`form_fillable`, those whose configuration the web app can put a form to.
What is neither is named in `needs_setup` — triggers a member can subscribe
to only through the API, because a field they require is an object or a list
of objects. `formfill.go` mirrors the web app's own rule
(`web/src/lib/apps/client.ts`, `triggerConfigCapability`) so the measurement
is of what members actually face; the two are meant to agree, and their test
cases are deliberately the same.

```text
POST /v1/apps/webhooks/composio
```

Every delivery is verified before anything in it is read: an HMAC-SHA256 over
`{webhook-id}.{webhook-timestamp}.{body}`, keyed with the subscription's
secret, inside a five-minute window. An unverified delivery is answered 401 and
nothing in it is used — not even to mark it handled.

A verified delivery is recorded with the event it became, in one transaction, so
a redelivery publishes nothing twice. A delivery about something apps does not
know — a trigger since removed, an account never connected here — is
acknowledged and dropped, because Composio would otherwise retry it forever.

## API

Every route needs a member's access token, as a bearer token or the access
cookie, except the catalog, Oryxa's integrations and manifests, the two
callbacks and the two webhook routes. A cookie-authenticated mutation also
needs an `Origin` in `APPS_ALLOWED_ORIGINS`.

| Route | Does |
| --- | --- |
| `GET /v1/apps/catalog` | Composio's apps, with whether each can be connected here |
| `GET /v1/apps/catalog/{toolkit}` | One app |
| `GET /v1/apps/catalog/{toolkit}/tools` | An app's tools |
| `GET /v1/apps/catalog/{toolkit}/triggers` | The events an app can deliver |
| `GET /v1/apps/connect/callback` | Where Composio returns a member's browser |
| `POST /v1/apps/webhooks/composio` | Where Composio delivers events |
| `POST /v1/organizations/{org}/apps/connections` | Start connecting an app; answers the link to send the member to. `auto_subscribe: false` connects without subscribing to its events. `broker: "oryxa"` with `kind` and `fields` connects through Oryxa's own sign-in |
| `GET /v1/organizations/{org}/apps/connections` | The caller's connections, or with `?scope=organization` an admin's view of everyone's |
| `GET /v1/organizations/{org}/apps/connections/{id}` | One connection |
| `GET /v1/organizations/{org}/apps/connections/{id}/targets` | What the connection holds that can be indexed, read from the app itself |
| `DELETE /v1/organizations/{org}/apps/connections/{id}` | Disconnect: Composio deletes the account and revokes its credential |
| `POST /v1/organizations/{org}/apps/tools/{tool}/run` | Run a tool |
| `POST /v1/organizations/{org}/apps/read` | Read the app's own API through the caller's connection — GETs, and the few read endpoints that are POSTs |
| `POST /v1/organizations/{org}/apps/triggers` | Subscribe a connection to an app's event, checked against the trigger's own definition |
| `GET /v1/organizations/{org}/apps/triggers` | The caller's triggers, with how each delivers and whether it ever has |
| `DELETE /v1/organizations/{org}/apps/triggers/{id}` | Unsubscribe |
| `GET /v1/apps/oauth/callback` | Where an app returns a member's browser from Oryxa's own sign-in |
| `GET /v1/apps/integrations` | The toolkits Oryxa's own sign-in is switched on for |
| `GET /v1/apps/integrations/{toolkit}/manifest` | One toolkit's sign-in, as a form |
| `PUT /v1/organizations/{org}/apps/connections/{id}/credentials` | Replace an Oryxa connection's key, password or client credentials |
| `GET/PUT/DELETE /v1/organizations/{org}/apps/oauth-clients/{client}` | An organization's own OAuth app, for its admins |
| `POST /v1/organizations/{org}/apps/webhooks/{toolkit}/verification-token` | Reveal, once, the token an organization's own app sent its endpoint |
| `POST /v1/apps/hooks/{endpointID}` | Where Oryxa's own integrations' apps deliver |
| `POST /v1/apps/slack/events`, `POST /v1/apps/slack/interactivity` | Where Slack delivers, with the surface on — see [The Slack surface](#the-slack-surface) |
| `GET /v1/apps/slack/install/callback` | Where Slack returns an admin's browser from installing the bot |
| `GET/DELETE /v1/organizations/{org}/apps/slack/install` | An organization's Slack install and its Add to Slack link, for its admins; disconnect |
| `GET/PUT /v1/organizations/{org}/apps/slack/channels` | The private channels an admin may mark high priority |
| `/v1/internal/apps/slack/…` | `inbound/{id}`, `profiles`, `channels/{id}`, `post` and `update`, for a service with the purpose `slack` |

Errors are `{"error":{"code","message"}}` with the codes
`invalid_request`, `invalid_credentials`, `unauthorized`, `forbidden`,
`not_read_only`, `not_found`, `not_connected`, `needs_reconnect`,
`not_connectable`, `conflict`, `rate_limited`, `unavailable`, `internal`, and the
Slack surface's `slack_error` (502) and `channel_unreadable` (422).
`invalid_credentials` (400) is a key, a password or a field that broke its
rule or that the app refused; it also carries `"field"`, the field's name,
and never quotes the value.

## Events

Published on `oryxa.apps`, through the outbox, so a change and its event cannot
disagree.

Every change a member's apps go through is announced, so nothing that
happened is visible only in a log line.

| Detail type | Says |
| --- | --- |
| `apps.connection.initiated.v1` | A member started connecting an app and was sent to it |
| `apps.connection.connected.v1` | A member connected an app |
| `apps.connection.failed.v1` | A connection never became usable, and why |
| `apps.connection.expired.v1` | Composio lost a connection's credential; the member must reconnect |
| `apps.connection.removed.v1` | A connection is gone, and why |
| `apps.trigger.subscribed.v1` | A member subscribed a connection to an event, with how it delivers |
| `apps.trigger.unsubscribed.v1` | A member or an org admin took a trigger away |
| `apps.trigger.received.v1` | An app delivered an event, with its payload |
| `apps.trigger.disabled.v1` | Composio stopped a trigger, and why |
| `apps.action.completed.v1` | A write ran |
| `apps.action.failed.v1` | A write did not, in one word — never the app's own |
| `apps.credential.replaced.v1` | A member replaced an Oryxa connection's key, with its kind — never the key |
| `apps.oauth_client.saved.v1` | An admin saved their organization's own OAuth app, or the deployment saved one of Oryxa's (`organization_id` empty, `actor_id` `deployment`) — never its secret |
| `apps.oauth_client.removed.v1` | An admin removed their organization's own OAuth app |
| `apps.slack.message.received.v1` | A verified @-mention of the Slack bot: ids only, the text stays in a record for 7 days |
| `apps.slack.action.received.v1` | A verified click on one of the bot's buttons: ids only |
| `apps.slack.install.changed.v1` | A workspace's Slack install became active or ended, and why |

The connection events of an Oryxa connection carry `"broker": "oryxa"`, and
its `removed` carries `provider_revoked`. Both are left out for Composio's,
whose events are byte for byte what they were
(`contracts/events/apps/v1/testdata/composio.golden`). An Oryxa connection's
`failed` reasons add `invalid_credentials`, `unavailable` and
`not_connectable`, and its `expired` reasons are `refresh_rejected` and
`credential_rejected`.

A connection's life is a closed story on the bus: every `initiated` ends as
`connected`, `failed`, or `removed` when it is removed while still pending,
and a connected one itself ends as `expired` or `removed`. `failed` carries
a constant rather than the prose a member reads, since it is grouped and
counted — `denied_at_provider`, `wrong_account`, or `abandoned` for a member
who started and never came back, which the maintenance sweep announces and
nothing else would report.

A trigger's has four ends: `unsubscribed` when a member or admin takes it
away, `disabled` when Composio stops it, and otherwise it goes with its
connection's `removed`.

A trigger's payload is the app's own data, so it travels only as far as it must:
over 128 KiB it is left out with `data_omitted`, and the outbox blanks it as
soon as the event is published rather than keeping it for the retention window.

Who listens today: agents takes `trigger.received`, recording each as a
wake-up and handing a member's batch to Krude in their updates thread
(`agents/primary/internal/inbound`, `agents/primary/internal/wakeups`);
knowledge takes `connection.expired` and `connection.removed`, pausing and
retiring what read through them. The rest are announcements nothing consumes
yet — published so that whatever needs them next does not have to go back and
add them.

Consumed, from `oryxa.auth`: `auth.member.removed.v1` and
`auth.organization.deleted.v1`. A connection is standing access to a member's
own account, so it must not outlive their place in the organization — both take
every affected connection with them, deleting the account at Composio and
revoking its credential. `apps.Subscriptions()` says what the queue must carry.

## Database

Apps uses `/internal/postgreskit`. Other services never read these tables.

| Table | Holds |
| --- | --- |
| `apps_connections` | A member's connection to an app: its Composio account, status and label, the hash of its pending round trip, and whether it has been subscribed to what the app delivers. A partial unique index permits one pending or active row per member and app |
| `apps_auth_configs` | The auth config the project connects each app with, so two members agree on one |
| `apps_triggers` | What a connection is subscribed to, the Composio instance behind it, how it delivers, and whether it ever has |
| `apps_actions` | Tool runs, with the idempotency key a write can be replayed under |
| `apps_handled_events` | Webhook and bus deliveries already acted on, since both deliver at least once |
| `apps_outbox` | Events written in the same transaction as the change they describe |
| `apps_credentials` | An Oryxa connection's credential: each secret value sealed on its own, its kind, scopes, expiry, the refresh lease and the health-check schedule |
| `apps_oauth_clients` | OAuth clients: organizations' own and Oryxa's (organization `''`), the secret sealed; a replaced one stays for its connections |
| `apps_signin_flows` | OAuth sign-ins in flight, by the state's SHA-256: ten minutes, spent once |
| `apps_hook_endpoints` | Webhook endpoints, each with its scheme frozen at creation and its secret sealed |
| `apps_slack_installs` | A Slack workspace's install, by team id, for one organization: the bot token sealed, status active or ended. One active per organization |
| `apps_slack_install_flows` | Slack installs in flight, by the state's SHA-256: ten minutes, spent once |
| `apps_slack_channels` | The private channels an organization's admins marked high priority |
| `apps_slack_inbound` | What verified deliveries became, by id: a mention's text and mentions, a click's action and ticket. Forgotten after 7 days |
| `apps_slack_posts` | The messages apps posted, once per idempotency key, and where they went |
| `apps_slack_profiles` | What Slack said about a person, with whether their address is confirmed, kept for a day |

No Composio credential is anywhere in there. Composio's answer for a
connected account carries the connection's state, and the client leaves it
undecoded, so no token Composio might include ever reaches a struct, a log or
a row. An Oryxa connection's credential is there, sealed: nothing opens it but
signin, with the KMS key, and no answer or event ever carries it.

## Security

- The project API key is read from `COMPOSIO_API_KEY` and never passed on by
  apps. Knowledge reads the same variable for its own sources, so today the key
  is in two services rather than one.
- A member's browser round trip is bound by a random state, of which only the
  SHA-256 hash is stored, and a return page must be under
  `APPS_ALLOWED_ORIGINS` — checked against dot segments and encoded ones, so a
  redirect cannot climb out.
- Composio's own wording reaches a caller only for a request it refused,
  truncated, because that is usually about the caller's own input. Everything
  else keeps only its kind.
- Oryxa's own sign-in seals every secret with `APPS_SIGNIN_KMS_KEY_ID`,
  bound to `service=oryxa-apps` and the row it belongs to, so a sealed value
  cannot be moved to another row; the key's policy grants apps only that
  encryption context. The state cookie is HttpOnly, Secure, Lax and scoped to
  the callback; a callback whose browser is signed in as someone else fails;
  an agent may not connect, replace a key or change an OAuth app; and no
  answer, error, event or log line quotes a secret a member or admin typed.
- Composio was breached in May 2026, taking roughly 5,000 GitHub tokens and
  5,241 API keys with it, including custom-auth credentials. That is the risk
  this design accepts, and why apps holds the rules that outlive any single
  vendor: an organization can be cut off from every app at once by revoking
  the project key, and each connection's credential is revoked at the app when
  it is removed.

## Mounting it

Apps mounts into `internal/host` like every other service. `apps.Definition`
names it; `apps.Migrate` installs its schema; `apps.Mount` reads apps' own
variables through the host's environment, borrows the shared Postgres pool and
AWS configuration, builds the Composio client, the auth client and the event
publisher — signed with apps' own key, as every Oryxa publisher is — and hands
back its handler, its routes, its timeouts and its workers:

```go
var definitions = []host.Definition{
	apps.Definition,
	// the other services this binary carries
}
```

Underneath, `apps.New` assembles it and touches nothing itself — no query, no
goroutine, no listener:

```go
signer, err := registry.Signer(apps.Name) // the host's event signing identities
if err != nil { return err }
publisher, err := events.NewPublisher(eventBridge, busName, signer)
if err != nil { return err }

service, err := apps.New(apps.Dependencies{
	Store:     postgres.New(database),
	Directory: authclient.New(config.AuthURL),
	Composio:  composioClient,
	Sink:      publisher,
	Verifier:  registry.Verifier(),
	Logger:    logger,
}, apps.Config{
	PublicBaseURL:  config.PublicBaseURL,
	AllowedOrigins: config.AllowedOrigins,
	WebhookSecret:  config.ComposioWebhookSecret,
})
if err != nil { return err }

if err := service.Migrate(ctx); err != nil { return err }
mux.Handle("/", service.Handler())
for _, worker := range service.Workers() {
	group.Go(func() error { worker(ctx); return nil })
}
```

## Provision

Copy the root `.env.example` to the ignored `.env` and fill in apps' variables.
Create a project at [platform.composio.dev](https://platform.composio.dev) and
put its API key in `COMPOSIO_API_KEY`. Then:

```sh
(set -a && . ./.env && go run ./apps/infra/events/cmd/provision)
```

Repeatable: it creates or updates the `apps-events` queue with its dead-letter
queue, and an `apps-events` rule on `EVENT_BUS_NAME` whose target is the queue
and which the queue policy admits alone. Put the printed `queue_url` in
`APPS_EVENTS_QUEUE_URL`.

Then point Composio at this deployment:

```sh
(set -a && . ./.env && go run ./apps/infra/composio/cmd/provision)
```

It creates the one webhook subscription, for the three event types apps acts
on, and prints the signing secret — which Composio shows **once**. Put it in
`COMPOSIO_WEBHOOK_SECRET`. Run again and it leaves the existing subscription
alone; it refuses to take over one pointing at another deployment unless
`APPS_REPLACE_WEBHOOK=true`, because replacing it mints a new secret and breaks
whatever was using the old one.

Oryxa's own sign-in needs nothing until a toolkit is switched on. Then:

| Variable | Is |
| --- | --- |
| `APPS_ORYXA_TOOLKITS` | The toolkits it is switched on for, a comma list. Empty means off |
| `APPS_SIGNIN_KMS_KEY_ID` | The KMS key every secret is sealed with. Required once a toolkit is on |
| `APPS_SIGNIN_REDIRECT_URI` | The one OAuth callback: `{the web app's origin}/v1/apps/oauth/callback`, `https://app.oryxa.in/v1/apps/oauth/callback` in production. Required with the key |
| `APPS_DEFAULT_OAUTH_CLIENTS` | Oryxa's own OAuth apps, `{"github": {"client_id": "…", "client_secret": "…"}}` |
| `APPS_HOOK_SECRETS` | The deployment's webhook secrets, `{"slack": ["current", "previous"]}` |
| `APPS_HOOK_VALUES` | What a manifest names as `{config:name}`, `{"pubsub_push_account": "…"}` |
| `APPS_SERVICE_CALLERS` | The services that may call apps' internal routes, as `internal/serviceauth` reads them |
| `APPS_SLACK_SURFACE` | `true` switches the Slack bot's gateway on; needs the key above, a caller with the purpose `slack`, and the two below |
| `APPS_SLACK_APP` | The Slack app, JSON `{"client_id","client_secret","signing_secret"}`, from Secrets Manager |
| `APPS_SLACK_API_BASE_URL` | Slack's Web API for a test's fake, loopback only. Empty is Slack itself |

Webhook URLs are built on `APPS_PUBLIC_BASE_URL`. The task role needs
`kms:GenerateDataKey` and `kms:Decrypt` on the key, conditioned on
`kms:EncryptionContext:service = oryxa-apps`.

The embedded migrations are in `internal/postgres/migrations/` and are applied
in name order when `oryxa` starts, unless `ORYXA_AUTO_MIGRATE=false`, or ahead
of time with `go run ./cmd/oryxa migrate`.

## Run locally

Composio has to reach this process to deliver a webhook, and a member's browser
has to reach it to come back from connecting an app, so localhost is not enough.
Put a tunnel in front of it:

```sh
cloudflared tunnel --url http://localhost:8080
```

Then set both to the tunnel's URL and start the binary:

```sh
APPS_PUBLIC_BASE_URL=https://<tunnel>.trycloudflare.com
APPS_WEBHOOK_URL=https://<tunnel>.trycloudflare.com/v1/apps/webhooks/composio
```

```sh
(set -a && . ./.env && go run ./cmd/oryxa)
```

Connecting an app end to end: sign in, `POST` a connection, open the
`redirect_url` it answers, finish Composio's page, and the callback brings the
browser back to `APPS_ALLOWED_ORIGINS` with `app_connection` and `status`.

Through Oryxa's own sign-in, the redirect URI has to be on the host the web
app is served from, since its dev server proxies `/v1` to apps: with web-next
on `localhost:3000`, `APPS_SIGNIN_REDIRECT_URI=http://localhost:3000/v1/apps/oauth/callback`
(http is accepted on a loopback host), registered with the app's OAuth
client. The KMS key can be a local emulator's.

## Tests

```sh
go test ./apps/...
```

Everything runs without a network: the application against an in-memory store
and a fake Composio, the MCP endpoint against a real client over `httptest`.
The store's own tests need a throwaway database and are skipped without one:

```sh
APPS_TEST_DATABASE_URL=postgres://... go test ./apps/internal/postgres/
```

They create a schema of their own and drop it afterwards, so they never touch
anything else in that database. The same variable runs
`apps/internal/httpserver/oryxa_e2e_test.go`, which drives Oryxa's own sign-in
through the real API, application, signin, hooks and store against a
stand-in OAuth app: connect, callback with the state cookie, a key and its
replacement, disconnect with revocation, a signed Slack `url_verification`,
the upkeep expiring a refused key, and a member leaving.

The Slack surface is tested against `internal/slackfake`, a fake Slack that
signs what Slack signs and answers what the Web API answers: the ingress end to
end through hooks (signature vectors, a stale timestamp, a replay, the dedupe
race, a form-encoded click, an unknown workspace, the 2 s deadline, and that
nothing on the ack path calls out), the install and its 409s, the internal API
and its posting rules, the 429 and `Retry-After` paths, every template as a
golden under `internal/slackbot/testdata/templates/` (`go test -run
TestEveryTemplateRendersAsPinned -update` rewrites them), and the manifest. The
Slack store's SQL runs with the rest of the store's, under
`APPS_TEST_DATABASE_URL`.

**The trigger pipeline is tested by shape, not by app.** Composio has
thousands of triggers and no test can name them, but each is some combination
of a few shapes, and the pipeline is blind to which app an event came from.
So one trigger of every shape — pushed and polled, required config and none,
an app needing its webhook registered by hand, payloads from a single field to
deeply nested, an empty one, one that is an array, one carrying unicode and
escapes — is driven through the whole path a real one takes:

- `apps/internal/domain/triggerconfig_test.go` — what a config check accepts
  and refuses, against the schema shapes the catalog publishes.
- `apps/internal/application/triggers_conformance_test.go` — subscribing,
  a signed delivery, the event it becomes with its payload intact, the
  delivery counted, and a redelivery counted once.
- `agents/primary/internal/inbound/triggers_conformance_test.go` — the other
  half: across the bus, into the wake-up, into the message Krude reads.

For the catalog itself, see [Checking the whole catalog](#checking-the-whole-catalog).

## What has and has not been run

Against a real Composio project, through a cloudflared tunnel, on 2026-09-18:

| Run for real | What it proved |
| --- | --- |
| The catalog, one app, its tools and its triggers | The types read Composio's own answers: GitHub's 874 tools and 46 triggers, `connectable` true for its managed OAuth and false for a `NO_AUTH` toolkit |
| Tool search and its read-only marks | Composio tags reads with `readOnlyHint`, which is what the whole write/confirm rule rests on |
| Creating an auth config and a connect link | Apps made a Composio-managed config for GitHub and recorded the pending connection under `oryxa.{org}.{member}` with only a SHA-256 of the callback state |
| The webhook subscription provisioner | Created the subscription on V3 with the three event types, printed the secret once, and on a second run left it alone rather than minting another |
| Webhook verification | Unsigned, wrongly signed and 12-minute-stale deliveries each refused 401; a correctly signed one accepted, recorded once, and its retry deduplicated |
| The MCP endpoint | `tools/list` and every tool over plain HTTP JSON-RPC, with real Composio behind the search |
| The refusals | `not_connected`, `not_read_only`, `invalid_request` for a short key, `not_found` for an unknown tool and an unknown app, `forbidden` for another organization and for an agent trying to connect |
| Migrations | Installed through the binary into a schema of their own |

On 2026-09-18, against a real PostgreSQL 16 rather than Composio:

| Run for real | What it proved |
| --- | --- |
| The store's own tests, including the trigger-delivery migration | `002_trigger_delivery.sql` installs, twice over, and a delivery's count, its event and its deduplication are one transaction |
| The wake-up store's tests | A batch claims, leases, releases and drops as the waker expects |

Against the same Composio project with a real GitHub account connected, on
2026-09-19, running apps and knowledge from the binary on localhost (auth a
stand-in, the database a throwaway schema, no tunnel):

| Run for real | What it proved |
| --- | --- |
| `triggercheck` over GitHub, Slack, Gmail, Notion, Google Calendar and Linear | All 84 triggers read and checked; none refused; 35 need no configuration, 15 need a webhook registered by hand |
| Connecting an account | The hosted link, the member authorizing in their own browser, and the callback activating the connection; `initiated` then `connected` written once each |
| Auto-subscribe | Found that a project with two-factor protection refuses a subscription without its Composio user: every subscription, by hand or automatic, failed until the user was sent. With it, GitHub's three triggers needing no configuration subscribed |
| Opting out | A trigger the member removed was left removed, and the two live ones untouched, when the pass ran again |
| Subscribing by hand | `GITHUB_COMMIT_EVENT` with its owner and repo subscribed; one missing `repo` refused 400 naming it; another app's trigger refused 404 |
| What a connection holds | Thirty-three repositories, most recently pushed first, each ready to register |
| A tool | `GITHUB_GET_A_REPOSITORY` read a private repository through the account |
| Knowledge reading through it | A GitHub source registered by hand read 41 pull requests in one pass, all published |
| Disconnecting | The account deleted at Composio, every trigger recorded removed, `removed` written once |
| A real delivery | The project's webhook re-pointed (a new secret) at a tunnel that forwarded the webhook route alone. A member starring the repository reached apps as `GITHUB_STAR_ADDED_EVENT` about 20 seconds after subscribing, verified and answered 204 in about 100 ms. Composio sent the same message three times in seven seconds; it was recorded once, `delivery_count` 1, one `apps.trigger.received.v1` |
| A connection abandoned before activation | Its link expired unused; removing it ended it as `removed`, the ending the contract names for one removed while pending |

Not yet run, and not to be described as working:

- **The Slack surface, against a real workspace.** Everything above is proven
  against `slackfake` and an in-memory store. Its SQL (migration 008) has not
  run against a PostgreSQL. To check against Slack itself: that
  `app_uninstalled` and `tokens_revoked` are delivered under `bot_events`, and
  their payloads; that `app_mention` reaches the bot in a group message and
  carries `channel_type` and `is_ext_shared_channel`; whether Slack's
  app-approval flow keeps the install link's `state`; `apps.uninstall`'s
  arguments; that `conversations.info` answers `missing_scope` for a group
  message with these scopes; `is_stranger` for Slack Connect people; that
  `users.info` carries `is_email_confirmed` (absent, a person is unconfirmed);
  and which of `team_id`, `context_team_id` and `authorizations` names the
  install for a delivery from a shared channel (the surface tries them in that
  order and takes the first with an active install).
- **Slack and Notion accounts.** Their targets (a member's own channels; parent databases) are proven only against a stand-in.
- **Google Drive and Gmail accounts, in every respect.** Connecting either,
  their targets (the whole Drive, a shared drive, `INBOX`/`SENT`), and
  knowledge's own extractors reading through them — Drive's `files.list`,
  `export`, `alt=media` and `changes`; Gmail's `messages.list`,
  `messages.get`, `history.list` and `profile` — are all proven only against
  scripted Composio proxy answers, unit-tested with fakes. In particular,
  Composio's binary indirection (a link to fetch rather than inline bytes,
  which Drive's export and download calls depend on) has not been observed
  for real.
- **The outbox reaching the bus.** EventBridge pointed at a dead local endpoint for both runs; events were written and held, never published.
- **Auth.** A stand-in answered membership; Cognito was not wired in, so indexing on connect, which redeems a member's grant, has not run.

## Later

- **Oryxa's own integrations' tools, readers and triggers** (phase 2), so an
  Oryxa connection can do more than exist. Until then every Composio path
  refuses one.
- **Knowledge behind the read route**, so the project API key is in one
  service rather than two. It waits on a member-acting token for a scheduled
  pass.
- **A tool allow-list per organization**, for an admin who wants writes limited
  to certain apps.
