# Integrations service: design

| | |
|---|---|
| Status | Approved direction, 2026-10-15. Built around the clock (every two hours) by the integrations loop agent ([loop-agent.md](loop-agent.md)), reviewed and merged by macbookpro, both as Claude Code cloud routines ([operations.md](operations.md)). |
| Owner | Shubham (decisions) · macbookpro (review, merge, infrastructure) · the integrations loop agent (build) |
| Code | `integrations/` (a service in the root Go module, run as its own ECS service) and `integrations/connectors/<app>/` |
| Queue | GitHub issues labelled `loop`, in phase order (§18) |

## 1. Summary

Oryxa gets its own **integrations service**: one Go service that connects members' apps and runs everything that follows from that. Signing in, syncing data for knowledge, receiving webhooks, polling, exposing actions to agents as MCP tools, proxying raw API calls, and its own operations (rate limits, retries, health, revocation) all happen inside it. The rest of Oryxa sees a small API and a few events.

- **Native connectors only for the apps that matter most:** Google, Microsoft, GitHub, Slack and Notion. The long tail stays on Composio, behind the router `apps` already has.
- **Two ways to sign in, for every app:**
  - the org's own OAuth app or API key, "bring your own" (BYO);
  - Oryxa's own app.
- **Connectors never hold tokens:** they run as separate processes that reach apps only through the service's egress proxy.

## 2. Context

