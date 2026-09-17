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
project. That key lives only in this process. It is never forwarded, never
given to an agent, and never put in an event, because anything holding it could
act as anyone.

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

Failures come back as codes a model can act on rather than prose:
`not_connected`, `needs_reconnect`, `choose_connection`, `not_read_only`,
`app_error`, `rate_limited`. Krude's side of this is
`agents/primary/internal/agents/apps.go` and its skill in
`toolboxes/apps.md`.

## Triggers

A member subscribes one of their own active connections to an event its app
delivers, such as `GITHUB_COMMIT_EVENT`. Composio watches the app — pushing
webhooks where the app supports them, polling where it does not — and delivers
to one project-wide subscription:

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
| `POST /v1/organizations/{org}/apps/connections` | Start connecting an app; answers the link to send the member to |
| `GET /v1/organizations/{org}/apps/connections` | The caller's connections, or with `?scope=organization` an admin's view of everyone's |
| `GET /v1/organizations/{org}/apps/connections/{id}` | One connection |
| `DELETE /v1/organizations/{org}/apps/connections/{id}` | Disconnect: Composio deletes the account and revokes its credential |
| `POST /v1/organizations/{org}/apps/tools/{tool}/run` | Run a tool |
| `POST /v1/organizations/{org}/apps/triggers` | Subscribe a connection to an app's event |
| `GET /v1/organizations/{org}/apps/triggers` | The caller's triggers |
| `DELETE /v1/organizations/{org}/apps/triggers/{id}` | Unsubscribe |

Errors are `{"error":{"code","message"}}` with the codes
`invalid_request`, `unauthorized`, `forbidden`, `not_read_only`, `not_found`,
`not_connected`, `choose_connection`, `needs_reconnect`, `not_connectable`,
`conflict`, `rate_limited`, `unavailable`, `internal`.

## Events

Published on `oryxa.apps`, through the outbox, so a change and its event cannot
disagree.

| Detail type | Says |
| --- | --- |
| `apps.connection.connected.v1` | A member connected an app |
| `apps.connection.expired.v1` | Composio lost a connection's credential; the member must reconnect |
| `apps.connection.removed.v1` | A connection is gone, and why |
| `apps.trigger.received.v1` | An app delivered an event, with its payload |
| `apps.trigger.disabled.v1` | Composio stopped a trigger, and why |
| `apps.action.completed.v1` | A write ran |
| `apps.action.failed.v1` | A write did not, in one word — never the app's own |

A trigger's payload is the app's own data, so it travels only as far as it must:
over 128 KiB it is left out with `data_omitted`, and the outbox blanks it as
soon as the event is published rather than keeping it for the retention window.

Consumed, from `oryxa.auth`: `auth.member.removed.v1` and
`auth.organization.deleted.v1`. A connection is standing access to a member's
own account, so it must not outlive their place in the organization — both take
every affected connection with them, deleting the account at Composio and
revoking its credential. `apps.Subscriptions()` says what the queue must carry.

## Database

Apps uses `/internal/postgreskit`. Other services never read these tables.

| Table | Holds |
| --- | --- |
| `apps_connections` | A member's connection to an app: its Composio account, status and label, and the hash of its pending round trip |
| `apps_auth_configs` | The auth config the project connects each app with, so two members agree on one |
| `apps_triggers` | What a connection is subscribed to, and the Composio instance behind it |
| `apps_actions` | Tool runs, with the idempotency key a write can be replayed under |
| `apps_handled_events` | Webhook and bus deliveries already acted on, since both deliver at least once |
| `apps_outbox` | Events written in the same transaction as the change they describe |

No credential is anywhere in there. Composio's answer for a connected account
carries the connection's state, and the client leaves it undecoded, so no
token Composio might include ever reaches a struct, a log or a row.

## Security

- The project API key is read once, from `COMPOSIO_API_KEY`, and never leaves
  the process.
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

## Later

- **An OAuth app of Oryxa's own**, so apps that Composio holds no app for can
  be connected. Today they are refused as not connectable.
- **Triggers to agent runs**: `apps.trigger.received.v1` is published but
  nothing starts a run from it yet.
- **Knowledge over Composio**, so a source reads an app through the member's
  connection rather than a credential Oryxa holds.
- **A tool allow-list per organization**, for an admin who wants writes limited
  to certain apps.
