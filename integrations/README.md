# Integrations service

Status: built, not yet deployed. Subscriptions to provider events for the
connections an organization already has: GitHub repository webhooks, Slack
Events API deliveries, Linear webhooks and Microsoft Graph change
notifications; webhook ingress with per-provider verification; renewal,
removal and the holds that keep a disconnected connection's credentials until
its registrations are gone; actions a caller runs with a connection; the
catalogue that reads every provider aloud; an MCP endpoint per provider that
offers those actions as tools; and the `oryxa.integrations` events every other
service listens to. Not built yet: the agent toolboxes that consume those
endpoints, which wait on the agents service's run API (see Later).

Integrations owns what passes between Oryxa and a provider: what the provider
sends, and what a caller does with a connection. Connections owns the apps,
connections and credentials: integrations never stores a provider credential,
and asks connections for one whenever it registers, renews, removes or acts. Auth remains the identity and membership boundary. Everything a
provider delivers leaves this service as one event, `integrations.trigger.received.v1`,
so a workflow, an agent or any other consumer subscribes to provider events
without knowing a provider.

```text
member ── subscriptions ──▶ integrations ── register, renew, remove ──▶ provider
                               │
                               ├── POST connections /v1/internal/connections/{id}/token
                               ├── POST connections /v1/internal/connections/{id}/holds
                               ├── GET  auth        /v1/organizations/{org}
                               └── oryxa.integrations ──▶ EventBridge

provider ── POST /v1/webhooks/{provider}/{route} ──▶ Cloudflare ──▶ integrations
auth, teams ── organization deleted, member removed ──▶ integrations-events queue
connections ── connection disconnected, app deleted ──▶ integrations-events queue
```

## Subscriptions

A subscription is one connection, one target and the events it asks for. The
member who creates it owns it: the work it triggers runs as them, so it stops
when they leave the organization, when they leave the team a team connection
hangs on, or when the connection is disconnected.

| Field | Holds |
| --- | --- |
| `connection_id` | The connection to watch, which the member must be allowed to use |
| `target` | What to watch at the provider, such as `{"repository": "acme/api"}`. At most 8 lowercase fields |
| `events` | The provider's event names to publish; empty asks for everything the provider sends |
| `status` | `pending` while it registers, then `active`; `stopped` when someone or something ends it, `failed` when the provider ends it |
| `expires_at` | When the provider's registration lapses, for a provider whose registrations expire |

Creating one checks, in order: the caller is a member of the organization; the
connection exists and they can see it; it is `active`; the provider takes that
target and those events; and connections issues a `trigger` credential for the
member, which is what decides whether they may use the connection at all.

A member sees the subscriptions they created, and an org admin the
organization's. Anyone who can see a connection can list its subscriptions and
read one; only the creator or an org admin can stop one.

Stopping is final: a stopped subscription is never restarted, and a new one
registers afresh. Deliveries that arrive after it stops are acknowledged and
dropped, because a provider may keep sending until its registration is
removed.

## Delivery kinds

A provider either delivers to one URL per app, or to a URL integrations
registers per subscription.

| Provider | Delivery | Target | Events | Verified by | Expires |
| --- | --- | --- | --- | --- | --- |
| `github` | Webhook per subscription | `{"repository": "owner/name"}` | GitHub event names (`issues`, `push`, …); none subscribes to `*` | `X-Hub-Signature-256`, HMAC-SHA256 over the raw body | No |
| `slack` | One Request URL per app | None: events reach the whole workspace | Slack event types (`message`, `reaction_added`, …) | `X-Slack-Signature` `v0=`, over `v0:timestamp:body`, within 5 minutes | No |
| `linear` | Webhook per subscription | `{"team": "ENG or its ID"}`, or none for every public team | Linear resource types (`Issue`, `Comment`, …) | `Linear-Signature`, HMAC-SHA256 over the raw body, `webhookTimestamp` within a minute | No |
| `microsoft` | Graph subscription per subscription | `{"resource": "me/messages"}`, `me/mailFolders('<id>')/messages`, `me/events` or `me/drive/root` | Change types (`created`, `updated`, `deleted`; `updated` for the drive) | `clientState` on every notification, compared in constant time | Yes: 7 days for mail and calendar, 29 for the drive |

**App delivery** (Slack) has one Request URL per connections app, so an org
admin sets the app's signing secret in integrations first —
`PUT …/apps/{appID}/webhook` — and sets the URL that answers on the provider's
app. A delivery is routed to the subscriptions of the workspaces its
`authorizations` name, so members of the same workspace each get their own
event, and a workspace nobody subscribed gets none.

