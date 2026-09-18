# Apps service

A member's third-party apps — GitHub, Gmail, Slack, Notion, and the rest of
Composio's catalog — and the safety layer around them: the connections a member
makes, the tools an agent may run through them, the events those apps deliver,
and the `oryxa.apps` events the rest of Oryxa acts on. It mounts into the
single `oryxa` binary like every other service — see
[Mounting it](#mounting-it) and [Run locally](#run-locally).

Apps replaces the connections service, which was archived on 2026-09-17 at tag
`archive/connections-and-integrations-2026-09-17`. Oryxa no longer holds a
single OAuth credential: [Composio](https://composio.dev) holds them, refreshes
them, and runs the calls. What Oryxa keeps is the part no vendor can decide —
who may use which connection, which tools may run unconfirmed, and what is
announced when something happens.

```text
member ── connect an app ──▶ apps ── hosted link ──▶ Composio ──▶ the app
                              │
agent ── MCP ─────────────────┤
                              ├── GET auth /v1/organizations/{org}
                              └── oryxa.apps ──▶ the bus ──▶ agents, knowledge

the app ── event ──▶ Composio ── signed webhook ──▶ apps
auth ── organization deleted, member removed ──▶ apps-events queue
```

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

| Refused | Because |
| --- | --- |
| An agent connecting or removing an app | Standing access to a member's account is the member's decision |
| A tool run through another member's connection | A connection is not a shared credential |
| A write through `apps_run_read_tool` | Composio's own read-only mark decides, and the server enforces it |
| A write without an idempotency key | A retry must not become a second change |
| A webhook whose signature does not verify | Nothing in the body is read at all |
| An app needing an OAuth app of Oryxa's own | There is none yet, so the member would reach a broken page |

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

The project's auth config for an app is found or made the first time anyone
connects it: Composio's own app where Composio has one, otherwise a scheme
whose credential the member types into Composio's page (`API_KEY`,
`BEARER_TOKEN`, `BASIC`). An app that would need an OAuth app of Oryxa's own is
refused as not connectable rather than half-connected.

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
| `apps_connections` | The member's connected apps, with the connection each tool would act through |
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
`not_connected`, `needs_reconnect`, `choose_connection`, `not_read_only`,
`app_error`, `rate_limited`. Krude's side of this is
`agents/primary/internal/agents/apps.go` and its skill in
`toolboxes/apps.md`.

## Reading an app's own API

A tool run answers what a tool returns. Paging through what an app *holds* —
a repository's files, a channel's history, a workspace's pages — is a different
shape, so `POST /v1/organizations/{org}/apps/read` proxies one request to the
app's own API through the caller's connection, with Composio placing the
credential.

Only reads pass. A GET or HEAD always does; a POST only where the app's own
read API is one (Notion's search and database queries, Slack's history), from a
list apps keeps. Anything else is refused as `not_read_only`, because a change
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
member noticing. A connection carries `auto_subscribe` and
`auto_subscribed_at`, so a caller can tell a connection still being set up
from one that is done.

Every subscription that fires wakes Krude, and every wake-up is a run, so
connecting an app has a ceiling on what it can commit someone to:
`AutoSubscribeLimit`, 25 events. Past it the rest are left for the member, the
same as one needing configuration. An app the pass cannot read is tried again
next tick, three times before it is left alone.

Two ways to turn it off:

| Off | How |
| --- | --- |
| For one connection | `"auto_subscribe": false` in the connect request |
| For the deployment | `APPS_AUTO_SUBSCRIBE=false` |

A member naming it outranks the deployment's default, either way.

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
cookie, except the catalog and the two Composio calls. A cookie-authenticated
mutation also needs an `Origin` in `APPS_ALLOWED_ORIGINS`.

| Route | Does |
| --- | --- |
| `GET /v1/apps/catalog` | Composio's apps, with whether each can be connected here |
| `GET /v1/apps/catalog/{toolkit}` | One app |
| `GET /v1/apps/catalog/{toolkit}/tools` | An app's tools |
| `GET /v1/apps/catalog/{toolkit}/triggers` | The events an app can deliver |
| `GET /v1/apps/connect/callback` | Where Composio returns a member's browser |
| `POST /v1/apps/webhooks/composio` | Where Composio delivers events |
| `POST /v1/organizations/{org}/apps/connections` | Start connecting an app; answers the link to send the member to. `auto_subscribe: false` connects without subscribing to its events |
| `GET /v1/organizations/{org}/apps/connections` | The caller's connections, or with `?scope=organization` an admin's view of everyone's |
| `GET /v1/organizations/{org}/apps/connections/{id}` | One connection |
| `DELETE /v1/organizations/{org}/apps/connections/{id}` | Disconnect: Composio deletes the account and revokes its credential |
| `POST /v1/organizations/{org}/apps/tools/{tool}/run` | Run a tool |
| `POST /v1/organizations/{org}/apps/read` | Read the app's own API through the caller's connection — GETs, and the few read endpoints that are POSTs |
| `POST /v1/organizations/{org}/apps/triggers` | Subscribe a connection to an app's event, checked against the trigger's own definition |
| `GET /v1/organizations/{org}/apps/triggers` | The caller's triggers, with how each delivers and whether it ever has |
| `DELETE /v1/organizations/{org}/apps/triggers/{id}` | Unsubscribe |

Errors are `{"error":{"code","message"}}` with the codes
`invalid_request`, `unauthorized`, `forbidden`, `not_read_only`, `not_found`,
`not_connected`, `choose_connection`, `needs_reconnect`, `not_connectable`,
`conflict`, `rate_limited`, `unavailable`, `internal`.

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

A connection's life is a closed story on the bus: every `initiated` ends as
exactly one of `connected` or `failed`, and a connected one ends as `expired`
or `removed`. `failed` carries a constant rather than the prose a member
reads, since it is grouped and counted — `denied_at_provider`,
`wrong_account`, or `abandoned` for a member who started and never came back,
which the maintenance sweep announces and nothing else would report.

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
| `apps_connections` | A member's connection to an app: its Composio account, status and label, the hash of its pending round trip, and whether it has been subscribed to what the app delivers |
| `apps_auth_configs` | The auth config the project connects each app with, so two members agree on one |
| `apps_triggers` | What a connection is subscribed to, the Composio instance behind it, how it delivers, and whether it ever has |
| `apps_actions` | Tool runs, with the idempotency key a write can be replayed under |
| `apps_handled_events` | Webhook and bus deliveries already acted on, since both deliver at least once |
| `apps_outbox` | Events written in the same transaction as the change they describe |

No credential is anywhere in there. Composio's answer for a connected account
carries the connection's state, and the client leaves it undecoded, so no
token Composio might include ever reaches a struct, a log or a row.

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
publisher, and hands back its handler, its routes, its timeouts and its
workers:

```go
var definitions = []host.Definition{
	apps.Definition,
	// the other services this binary carries
}
```

Underneath, `apps.New` assembles it and touches nothing itself — no query, no
goroutine, no listener:

```go
service, err := apps.New(apps.Dependencies{
	Store:     postgres.New(database),
	Directory: authclient.New(config.AuthURL),
	Composio:  composioClient,
	Sink:      events.NewPublisher(eventBridge, busName),
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
anything else in that database.

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

Not yet run, and not to be described as working:

- **Connecting an account end to end.** The hosted flow was opened but not completed, so no account has been activated, no tool has run against a real app, and no trigger has been subscribed. Every path past activation is proven only against a stand-in.
- **A real delivery from a real trigger.** The verified deliveries above were signed with the real subscription secret but sent by hand. GitHub's triggers are all `poll`, so a real one arrives on Composio's polling interval rather than instantly.
- **`triggercheck` against the real catalog.** It is tested against a stand-in catalog, including the shapes it is meant to catch, but has never read Composio's own. Running it is the first thing to do with a project API key, since it needs no connected account.
- **The outbox reaching the bus.** EventBridge pointed at a dead local endpoint for this run, and no event was written, so the dispatcher never published.
- **Auth.** A stand-in answered membership; Cognito was not wired in.

## Later

- **An OAuth app of Oryxa's own**, so apps that Composio holds no app for can
  be connected. Today they are refused as not connectable.
- **Knowledge behind the read route**, so the project API key is in one
  service rather than two. It waits on a member-acting token for a scheduled
  pass.
- **A tool allow-list per organization**, for an admin who wants writes limited
  to certain apps.