- **Today,** `apps` serves integrations through Composio (tools and triggers) and Pipedream (development only). Native sign-in (`apps/internal/signin`) and a connector framework (`apps/internal/integration`) exist and are live, but nothing native is switched on (`APPS_ORYXA_TOOLKITS` is empty).
- **Knowledge** reads app data through Composio's proxy. That is billed per call, misses deletes, and can't sync incrementally.
- **oryxa-platform** (Rust, not deployed) built a broker, a connector contract, connectors and webhook verifiers. They are well tested, but its core is built on WorkOS and SpiceDB. We take its contract, its manifest format, its verifiers and its Go connectors.
- **Research, 2026-10-15** (Nango, Airbyte, Singer, Fivetran, Composio, Pipedream, Activepieces, n8n, Merge, Paragon, Arcade, Klavis, vendors' MCP servers):
  - nothing worth running off the shelf fits: self-hosted Nango is auth and proxy only, and it and Airbyte are under the Elastic License;
  - but the designs agree with each other, and this document follows them.

## 3. Goals and non-goals

**Goals**
1. Members connect Google, Microsoft, GitHub, Slack and Notion through **their org's app** or **Oryxa's app**.
2. **Knowledge stays current** from those apps, deletes and permission changes included, without re-embedding unchanged data.
3. **Agents act** in those apps through MCP tools, within their grants (I3), with writes confirmed.
4. **Agents wake** on app events (webhooks or polls) through the existing batching path.
5. **The service runs itself:**
   - token refresh, subscription renewal, rate limits and retries;
   - health and expiry warnings;
   - revocation when people leave.
6. **An agent can add or extend a connector in a day,** safely: small files, a gate in CI that needs no live accounts, and no access to tokens.

**Non-goals (for now)**
- Native connectors for the long tail: Composio keeps it.
- A public developer platform or a marketplace.
- Running vendors' MCP servers as the only route (an option later, §12.5).
- Writing into members' data warehouses (we are not an ELT tool).

## 4. Decisions

| # | Decision | By |
|---|---|---|
| D1 | A separate service with its own deploy, database login and IAM role, handling its own operations | Owner |
| D2 | Native only for the main apps; Composio for the long tail; Pipedream development-only | Owner |
| D3 | BYO **and** Oryxa apps for every app; members complete OAuth through their org's app; API keys where apps use them | Owner |
| D4 | No Durable Object per connection, and no scatter of Workers | Owner |
| D5 | Go, in this repo and the root module. It is a service under `internal/host` like the others, run from the same image as its **own ECS service** with `ORYXA_SERVICES=integrations`, its own task role and its own database login. CI and the image pipeline need no changes. Connector binaries are built into the same image. **It ships switched off:** `Definition.Enabled` reads `INTEGRATIONS_ENABLED=true`. Every merge deploys main, and production runs all services by default, so until then the main task carries it inert (no mount, no migration, no workers). | Default |
| D6 | Connectors run as **separate processes** behind **egress handles** | Default (recommended) |
| D7 | Vault key in **AWS KMS**. The owner suggested Cloudflare Secrets Store to save cost; KMS costs about $1 a month and every decrypt is audited in CloudTrail. Secrets Store holds edge-side secrets only. | Default; the owner may override |
| D8 | Webhook ingress **in the service** first; the Cloudflare Worker from oryxa-platform only if volume needs it | Default |
| D9 | Build order: core, then **GitHub**, Microsoft, Google, then Slack and Notion | Default |
| D10 | oryxa-platform is a **reference:** we lift its contract, manifest, verifiers and Go connectors, not its Rust services | Default |

## 5. Architecture

```
                 ┌───────────────────────────── integrations (ECS service, Go) ─────────────────────────────┐
 web ──▶ apps ──▶│ HTTP API ─┬─ connect / callback / connections / org apps (admin)                          │
 agents ─MCP────▶│           ├─ MCP gateway (search_tools · get_tool_schema · call_tool)                     │
 knowledge ─────▶│           ├─ records feed (GET /v1/records)                                               │
 apps (router) ─▶│           ├─ proxy (raw calls)                                                            │
 app webhooks ──▶│           └─ webhook ingress ──▶ queue                                                    │
                 │                                                                                           │
                 │  vault (KMS envelopes) ◀── broker: refresh · handles · egress proxy ──▶ app APIs          │
                 │                                  ▲ handle                                                 │
                 │  connector host ──gRPC/Unix socket──▶ connector processes (one Go binary per app)         │
                 │        ▲                                                                                  │
                 │  workers ◀── job queue (Postgres, SKIP LOCKED) ◀── scheduler                              │
                 │  records store + change feed · rate limiter · outbox ──▶ EventBridge bus                  │
                 └───────────────────────────────────────────────────────────────────────────────────────────┘
```

**Process model.** One task runs `oryxa serve` with `ORYXA_SERVICES=integrations`. That starts:
- the HTTP API (including webhook ingress and the MCP gateway);
- the scheduler loop;
- N workers;
- the connector host, which spawns one child process per connector binary, supervises it and restarts it on a crash.

**Built so far (#283).** The skeleton: `integrations.Definition`, registered after `realtime`, reads `INTEGRATIONS_ENABLED=true` (off otherwise: no mount, no migration, no workers, no configuration). On, it migrates `integrations/migrations/` (schema `integrations`) and serves `GET /healthz`; internal routes are registered with `Server.Internal`, which refuses any call without a verified serviceauth token, and the callers come from `INTEGRATIONS_SERVICE_CALLERS`. A process whose `ORYXA_SERVICES` names `integrations` while it is off refuses to start.

**Built so far (#284).** Migration `002_vault.sql` creates `org_apps`, `connections`, `credentials` and `oauth_states`. `integrations/internal/vault` seals secrets with `internal/secrets`: the envelope's resource type is the table and its resource id is `<org>/<row>` (empty org for Oryxa's own app), so the KMS encryption context is `{service, table, org/row}`. A value copied to another org, table or row does not open. `oauth_states` holds only the hash of `state`; `credentials` is keyed by `(connection_id, kind)`.

**Built so far (#285).** `integrations/proto/oryxa/connector/v1/connector.proto` and its Go code (`integrations/proto/connectorv1`) are oryxa-platform's connector contract, unchanged. `integrations/manifest` decodes (unknown fields refused) and validates a manifest: names, https URLs, a `base_url` host the `egress_allowlist` covers, token-kind references, tool and input/output schemas (as JSON Schema, patterns not compiled: oryxa-platform's ECMA patterns don't compile in Go's regexp), streams, and webhook events against streams. Two decisions the example in §8.2 left open: `webhooks.routing` is `"org_app"` and the connection is found by `route_by` (JSON pointers into the payload) or `route_header`; `reauth_on` takes HTTP statuses or error codes. The five oryxa-platform manifests, with their names mapped to ours, are the test fixtures (`manifest/testdata/platform`). The description lint and tool budget stay in the gate (#290).

**Built so far (#287).** `integrations/egress` mints and verifies handles (`eh1.<kid>.<claims>.<mac>`; HMAC-SHA256 over the first three parts; any held key verifies, one signs; claims `c`, `k`, `m`, `a`, `e`; a handle over 5 minutes is refused at mint and at verify) and runs the proxy: https only, no URL credentials, port 443, host checked against the manifest (`Manifest.AllowsHost`), the connector's `Authorization`, `Cookie` and hop-by-hop headers dropped, a bearer token added, `safehttp` with **no redirects** (a 3xx goes back to the connector, whose next call is checked like any other), 10 MB body caps, one refresh and retry on a 401, `Set-Cookie` removed from responses and the token masked if the app echoes it. The vault and refresh are behind the `Connections` interface, wired when #289 lands. Recording rate-limit headers (step 6) waits for the limiter (#298).

**Built so far (#286).** `integrations/gosdk` is oryxa-platform's `gosdk` without its Lambda adapter: `gosdk.Main` serves a connector on the socket named by `ORYXA_CONNECTOR_SOCKET` (directory 0700, socket 0600). `integrations/host` spawns each connector binary with a bare environment (only the socket path, never the host's secrets), calls `Describe`, parses the answer with `manifest.Parse` and requires the manifest id to match; a restarted connector must describe the same manifest. A crashed connector is restarted with exponential backoff (reset after a minute up). `Invoke` checks the tool and input against the manifest's `input_schema`, then the output against `output_schema` (at most 256 KiB) and a `ToolError`'s shape; `Sync` checks stream and mode against the manifest, then every event against the contract's limits (ids, kinds, https urls, content, ACL required on an upsert), requires a final `Checkpoint`, and cuts a run silent for 60 s or longer than 30 minutes. A schema Go can't resolve (the ECMA-pattern case in #285) refuses the connector at start. `integrations/connectors/fakeapp` has one read tool (`fakeapp.echo`) and one stream (`notes`) for tests; the host's tests build it.

Roles can later split across tasks with flags (`--api`, `--workers`). Like the core binary, it starts with one task.

**Public routes.** The Cloudflare Tunnel gets a path rule sending `api.oryxa.in/v1/integrations/*` (OAuth callbacks, webhooks) to this service. Everything else is internal, reached with serviceauth.

**Trust boundaries**

| Boundary | What crosses | Guard |
|---|---|---|
| Internet → ingress | Webhooks, OAuth callbacks | Signature verification with the org app's secret; single-use hashed OAuth `state` |
| Oryxa services → API | Calls from apps, knowledge and agents | serviceauth ES256 (KMS), with member, agent and role claims (I2) |
| Service → connector | gRPC on a private Unix socket (0600) | Messages validated against the manifest; connectors get handles, never tokens |
| Connector → app | Only through the egress proxy | Handle checked (HMAC, 5-minute TTL, one connection and token kind); host allowlist; SSRF checks (`safehttp`) |
| Service → database | Its own schema, `integrations`, and its own login | No other service can read it |

## 6. Data model

All tables live in schema `integrations`; migrations are numbered in `integrations/migrations/`.

| Table | Purpose | Key columns |
|---|---|---|
| `org_apps` | An OAuth app or key-based app per org, or Oryxa's own (`org_id IS NULL`) | `id`, `org_id`, `app`, `kind` (`oauth_client` \| `github_app` \| `slack_app` \| `api_key`), `client_id`, `secret` (sealed), `webhook_secret` (sealed), `extra` jsonb (GitHub App id, tenant id…), `expires_at`, `status` (`ok` \| `expiring` \| `broken` \| `retired`), `created_by`, timestamps. **One live row per (org, app)**; a replaced row stays for the connections pinned to it. |
| `connections` | One member's (or the org's) connection to an app | `id`, `org_id`, `member_id` (null for org-level), `level` (`personal` \| `org`), `app`, `org_app_id` (pinned), `mode` (`oryxa` \| `byo` \| `user_key`), `account` (the app's account id and name), `scopes[]`, `status` (`ok` \| `needs_reauth` \| `degraded` \| `error` \| `revoked`), `failures`, `cooldown_until`, `last_ok_at`, timestamps |
| `credentials` | Sealed tokens | `connection_id`, `kind` (`access` \| `refresh` \| `api_key` \| `installation`), `sealed`, `expires_at`, `version` |
| `oauth_states` | Sign-ins in flight | `state_hash`, `org_id`, `member_id`, `app`, `org_app_id`, `pkce_verifier` (sealed), `return_to`, `expires_at`, `used_at` |
| `stream_state` | Sync position | `connection_id`, `stream`, `partition`, `checkpoint` jsonb (`{kind: datetime\|token, value, lookback}`), `last_run_at`, `next_run_at`, `status` |
| `backfill_slices` | Resumable backfills | `id`, `connection_id`, `stream`, `partition`, `window_from`, `window_to`, `checkpoint`, `status` |
| `jobs` | The work queue | `id`, `kind` (`sync` \| `slice` \| `poll` \| `refetch` \| `renew` \| `reconcile` \| `revoke` \| `refresh`), `connection_id`, `stream`, `payload`, `priority`, `run_after`, `lease_until`, `lease_owner`, `attempts`, `status`, `last_error` |
| `records` | The synced copy plus its change feed | PK `(connection_id, stream, record_id)`; `seq` (bigint, from a sequence, bumped on every change), `kind` (`upsert` \| `delete`), `hash`, `doc` jsonb (title, body, url, author, updated_at, as the manifest's `document` names them), `acl` jsonb, `source_updated_at`. Index `(org_id, seq)`. |
| `subscriptions` | Webhook subscriptions the app needs renewed (Drive channels, Graph, Jira) | `id`, `connection_id`, `external_id`, `resource`, `expires_at`, `renew_at`, `status` |
| `deliveries` | Webhook dedupe | `(org_app_id, dedupe_key)`, `received_at`; pruned after 7 days |
| `tool_calls` | Audit | `id`, `org_id`, `member_id`, `agent_id`, `agent_role`, `connection_id`, `tool`, `effect`, `decision`, `status`, `duration_ms`, `at` |
| `outbox` | Events to the bus | The shared outbox pattern (`internal/outbox`) |

**Sealing.**
- Every secret is AES-256-GCM under a fresh data key per record. The key is wrapped by KMS, with the encryption context `{org, table, row}` (oryxa-platform's envelope, and `internal/secrets` here).
- A row copied to another org or table won't decrypt.

## 7. Interfaces

### 7.1 HTTP API (internal unless marked public)

| Method and path | Caller | What |
|---|---|---|
| `POST /v1/connect` | apps (for web) | Start a sign-in. Request: `{org, member, app, level}`. Answer: `{url}`. Picks the org's app if one is live, else Oryxa's. |
| `GET /v1/integrations/oauth/callback` | **public** (browser) | Spend the `state`, exchange the code, seal the tokens, test the connection, then redirect to `return_to` |
| `GET /v1/connections?org&member` · `GET/DELETE /v1/connections/{id}` | apps, web | List, read and remove a connection. Remove revokes at the app, best-effort. |
| `PUT/GET/DELETE /v1/orgs/{org}/apps/{app}` | apps (admin routes) | An org's own app: set, view (never the secret, only a hint), retire |
| `POST /v1/orgs/{org}/apps/github/manifest` · `GET /v1/integrations/github/manifest/callback` (public) | admin | GitHub App **manifest flow**: create the org's GitHub App in one click and store what GitHub returns |
| `GET /v1/orgs/{org}/apps/{app}/slack-manifest` | admin | A ready-to-paste Slack app manifest for the org |
| `POST /v1/tools/call` | apps (router) | Call one tool for a member and agent (§12). This is what `apps` uses for native apps. |
| `POST /mcp` | agents (via apps) | The MCP gateway (§12) |
| `POST /v1/proxy` | apps | A raw call to an app through a connection, for what tools don't cover |
| `GET /v1/records?org&connection&stream&after&limit` | knowledge | The change feed (§10.5) |
| `POST /v1/integrations/hooks/{app}/{org_app_id}` | **public** (apps' webhooks) | Ingress (§11) |
| `GET /healthz` | ECS | Liveness |

### 7.2 Events (bus, through the outbox)

- `integrations.connection.created.v1`, `.updated.v1` (status changes), `.removed.v1`
- `integrations.org_app.expiring.v1`, `integrations.org_app.broken.v1`
- `integrations.event.received.v1`: a normalized app event for triggers:
  `{connection, app, trigger, object_id, occurred_at, dedupe_key, data}`
- `integrations.records.changed.v1`: a hint that `connection` has new feed entries up to `seq`. Knowledge pages the feed; the event carries no data.

**Consumed:**
- `teams.member.removed.v1` and `teams.organization.deleted.v1` revoke and purge.
- `agents.*` nothing.

### 7.3 apps as router

`apps` keeps Composio and Pipedream and adds a third broker, **native**, for toolkits listed in `APPS_NATIVE_TOOLKITS`:
- a connect for those apps goes to `POST /v1/connect`;
- their tool calls go to `POST /v1/tools/call`.

An existing connection keeps the broker it was made with. Agents see no difference.

## 8. Connectors

### 8.1 Contract

- oryxa-platform's `proto/oryxa/connector/v1/connector.proto`, copied into `integrations/proto/`:
  - `Describe() → manifest`
  - `Sync(SyncRequest) → stream SyncEvent`. Events are `Upsert`, `Delete`, `SetAcl` and `Checkpoint`. Each `SyncRequest` carries the stream, partition, mode (full, incremental or slice), checkpoint and an egress handle.
  - `Invoke(InvokeRequest) → InvokeResponse`. The request carries the tool, input, egress handle and acting user; the response carries the output or a typed `ToolError`.
- Connectors use **`gosdk`** (lifted from oryxa-platform): `gosdk.Main(server, id)` serves on the socket the host passes in.

### 8.2 Manifest (`connectors/<app>/manifest.json`)

Based on oryxa-platform's format, validated by `integrations/manifest`:

```jsonc
{
  "id": "github", "version": 1, "display_name": "GitHub",
  "connection_levels": ["personal", "org"],
  "auth": {
    "credential_modes": ["oryxa", "byo", "user_key"],
    "oauth2": { "authorization_url": "…", "token_url": "…", "pkce": false,
                "token_kinds": [{ "name": "user", "scopes": ["repo", "read:org"] }],
                "refresh": { "rotating": true } },
    "github_app": { "manifest_template": "github-app.json" }     // per-app extras
  },
  "base_url": "https://api.github.com",
  "egress_allowlist": ["api.github.com"],
  "verify": { "path": "/user", "token_kind": "user" },
  "rate_limit": { "scope": "connection", "per_hour": 5000, "max_concurrency": 4,
                  "headers": { "remaining": "x-ratelimit-remaining", "reset": "x-ratelimit-reset", "retry_after": "retry-after" } },
  "tools": [{ "name": "github.create_issue", "effect": "write",
              "description": "Create an issue in a repository. Use when the member asks to file or track a bug or task.",
              "input_schema": {…}, "output_schema": {…} }],
  "streams": [{ "name": "issues", "modes": ["full", "incremental"], "schedule": "rate(15 minutes)",
                "partition": "repos", "deletes": "webhook", "lookback": "PT5M",
                "document": { "title": "/title", "body": "/body", "url": "/html_url",
                              "author": "/user/login", "updated_at": "/updated_at" },
                "acl": "repo_collaborators" }],
  "webhooks": { "verification": { "scheme": "hmac_sha256_github" },
                "routing": "org_app", "events": { "issues": { "stream": "issues", "id": "/issue/id",
                "delete_on": ["deleted"] } }, "fallback_poll_s": 900 },
  "reauth_on": [401]
}
```

Additions over oryxa-platform's format:
- `credential_modes` uses our names;
- streams gain `document`, `lookback`, `partition` and `deletes` (`feed` \| `field` \| `webhook` \| `reconcile`);
- `rate_limit` gains `scope` and `headers`;
- `webhooks.events` maps an app event to a stream re-fetch or a trigger.

### 8.3 Layout

```
integrations/connectors/github/
  manifest.json
  main.go            # gosdk.Main
  sync.go  tools.go  model.go
  github-app.json    # GitHub App manifest template (BYO one-click)
  testdata/          # recorded responses (cassettes)
  *_test.go
```

### 8.4 The gate (CI, no live accounts)

`go run ./integrations/cmd/check <app>` passes only when **all** of these hold:
1. The manifest validates against the schema, and every tool, stream and webhook is consistent.
2. **Effects are honest:** any tool whose calls aren't all GET is `write`, `external` or `delete`.
3. **Description lint:** each description starts with a verb, says when to use the tool, is 40–400 characters, and is unique.
4. **Tool budget:** at most 15 tools per app; the rest goes through the proxy.
5. **Replay conformance,** run against the host with cassettes:
   - full sync to the end of pagination;
   - incremental with the emitted checkpoint returns nothing new;
   - a crash after a page resumes from the checkpoint with no loss or duplicate;
   - a 429 is retried after `Retry-After`;
   - an empty page;
   - a delete produces a tombstone;
   - each tool's happy path and one error.
6. `go vet`, `go test -race`, and no network in tests.

## 9. Auth and vault

### 9.1 Choosing the app

At connect time:
1. **The org's live app,** if it has one for that app (BYO).
2. Otherwise **Oryxa's app**, if the app has one in this environment.
3. Otherwise refuse, with "Your admin needs to set up <app>" and a link to the org's setup page.

`user_key` (a personal API key or token) is offered where the manifest allows it.

### 9.2 The org's own app (BYO)

| App | Setup | What we store |
|---|---|---|
| GitHub | **Manifest flow:** one click creates a GitHub App in their org from `github-app.json`; GitHub redirects back with a code we exchange | App id, client id and secret, private key, webhook secret |
| Slack | We generate a manifest; the admin creates the app from it and pastes back the client id, secret and signing secret | Same |
| Microsoft | Guided steps (or a script) for a single-tenant app registration | Tenant id, client id, secret (with **expiry**) |
| Google | Guided steps: project, consent screen **Internal**, OAuth client, enable APIs | Client id and secret |
| Notion | An internal integration; the admin pastes its token (key-based) | Token |

An "Internal" Google app or single-tenant Microsoft app **needs no Google verification or Microsoft publisher verification**, so BYO gets full Gmail and Drive scopes from day one.

### 9.3 Oryxa's own apps

- Created once per environment by the owner.
- Their secrets go in AWS Secrets Manager. They are loaded at start into `org_apps` rows with `org_id NULL`, never stored in the repo.
- Google on Oryxa's app uses **`drive.file`** (no restricted scopes) until verification is chosen. Gmail on Oryxa's app stays on Composio.

### 9.4 Refresh

- **Single flight per connection:** `pg_advisory_xact_lock(connection)`, then re-read. If another worker refreshed in the meantime, use its token.
- **Proactive:** refresh 10 minutes before expiry, from a `refresh` job.
- **Rotating refresh tokens** are saved in the same transaction as the access token.
- **Failures:**
  - `invalid_grant` marks the connection `needs_reauth`, emits an event and tells the member.
  - Transient errors back off. After 5, the connection is `degraded`, with a cooldown so it can't storm.

### 9.5 Egress handles and the proxy

- **Handles:** `eh1.<kid>.<claims>.<mac>`, HMAC-SHA256 with rotating keys. The claims name `{connection, token_kind, member, agent, exp ≤ 5 min}`.
- **The proxy:**
  1. checks the handle and the host allowlist;
  2. injects the token;
  3. sends through `safehttp` (SSRF-safe, no redirects to other hosts, body cap);
  4. on a 401, refreshes once and retries;
  5. strips credentials from the response;
  6. records the rate-limit headers (§13).

### 9.6 Org app health

- A daily job checks every org app: does the secret still work, and when does it expire?
- `expiring` warns the admin at 30, 7 and 1 days, in the app and by event. `broken` blocks new connects and shows an alert to the org's admins.

## 10. Syncs

### 10.1 Jobs

- **Queue:** the Postgres `jobs` table. Workers claim with `SELECT … FOR UPDATE SKIP LOCKED`, set `lease_until` (60 s) and heartbeat every 20 s. An expired lease returns the job to the queue.
- **Retries and failure:** `attempts` backs off exponentially with jitter. After 8 attempts the job is `failed`, and the connection or stream is marked `degraded`.
- **Priorities:** `refresh` > `refetch` (webhook) > `sync` (incremental) > `renew` > `slice` (backfill) > `reconcile`.
- **Fairness:**
  - **one** running job per (connection, stream);
  - workers take jobs round-robin across orgs within a priority.

### 10.2 Scheduling

- The scheduler loop runs every 30 s.
- It enqueues a `sync` for each `stream_state` whose `next_run_at` is past, as set by the stream's `schedule`.
- A webhook-backed stream polls at `fallback_poll_s` only while its subscription isn't healthy.

### 10.3 Checkpoints

- **Kinds:**
  - `datetime` (updated-at with `lookback` overlap);
  - `token` (an opaque change token: Drive `changes`, Graph delta, Gmail history).
- **Commit order:** a `Checkpoint` event is stored **in the same transaction as the records before it,** so a crash replays from the last checkpoint and upserts absorb the duplicates.
- **Caps:** a run is capped at 10 minutes or N pages, then re-enqueues itself to continue.
- **Expired change token** (Drive, Graph 410): fall back to a full sync plus a reconcile.

### 10.4 Backfills

On a new connection or a new stream:
1. **Plan slices:** time windows × partitions (for example months × repos).
2. **Store them** as `backfill_slices` rows.
3. **Run** each slice as a `slice` job at low priority.

Incremental sync starts at once, from "now", so new data isn't held up by the backfill.

### 10.5 Records and the change feed

- **Upsert:**
  - compute `hash` over the normalized `doc` and `acl`;
  - if the hash equals the stored one, do nothing;
  - otherwise write the row and bump `seq`.
- **Delete:** set `kind = delete`, clear `doc` (keeping the id), and bump `seq`. That is a tombstone.
- **A webhook-sourced write beats a stale poll:** a poll's upsert is ignored when the stored `source_updated_at` is newer.
- **The feed:**
  - knowledge pages `GET /v1/records?org&after=<seq>&limit` and gets upserts and tombstones in `seq` order, with `next` set to the last seq;
  - knowledge stores its own position;
  - `integrations.records.changed.v1` only tells it to look sooner.
- **Retention:** records live as long as their connection does. Removing a connection writes tombstones for all its records, and they are purged 30 days later. There is no silent pruning.

### 10.6 Deletes and permissions

Each stream declares how deletes are detected:

| `deletes` | How |
|---|---|
| `feed` | The change feed reports removed items (Drive `changes.removed`, Graph `@removed`, Gmail history) |
| `field` | A field marks it (Notion `archived`/`in_trash`, Linear `archivedAt`) |
| `webhook` | A delete event (GitHub `issues.deleted`, Slack `message_deleted`) |
| `reconcile` | A weekly job lists every id. **Only if the listing finished with no error,** ids not seen become tombstones. |

**ACLs:**
- Records carry `acl`: the app's principals (users, groups, "anyone in the org"), mapped to Oryxa members where an email or link matches.
- **A permission change is a change:** the record is re-emitted, and knowledge drops access for members no longer in the ACL.

## 11. Webhooks and polling

### 11.1 Ingress

- **Route:** `POST /v1/integrations/hooks/{app}/{org_app_id}`. The URL names the org's app, so each app's webhook is verified with **that app's own secret**.
- **Verification schemes,** ported from oryxa-platform:

  | Scheme | Used by |
  |---|---|
  | `hmac_sha256_github` | GitHub |
  | `slack_v0` | Slack (with a 5-minute replay window) |
  | `jwt` | Jira-style |
  | `channel_token` | Google push channels |
  | `client_state` | Microsoft Graph, plus Graph's validation handshake |

  Slack's `url_verification` and Graph's `validationToken` are answered inline.
- **Handling:**
  1. Dedupe on `(org_app_id, dedupe_key)`.
  2. Route to connections. The manifest's `routing` names the field: GitHub `installation.id`, Slack `team_id` + `api_app_id`, Graph `subscriptionId`.
  3. Enqueue `refetch` jobs.
  4. Answer within 2 s.

### 11.2 Webhooks are hints

- **Updates:**
  - a `refetch` fetches the named object through its stream's code, coalesced per id within 5 s;
  - it writes through §10.5, so it lands in the feed like a sync.
- **Deletes** are trusted from the payload after verification, because a deleted object can't be fetched.
- **Triggers:** an event the manifest marks as a trigger also emits `integrations.event.received.v1`, deduped. batching and agents consume it as they consume Composio's triggers today.

### 11.3 Subscriptions

- **Renewal:** apps that need renewing (Drive channels ≤ 7 days, Graph ≤ 3 days for mail, Jira 30 days) get a `renew` job before `renew_at`.
- **Lapsed subscriptions:** a failed renewal marks the subscription down, and the stream polls at `fallback_poll_s` until a renewal succeeds.

## 12. Tools and MCP

### 12.1 The tool catalog

- Tools come from manifests, named `<app>.<action>`, each with an `effect`.
- A member's catalog holds only the tools of apps they have a healthy connection to.

### 12.2 MCP gateway

`POST /mcp` speaks MCP over streamable HTTP. The caller is apps, with serviceauth carrying member and agent. It exposes three meta-tools:

| Tool | Does |
|---|---|
| `search_tools` | Ranked matches from the member's catalog, by keyword (later by embedding): name, description, effect |
| `get_tool_schema` | The input schema of one tool |
| `call_tool` | Run one tool |

Agents never get a flat list of hundreds of tools.

### 12.3 Every call

1. **Grant (I3):** the agent's grant must allow the app; a `write`, `external` or `delete` tool needs a write grant for it. Refused → `forbidden`, with the reason.
2. **Connection:** missing or `needs_reauth` → the result is a **connect link** ("Connect GitHub to continue: <url>"). The agent shows it and retries after the member connects.
3. **Confirmation:** a non-read effect needs the member's confirmation, as `apps` does today (the `OnlyReads` path).
4. **Invoke** the connector with a handle. The output is **projected to `output_schema`** and capped at 16 KB.
5. **Audit:** a `tool_calls` row names the member and agent.

### 12.4 Proxy

`POST /v1/proxy` covers what tools don't. It holds the same checks as a `write` tool, unless the method is GET.

### 12.5 Vendors' own MCP servers (later)

- For GitHub, Linear, Atlassian and Google, the gateway may also **front the vendor's remote MCP server** with the member's token.
- It pins a tool allowlist and classifies each tool's effect.
- This is an option, not a replacement: vendors' servers have no triggers or syncs, and several need their own sign-in.

## 13. Rate limiting

- **Shared buckets:** a token bucket per `(app, org_app_id, scope_key)`, shared by every worker and the proxy. `scope_key` follows the manifest: the app, the connection or the user.
- **Server signals win:** the remaining and reset headers adjust the bucket; `Retry-After` pauses it.
- **Adaptive concurrency:** halved on a 429, and raised again one step at a time.
- **One task at first:** buckets live in memory. A `ponytail:` note marks the move to Postgres-backed buckets once the service runs more than one task.
- **Priority under pressure:** interactive tool calls take tokens before syncs, so an agent isn't starved by a backfill.

## 14. Security

| Threat | Mitigation |
|---|---|
| Token theft through a buggy or malicious connector (agent-written) | Connectors never see tokens; handles last 5 minutes and name one connection; egress host allowlist; separate processes |
| Webhook spoofing | Per-org-app secrets, constant-time verification, replay windows, dedupe |
| OAuth CSRF or code injection | Hashed single-use `state` bound to org, member and app; PKCE where the app supports it |
| SSRF through the proxy | `safehttp`: https only, allowlist, resolved IPs checked (metadata, private, CGNAT), no cross-host redirects |
| Cross-org reads | Every query is keyed by `org_id`; envelopes are bound to org and row; feed and records are filtered by the caller's org |
| Agent overreach | Grants (I3), effect pinning, confirmation for writes, audit naming the agent |
| Leaked org app secret | Org-app rotation: a new row, old connections pinned until they reconnect. Retiring an app revokes. |
| Deleted or now-private content in knowledge | Tombstones, ACL changes as changes, reconcile |
| Prompt injection through synced content | Content is data; knowledge's answer guard and agents' Bedrock guardrail apply downstream |
| Secrets in logs | A structured logger with redaction; tokens are never logged; tests assert it |

## 15. Operations

- **Health:**
  - connection status is visible to the member;
  - org app status is visible to admins;
  - service health is on `/healthz`, plus queue depth.
- **Metrics** (CloudWatch, EMF):
  - calls, errors and 429s per app;
  - sync lag per stream (now minus the last checkpoint);
  - queue depth and the oldest job age per kind;
  - refresh failures;
  - webhook verification failures.
- **Alarms:**
  - oldest job over 30 minutes;
  - refresh failures over 5% for an hour;
  - verification failures spiking;
  - a connector crash-looping.
- **SLOs (initial):**
  - a webhook-driven change reaches the feed within 2 minutes (p95);
  - an incremental poll within its schedule plus 5 minutes;
  - a tool call adds under 300 ms of overhead (p95).
- **Runbooks** in `integrations/RUNBOOK.md`: rotate a handle key; rotate Oryxa's app secret; replay a stream from scratch; purge an org.

## 16. Testing

- **Unit:** each package, no network.
- **Connector replay (the gate, §8.4):** cassettes recorded once against a sandbox account by the owner or macbookpro, then committed. The loop agent may only re-record with an owner-provided sandbox.
- **Host conformance:** the same suite runs every connector through the real host and proxy, with a fake app server.
- **Database:** integration tests against Postgres. CI's service container; locally, a throwaway database.
- **Live (opt-in):** `test/live/integrations` against staging with real sandbox accounts, run by macbookpro before an app is switched on.

## 17. Moving out of `apps`

1. **P0:** create `integrations/` (`service.go` for `internal/host`, migrations, serviceauth, outbox), and build connector binaries into the image.
2. **P0: move** `apps/internal/signin` and `apps/internal/integration` and their tables:
   - `apps_oauth_clients` becomes `integrations.org_apps`;
   - native connections become `integrations.connections`;
   - the data is copied by a one-off migration (native connections are few or zero in production).
3. **P0:** `apps` gains the `native` broker, calling `integrations` for toolkits in `APPS_NATIVE_TOOLKITS`. The old in-`apps` native path is removed once the move is verified.
4. **Composio and Pipedream** are untouched.

## 18. Phases

Each phase is a set of small issues in the queue (`loop`, `phase:P<n>`). One PR closes one issue.

| Phase | Scope | Exit criteria |
|---|---|---|
| **P0 Core** | Service skeleton, config, migrations; vault (KMS envelopes); org apps and connections; connect and callback; refresh; egress handles and proxy; connector host and `gosdk`; manifest validator; `cmd/check` gate; move signin and the framework from `apps`; the `native` broker in `apps`; ECS service and tunnel route (macbookpro) | A fake app connects, refreshes, and serves one tool through the host and proxy on staging |
| **P1 Engine** | Job queue, scheduler, checkpoints, backfill slices, records store and feed, delete modes, rate limiter, webhook ingress and verifiers, subscriptions, the normalized trigger event, outbox events, health and alarms | A replay-tested fake stream backfills, resumes after a killed worker, honours 429s, tombstones a delete, and knowledge pages the feed |
| **P2 GitHub** | GitHub App (BYO manifest flow plus Oryxa app), repos, issues and PRs syncs, webhooks, ≤15 tools | Owner's live smoke test with a real org |
| **P3 Microsoft** | Graph app (BYO single-tenant plus Oryxa multi-tenant); mail, OneDrive/SharePoint, calendar and Teams via delta; Graph subscriptions | Same |
| **P4 Google** | Drive (BYO full, Oryxa `drive.file`), Calendar, Gmail (BYO); push channels; lift the Gmail and Calendar Go connectors from oryxa-platform | Same |
| **P5 Slack and Notion** | Slack member-data app (BYO manifest plus Oryxa), Notion (internal token plus public integration) | Same |
| **P6 Agents** | MCP gateway, connect-link results, the `tool_calls` audit, the org-app admin screen (with frontend) | An agent finds, calls and is refused tools as grants say |
| **Ongoing** | More streams and tools for these apps; a new app only when usage says so | |

**Ordering:** P2–P5 can run in parallel after P1. P6 can start after P0.

## 19. Risks

| Risk | Mitigation |
|---|---|
| The loop agent writes plausible but wrong connectors | The gate (§8.4); macbookpro reviews every PR; live smoke tests before switching an app on |
| Moving signin out of `apps` breaks Slack and Composio paths | Composio and Pipedream don't move; the move is tested by `apps`' suites; native is off in production |
| BYO onboarding friction | GitHub and Slack manifests; guided Google and Microsoft pages; Oryxa apps as the default |
| Rate-limit bans on Oryxa's shared apps | Shared buckets per org app, adaptive concurrency, priority for interactive calls |
| Change-token expiry or missed webhooks | Fallback polling, full resync plus reconcile |
| Licence contamination | Facts about APIs from anywhere; **code** only from MIT or Apache sources or our own repos; never Nango, Airbyte (ELv2), Pipedream or n8n |

## 20. Open questions

1. Microsoft: does Oryxa's own app ask for admin-consent scopes (Teams channel messages), or are those BYO-only?
2. Google verification for Oryxa's app: when (if ever) to apply? Until then Oryxa's Google app is `drive.file` only.
3. Should Slack's member-data app and the consent bot become one app later? Separate today.

## Appendix: sources

- Research reports (2026-10-15): Nango (architecture, licence), sync engines (Airbyte, Singer, Fivetran), landscape (Composio, Pipedream, Activepieces, n8n, Merge, Paragon, Arcade, Klavis, vendors' MCP servers).
- oryxa-platform: `proto/oryxa/connector/v1`, `connectors/gosdk`, `connectors/manifests`, `crates/oryxa-broker`, `crates/oryxa-webhooks`, `crates/oryxa-sync`.
- This repo: `apps/internal/signin`, `apps/internal/integration`, `docs/design/integrations-plan.md`, `docs/design/integration-providers-2026-10.md`, `docs/design/live-layer/06-agent-isolation.md` (grants I3, identity I2).