**Webhook delivery** (GitHub, Linear, Microsoft) registers one URL per
subscription with its own random route and its own secret. Registration
happens with the member's credential, so a provider that refuses the scope
refuses at once, and the member sees why.

Linear creates webhooks only for a workspace admin: the connection must be an
admin's API key, or an OAuth connection with the `admin` scope. Microsoft
subscriptions need the delegated permission for their resource (`Mail.Read`,
`Calendars.Read`, `Files.Read`).

## Webhook ingress

`POST /v1/webhooks/{provider}/{route}` is the only public entry. The route is
32 to 64 random characters; it names nothing but the subscription or app
webhook it belongs to, and it is never an ID. Treat the URL as a secret: it is
the only thing a provider knows.

1. The raw body is read, at most 8 MiB, and never re-encoded: every signature is over the bytes as they arrived.
2. The route names an app webhook or a subscription; anything else is `404`, whatever the body says.
3. The provider verifies the delivery with the secret behind that route, freshly decrypted. A delivery that does not verify is `401`; one that verifies but is not valid is `400`.
4. A handshake is answered as the provider expects: Slack's `url_verification` challenge and Graph's `validationToken` come back as `text/plain`, GitHub's `ping` as an empty `200`.
5. Every event is published once per subscription: `(subscription, delivery key)` is remembered for 7 days, so a provider's retry writes nothing new.
6. The answer goes out before anything else happens. Providers give little time: Slack 3 seconds, Graph 3 (10 for validation), Linear 5, GitHub 10.

### Behind Cloudflare

Webhook paths must reach integrations untouched:

- **No caching.** Set a cache rule to bypass `/v1/webhooks/*`; a cached `200` would swallow deliveries.
- **No challenges.** Bot Fight Mode, managed challenges and JavaScript detections turn a provider's POST into an HTML challenge, which the provider reads as a failure. Add a WAF skip rule for the path.
- **No body rewriting.** Anything that re-encodes or trims the body breaks every signature.
- **Rate limiting is not authentication.** A limit may protect the path, but it must sit far above a provider's burst: GitHub redelivers nothing, and Slack disables an app's events after repeated failures. Verification is what refuses forged deliveries.
- **TLS 1.2 or better**, with a certificate chain the provider trusts. Graph refuses anything less, and Slack and Linear refuse self-signed certificates.
- **Keep the path public.** Access policies (Cloudflare Access, IP allowlists) block providers.

## Actions

An action is one thing a caller does with a connection — open an issue, post a
message — carried out with a credential connections issues for the member
asking, as `action`. Every action is recorded and published, as
`integrations.action.completed.v1` or `.failed.v1`, so what a service or an
agent did with someone's connection is auditable.

- **The member's reach is the boundary.** Connections decides whether the member the work runs as may use the connection, exactly as it does for the API and for triggers. An agent acting for a member reaches their connections, their teams' and the organization's — nothing more.
- **An action that writes carries an `idempotency_key`.** A caller that asks again under the same key is answered with what happened the first time, and the provider is not asked twice: the key is what makes a model's retry safe. An action that only reads needs no key.
- **Failures say what to do.** `needs_reconnect`, `scope_required`, `credential_rejected`, `not_found`, `rate_limited`, `provider_unavailable`: the caller sees the same reason the event carries, and never the provider's own error body.
- **Results are small.** An action reports what a caller needs to act next, not the provider's whole answer; anything over 128 KiB is left out of the event and flagged.

Actions live with their provider, in `internal/providers`, beside the code
that verifies that provider's deliveries:

| Provider | Reads | Writes |
| --- | --- | --- |
| `github` | `get_issue`, `list_issues` | `create_issue`, `comment_on_issue`, `close_issue` |
| `slack` | `list_channels` | `post_message`, `reply_in_thread`, `add_reaction` |
| `linear` | `get_issue`, `list_issues` | `create_issue`, `comment_on_issue`, `close_issue` |
| `microsoft` | `list_messages`, `get_message`, `list_events` | `send_mail`, `create_event` |

## The catalogue

`GET /v1/catalog` reads the registry aloud: for each provider, how it
delivers, what a subscription's target takes, the events it sends, the actions
a connection may run with their input schemas, and the provider's **skill** —
markdown saying how its events and tools are meant to be used. It is built
from `Builtin()` rather than a list of its own, so a provider or an action
added there appears at once. It is the same for every organization and holds
nothing of one, so it takes no credentials: a caller needs it before it has a
connection.

A skill is behaviour, not reference: which identifier to use, what to confirm
before writing, what an error means. An agent carries the skill of the
provider it is working with alongside that provider's tools.

## Tools over MCP

`POST /v1/organizations/{organizationID}/mcp/{provider}` offers one provider's
actions as MCP tools. It is **stateless**, as the 2026-07-28 protocol is: no
`initialize` handshake, no session header, each request standing alone, so any
replica may answer one and ordinary HTTP infrastructure may route it. A
stateful server would silently negotiate down to an older protocol, so the
handler sets `Stateless` and a test asserts the version it settles on.

One endpoint per provider, rather than one for everything, because the
provider's **skill** rides along as the server's `instructions`: an agent that
loads GitHub's toolbox gets GitHub's tools and GitHub's guidance together, and
nothing about Slack.

- **A tool is `provider_action`** — `github_create_issue`, `slack_post_message` — named so a toolbox's tools never collide with an agent's own.
- **Every tool takes `connection_id`**, and a write also requires `idempotency_key`; both are added to the action's own schema. Without a connection the member's one connection to that provider is used, and a member with several is asked which.
- **`provider_connections`** lists the member's connections for that provider, so an agent can ask what it would be acting as before it acts.
- **The member is the boundary**, carried per request as a bearer token or the access cookie: an agent reaches exactly what that member reaches, and no more. A request without credentials is `401` with `WWW-Authenticate`.
- **One fixed URL per provider**: `POST /v1/mcp/{provider}` is the same endpoint, taking the organization from the `X-Oryxa-Organization` header, so an agent's toolset is configured once and names the organization per request.
- **A failure is a result, not a protocol error**: the tool answers `IsError` with `needs_reconnect`, `scope_required`, `not_found` and the rest, so a model can reconnect or stop rather than guess. An error nothing recognises reports only its shape.

## Agents acting for a member

An agent acts **as the member it works for, with that member's Cognito
identity** — never as a service impersonating them. While the member is
present, the agent forwards the member's own access token. When they are not,
as when a trigger starts a run, the agent uses a token auth issues from the
member's **agent grant**: still the member's identity, but minted for auth's
agent client with the `oryxa/agent.actions` scope. Auth's organization answer
says which it is, and integrations lets an agent token run actions, use MCP
and manage subscriptions, but never change an app's signing secret, even for
an admin. Auth's README describes the grant.

## Renewal and removal

A provider registration outlives the request that made it, so two workers keep
them in step. Both claim work under a lease, so every replica may run them.

- **Renewal** runs a day before a registration expires, or halfway through a
  registration that lasts less than two days, and whenever Graph asks for
  reauthorization. A provider that no longer has the registration fails the
  subscription (`provider_removed`); one that refuses to renew fails it
  (`provider_rejected`) and queues removal; anything else — the provider or
  connections not answering, a connection that needs reconnecting — is tried
  again every 15 minutes while the registration lasts. A registration that
  expires before the next try fails as `expired`, with nothing to remove,
  because providers drop expired registrations themselves.
- **Removal** takes the registration off the provider once a subscription is
  stopped or failed, with a `cleanup` credential. It retries every 15 minutes
  for 7 days — how long connections keeps a disconnected connection's
  credentials — and gives up when the provider says the registration is
  already gone.

**Holds.** Before it registers anything, integrations puts a hold on the
connection (`integrations:subscriptions`), which tells connections to keep its
credentials after a disconnect. The hold is released when none of that
connection's subscriptions is live or has a registration left to remove. A
subscription stopped while it is still registering keeps the hold until the
request that created it finishes, so the registration it makes can still be
removed.

## API

Members authenticate with a bearer token or the `oryxa_access` cookie; a
cookie-authenticated change must come from an origin in
`INTEGRATIONS_ALLOWED_ORIGINS`. Subscriptions never show their route, secret,
registration or account key.

| Method | Path | Who | Purpose |
| --- | --- | --- | --- |
| `GET` | `/healthz` | Anyone | Liveness |
| `GET` | `/v1/catalog` | Anyone | Every provider read aloud: targets, events, actions and skills |
| `POST` | `/v1/webhooks/{provider}/{route}` | The provider | A delivery |
| `GET` | `/v1/organizations/{organizationID}/subscriptions` | Members | The caller's subscriptions, or an org admin's view of the organization's. `connection_id` narrows to one connection, for anyone who can see it |
| `POST` | `/v1/organizations/{organizationID}/subscriptions` | Members | Subscribe: `{connection_id, target, events}` → `201` |
| `GET` | `/v1/organizations/{organizationID}/subscriptions/{subscriptionID}` | Its creator, an org admin, or anyone who can see its connection | One subscription |
| `DELETE` | `/v1/organizations/{organizationID}/subscriptions/{subscriptionID}` | Its creator or an org admin | Stop it → `204` |
| `POST` | `/v1/organizations/{organizationID}/actions` | Members | Run an action: `{connection_id, action, input, idempotency_key}` → the action, with its result |
| `POST` | `/v1/organizations/{organizationID}/mcp/{provider}` | Members | That provider's actions as MCP tools, stateless, carrying its skill |
| `POST` | `/v1/mcp/{provider}` | Members | The same, naming the organization in `X-Oryxa-Organization` |
| `GET` | `/v1/organizations/{organizationID}/apps/{appID}/webhook` | Org admins | An app's webhook URL, never its secret |
| `PUT` | `/v1/organizations/{organizationID}/apps/{appID}/webhook` | Org admins | Set or rotate the app's signing secret: `{secret}` → `200` with the URL, which does not change |
| `DELETE` | `/v1/organizations/{organizationID}/apps/{appID}/webhook` | Org admins | Forget it; deliveries are then refused |

| Code | Status | Means |
| --- | --- | --- |
| `invalid_request` | 400 | The target, events or secret are not what the provider takes |
| `unauthorized` | 401 | No member, or a delivery that does not verify |
| `forbidden` | 403 | Not the subscription's creator, not an org admin, or a connection the member may not use |
| `not_found` | 404 | No such subscription, app or route — also what a connection the caller may not see answers |
| `needs_reconnect` | 409 | The connection must be reconnected before it can register anything |
| `webhook_secret_required` | 409 | The app's signing secret is not set, and its provider delivers through the app |
| `conflict` | 409 | The connection is not active, or the subscription changed while it registered |
| `credential_rejected` | 422 | The provider refused the connection's credential |
| `provider_unavailable` | 503 | The provider or connections did not answer; retry |

## Events

Integrations publishes source `oryxa.integrations` to `EVENT_BUS_NAME`, in the
envelope every Oryxa publisher uses, through the outbox. Contracts are in
`contracts/events/integrations/v1`.

| Detail type | When |
| --- | --- |
| `integrations.trigger.received.v1` | A provider delivered an event a subscription asked for |
| `integrations.subscription.created.v1` | A subscription is registered and active |
| `integrations.subscription.stopped.v1` | A member stopped it, or a disconnect, a member leaving, or an organization's deletion did |
| `integrations.subscription.failed.v1` | The provider removed, refused or expired its registration |
| `integrations.action.completed.v1` | An action a caller ran with a connection succeeded |
| `integrations.action.failed.v1` | It did not, with the reason a caller can act on |

Every trigger carries the subscription, connection, app, provider, scope and
owner, `created_by` — the member whose subscription it is, and whom the work a
trigger starts runs as — the provider account, what the event is about
(`resource`), who did it (`actor`), and the provider's payload as it arrived,
up to 128 KiB;
`payload_omitted` says when it did not fit. `event` is the provider's name for
what happened, after the provider's: `github.issues.opened`, `slack.message`,
`linear.issue.create`, `microsoft.message.created`. No event ever carries a
credential, a signing secret or a route. A delivery a provider repeats is
published once, but the bus delivers at least once: a consumer deduplicates on
the envelope's `event_id` before it acts.

Integrations consumes, from `INTEGRATIONS_EVENTS_QUEUE_URL`:

| Event | What it stops |
| --- | --- |
| `connections.connection.disconnected.v1` | That connection's subscriptions (`disconnected`) |
| `connections.app.deleted.v1` | That app's webhook secret |
| `auth.member.removed.v1` | The subscriptions that member created (`member_left`); `reason` `organization_deleted` is left to the next one |
| `auth.organization.deleted.v1` | Everything the organization has (`organization_deleted`) |
| `teams.member.removed.v1` | The subscriptions that member created on that team's connections (`team_member_left`) |

Every handler is safe to redeliver: a subscription already stopped is not
stopped twice, and no second event is written.
`go run ./integrations/infra/events/cmd/provision` creates the queue, its
dead-letter queue and the EventBridge rule.

## Database

Integrations uses `/internal/postgreskit`. Other services never read these
tables.

| Table | Holds |
| --- | --- |
| `integrations_subscriptions` | The subscription, its owner and scope, target and events, the opaque route, the provider's registration ID, the encrypted secret, status and reason, expiry, renewal and removal times with their lease, and a version |
| `integrations_app_webhooks` | One signing secret and route per app whose provider delivers through its app |
| `integrations_deliveries` | `(subscription, delivery key)` for 7 days, so a redelivery publishes nothing new |
| `integrations_actions` | Every action run with a connection, under `(connection, action, idempotency key)`, kept for 30 days so a repeat answers what happened the first time |
| `integrations_outbox` | Events written in the same transaction as the change they describe |

## Security

- Every webhook secret is envelope-encrypted with integrations' own KMS key. The encryption context binds the service, organization, resource type and ID, so ciphertext cannot be moved between organizations or records. Plaintext is wiped after each delivery is verified.
- Integrations never stores a provider credential. It asks connections for one for a named purpose (`trigger` to register or renew, `cleanup` to remove), signing each call with its own KMS key as `integrations`.
- Signatures are checked over the raw body, in constant time, before the body is used for anything else. Graph's `clientState` arrives inside the notification, so it is compared the same way, and one notification without it refuses the whole batch.
- Routes are 32 bytes of randomness, never an identifier, and never published in an event.
- A provider's payload is published as it arrived, minus the fields that carry secrets: Slack's legacy verification `token`, Graph's `clientState`.
- Every outbound provider call has a 10-second timeout, a 1 MiB response cap and no redirects, and provider error bodies never reach an event, a log or the API.

## Adding a provider

A provider is one file in `internal/providers` returning a `Provider`,
registered in `Builtin()`. The application, store and API need no change. A
definition says how a target and events are checked, how a delivery is
verified and read, for webhook delivery how a registration is created, renewed
and removed, what actions a connection may run, and the skill that says how to
use any of it. Whatever it declares appears in the catalogue at once. `Receive` returns what to answer, which subscriptions the
delivery reaches, the events to publish, and whether the provider is asking
for renewal or saying the registration is gone.

Every adapter has unit tests against a fake server, and an end-to-end test in
`internal/e2e` that runs the whole service — HTTP API, store, encryption,
service identity and the adapter — against a fake provider that registers,
pings, signs and delivers the way the real one does.

## Configuration

The listener, `DATABASE_URL`, migrations, `EVENT_BUS_NAME` and `AWS_REGION`
belong to the `oryxa` process; see the root README. Integrations reads:

| Variable | Default | Purpose |
| --- | --- | --- |
| `AUTH_URL` | this process, when it runs auth | Auth |
| `CONNECTIONS_URL` | this process, when it runs connections | Connections |
| `INTEGRATIONS_PUBLIC_BASE_URL` | — | Where providers reach integrations; webhook URLs are built on it, required |
| `INTEGRATIONS_ALLOWED_ORIGINS` | — | Origins that may send cookie-authenticated changes |
| `INTEGRATIONS_KMS_KEY_ID` | — | Envelope-encrypts webhook secrets, required |
| `INTEGRATIONS_SERVICE_KEY_ID` | — | The KMS key integrations signs service tokens with, required |
| `INTEGRATIONS_SERVICE_NAME` | `integrations` | What it calls itself to connections |
| `INTEGRATIONS_EVENTS_QUEUE_URL` | — | Without it, subscriptions do not stop when connections, members or organizations go away |

Connections must allow integrations to call it, with the purposes it uses:

```json
CONNECTIONS_SERVICE_CALLERS=[{"service": "integrations", "key_id": "alias/oryxa-integrations-identity", "purposes": ["trigger", "cleanup", "action"]}]
```

## Run locally

Copy the root `.env.example` to the ignored `.env`, then from the repository root:

```sh
docker compose up -d
(set -a && . ./.env && ORYXA_SERVICES=integrations go run ./cmd/oryxa)
```

Install the schema on its own with `go run ./cmd/oryxa migrate`; it also runs
at startup unless `ORYXA_AUTO_MIGRATE=false`. Integrations deploys as part of
the single `oryxa` image.

`INTEGRATIONS_PUBLIC_BASE_URL` must be a public HTTPS URL that reaches the
service, since providers deliver to it: a Cloudflare tunnel is the shortest
way.

```sh
cloudflared tunnel --url http://localhost:8080
```

## Tests

Unit tests need nothing. The store and end-to-end tests need PostgreSQL; each
migrates a throwaway schema and drops it afterwards, so they are safe against a
shared development database.

```sh
go test ./integrations/...
INTEGRATIONS_TEST_DATABASE_URL='postgres://oryxa:oryxa-dev-password@localhost:5435/oryxa?sslmode=disable' go test ./integrations/...
```

The end-to-end tests in `internal/e2e` run the HTTP API, the application, the
store, envelope encryption, service identity and the real provider adapters
against a fake GitHub, Slack, Linear and Microsoft Graph: each registers,
validates or pings, delivers signed events, and is asked to remove what it
created.

The live tests run the same stack against the real provider. Each needs
`INTEGRATIONS_TEST_DATABASE_URL`, a public URL that reaches the test's
listener, and its own credentials; without them it skips. They create a
subscription, have the provider deliver a real event, and remove everything
they made, including anything they created at the provider.

```sh
export INTEGRATIONS_TEST_DATABASE_URL='postgres://oryxa:oryxa-dev-password@localhost:5435/oryxa?sslmode=disable'
export INTEGRATIONS_LIVE_ADDRESS=127.0.0.1:8787
export INTEGRATIONS_LIVE_PUBLIC_URL=https://<your tunnel hostname>
go test -run TestLive -v ./integrations/internal/e2e
```

| Test | Needs | Does |
| --- | --- | --- |
| `TestLiveGitHubWebhook` | `INTEGRATIONS_LIVE_GITHUB_TOKEN` (repo or admin:repo_hook), `INTEGRATIONS_LIVE_GITHUB_REPOSITORY` | Creates a webhook, waits for GitHub's ping, asks GitHub to redeliver the repository's latest push, checks the trigger, then removes the webhook |
| `TestLiveGitHubAction` | The same two, and no public URL, since actions reach out rather than being reached | Opens an issue, checks the same idempotency key opens no second one, reads it back, comments on it, then closes it. It leaves a closed issue behind, since GitHub's API cannot delete one |
| `TestLiveSlackEvents` | `INTEGRATIONS_LIVE_SLACK_SIGNING_SECRET`, `INTEGRATIONS_LIVE_SLACK_BOT_TOKEN`, `INTEGRATIONS_LIVE_SLACK_CHANNEL`, optionally `INTEGRATIONS_LIVE_SLACK_ROUTE` | Posts a message as the bot and waits for Slack to deliver it, then deletes it. The app's Request URL must already be the URL the test logs; set `INTEGRATIONS_LIVE_SLACK_ROUTE` (32 to 64 characters) to keep that URL between runs |
| `TestLiveLinearWebhook` | `INTEGRATIONS_LIVE_LINEAR_TOKEN` (a workspace admin's), `INTEGRATIONS_LIVE_LINEAR_TEAM` | Creates a webhook and an issue, waits for the delivery, then deletes both |
| `TestLiveMicrosoftGraphSubscription` | `INTEGRATIONS_LIVE_MICROSOFT_TOKEN` (delegated, `Mail.ReadWrite`) | Creates a subscription, which Graph validates against the public URL, drafts a message, waits for the notification, renews the subscription, then deletes the draft and the subscription |

Tokens are read from the environment and never printed. Give each live test a
throwaway repository, workspace, team or mailbox.

## Later

- Triggers into agent runs: something has to turn `integrations.trigger.received.v1` into a run, as the member the subscription belongs to. Subscriptions are not tools yet either, so an agent can act but not watch, although each provider's skill already describes its events.
- User-provided integrations from OpenAPI specs, offered through the same endpoint behind a filter.
- A sweeper for subscriptions left `pending` by a process that died mid-registration; today they are only cleaned up when something stops them.
- More providers, and more of each provider's surface: connections connects 17 providers and only five have triggers and actions, and Graph subscriptions are limited to mail, a mail folder, calendar events and the drive root. `PROVIDER-READINESS.md` surveys the rest and records why five of them cannot have a verified delivery at all.
- Per-request headers on provider calls: Graph's `Prefer` cannot be sent yet, so `microsoft_get_message` strips a message's HTML itself.
- Live runs of everything but GitHub's webhook. Linear's `issue(id:)` resolving an identifier such as `ENG-42`, which its actions rely on, has only been checked against the fake.
- Shared envelope encryption: `internal/secrets` is the same code as connections', which belongs in one place.
