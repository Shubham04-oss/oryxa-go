# The integrations service

Status: design, 2026-10-15, for the owner to approve. Nothing is built from it yet.

Inputs:
- The owner's direction of 2026-10-15.
- Research on Nango, Airbyte, Singer and Fivetran, and the wider landscape (Composio, Pipedream, Activepieces, n8n, Merge, Paragon, Arcade, Klavis and vendors' own MCP servers).
- A map of what already exists in two places: `apps/` in this repo, and the Rust stack in `oryxa-platform`.

## What the owner decided

1. **Integrations are their own service,** and it handles all of its own operations. The rest of Oryxa sees only its API and its events.
2. **It powers everything an integration needs:**
   - auth and a token vault;
   - syncs (for knowledge);
   - webhooks and polling (for triggers);
   - actions as MCP tools (for agents);
   - a raw proxy;
   - the operations work: rate limits, retries and health.
3. **Native only for the main apps:** Google, Microsoft, GitHub, Slack and Notion. Composio keeps the long tail behind `apps`' router, and Pipedream stays a development-only fallback.
4. **Two ways to sign in, for every app:**
   - **The org's own app (BYO):** the org's client ID and secret, through which its members complete OAuth, plus API keys where an app uses keys.
   - **Oryxa's own app** for orgs that don't bring one.
5. **No Durable Object per connection, and no scatter of Workers:** a proper service.
6. The master key may come from the **Cloudflare Secrets Store** to save cost (§5 weighs this against KMS).

## What already exists

| Piece | In oryxa-go (`apps/`) | In oryxa-platform (Rust) |
|---|---|---|
| **Org apps (BYO) and Oryxa apps** | **Built and live:** `apps_oauth_clients` per org, or `''` for Oryxa's default; replacing a client keeps old connections working (`apps/internal/signin/clients.go`) | Store only (`broker_tenant_apps`), no admin API |
| **OAuth, refresh, revoke, health** | Built (`apps/internal/signin`): PKCE, rotating refresh, revoke styles, health | Built (`oryxa-broker`): single-flight refresh with an advisory lock, `client_ref` pinning, refresh-once on 401 |
| **Vault** | Envelope-encrypted (`internal/secrets`) | A KMS data key per record, bound to tenant and row |
| **Connectors never see tokens** | No: connector code calls the app with the token | **Yes:** short-lived HMAC **egress handles**; only the broker's proxy holds tokens |
| **Connector contract** | Go manifest plus in-process Go code (`apps/internal/integration`) | **gRPC `Describe`/`Sync`/`Invoke`** over a Unix socket or Lambda (`proto/oryxa/connector/v1`), in any language |
| **Manifests** | GitHub, Gmail, Google, Calendar, Drive, Notion, Slack (~100 lines each, mostly auth) | Gmail, Calendar, Drive, Slack, Jira (19–49 KB: tools with `effect`, streams with `track_deletes`, webhooks, rate limits) |
| **Connectors with real code** | Template only | **Gmail and Calendar in Go** (`gosdk`), Drive and Slack in TS, Jira in Rust, ~200 tests |
| **Syncs** | Readers, no engine | `oryxa-sync`: SQS FIFO paging, checkpoint stored only after the sink is durable, cron fan-out. **No change feed;** hard deletes |
| **Webhooks** | Manifest field, verification per integration | Verifiers for `hmac`, `jwt`, `channel_token` and Graph `client_state`; a Cloudflare ingress Worker. **No consumer, renewal or polling fallback** |
| **Tools for agents** | Composio tools through `apps` | Gateway authorize-then-invoke with `effect` pinning. **No MCP** |
| **Rate limiting** | none | Declared, not enforced |
| **Tests** | Conformance suite, recorded responses, `cmd/check` | Conformance kit, fixtures, end-to-end through the host |
| **Deployed** | Yes, inside the single binary | **No** (deployment on hold) |

oryxa-platform's core is Rust and built on WorkOS and SpiceDB, which conflicts with oryxa-go's Cognito, Cedar and Go. Its **designs and its Go connectors** carry over; its Rust services don't.

## The design in one picture

```
                         ┌──────────────────────── integrations service (Go, its own ECS service) ─────────────────────────┐
 agents ──MCP──▶ apps ──▶│ API: connect · connections · tools (MCP) · proxy · records feed · admin (org apps)               │
 (router)        │       │                                                                                                  │
   └─▶ Composio  │       │  vault + broker ── egress proxy ──▶ app APIs     job queue (Postgres SKIP LOCKED)                 │
   └─▶ Pipedream │       │       ▲   (holds tokens)   ▲                        ├─ sync runs (backfill · incremental · reconcile)│
                 │       │       │ handle             │ handle                  ├─ polls                                        │
 knowledge ──────┼──────▶│  connector host ──gRPC (Unix socket)──▶ connector processes (Go, one per app)                     │
 (records feed)  │       │       │                                                                                          │
                 │       │  records store + change feed      rate limiter (per app × org app × scope)                     │
 app webhooks ──▶ edge ──┼──▶ webhook ingress (verify → queue)          outbox ──▶ bus                                     │
                         └──────────────────────────────────────────────────────────────────────────────────────────────────┘
```

## 1. Shape and hosting

- **One Go service, `integrations/`, in this repo,** with its own Go module, its own ECS service and its own IAM role.
  - It uses the same image pipeline as the core binary.
  - It's a separate deployable with its own database login, so a compromise of agents or apps can't read the vault.
- **Connectors run as separate processes inside its container.** Each is a Go binary serving the connector contract on a private Unix socket, as oryxa-platform's `gosdk` does.
  - A connector panic or leak takes down one connector, not the service.
  - A connector can't read the vault or the database. That matters because an agent will write connectors.
  - No Lambda sprawl: one container holds the host and its connectors.
- **Contract with the rest of Oryxa:**
  - **apps** stays the router: native apps go to this service, the long tail to Composio or Pipedream. Agents don't change.
  - **knowledge** reads the records feed (§4) instead of pulling apps itself.
  - **The bus** carries `integrations.*` events through the shared outbox.
  - **Service auth** is the existing KMS ES256 `serviceauth` both ways, with the agent and role claims from I2.

ECS over Cloudflare Containers because polling and syncs keep it busy (so scale-to-zero saves nothing) and its database, bus and callers are on AWS.

## 2. Auth: two modes, one vault

- **Org apps (BYO) and Oryxa apps:** keep `apps_oauth_clients` as is, and move it into this service with its data. Add what's missing:
  - **An admin API and screen** for an org's apps: GitHub App **manifest flow** (one click), Slack **app manifest**, guided steps for Microsoft and Google, and an API key or token form for Notion and key-based apps.
  - **The webhook signing secret** stored with each org app (a GitHub App's or Slack app's own secret).
  - **Expiry tracking:** Microsoft and Google client secrets expire. The admin is warned at 30, 7 and 1 days, and the org's setup page shows it. An org app's health is separate from each member's connection, because one expired secret breaks the whole org.
- **Connections** are pinned to the client they were made with (`client_ref`, from oryxa-platform). Replacing an org's app keeps old connections refreshing until their members reconnect.
- **Refresh:** a single flight per connection under a **Postgres advisory lock**, then a re-read: if someone else refreshed, use their token (Nango's pattern, without its Redis). Add a cooldown and an "exhausted" flag so a broken token doesn't retry forever.
- **Egress handles (from oryxa-platform):**
  - a connector gets a handle naming one connection, one token kind and who it acts for, valid for 5 minutes;
  - the service's egress proxy swaps it for the real token, enforces the host allowlist (`safehttp`), refreshes once on a 401, and strips credentials from the response;
  - so **connectors never see tokens.**

## 3. Connectors

- **The contract is oryxa-platform's gRPC `Describe` / `Sync` / `Invoke`,** adopted as is (`proto/oryxa/connector/v1`):
  - Sync streams `Upsert`, `Delete`, `SetAcl` and `Checkpoint` events.
  - Invoke runs one tool.
  - **Lift `gosdk` and the Gmail and Calendar connectors** (Go, ~2k lines each, with tests).
- **The manifest is oryxa-platform's format,** extended with what oryxa-go's has:
  - **identity:** `id`, `version`, `connection_levels`;
  - **auth:** `auth.credential_modes` (`platform`, `tenant_app` = BYO, `user_key`), OAuth URLs, PKCE and token kinds;
  - **calling the app:** `base_url`, `egress_allowlist`, `rate_limit`;
  - **tools** with `effect: read | write | external | delete` and schemas;
  - **streams** with modes, `track_deletes` and a schedule;
  - **webhooks** with the verification scheme, routing, subscription TTL and renewal, and `fallback_poll_s`.

  Add **`document`** (the title, body, URL, author, updated time and ACL fields knowledge needs) and **`lookback`** (overlap for `updated_at` cursors).
- **Where they live:** one directory per app under `integrations/connectors/<app>/`, holding the manifest, a Go connector, `testdata/` with recorded responses, and tests.
- **The gate (CI, no live accounts):**
  1. The manifest validates.
  2. Tool descriptions pass lint (they start with a verb, say when to use the tool, unique names, no pasted API docs).
  3. Every non-GET call is declared `write`, `external` or `delete`.
  4. Replay tests cover pagination to the end, a 429, an empty page, a delete, resume from a checkpoint, and a second incremental read that returns nothing.
  5. At most about 15 tools; anything else goes through the proxy.

## 4. Syncs and the records feed

- **The job queue is Postgres** (`SKIP LOCKED`, leases, heartbeats, attempts), holding sync runs, polls, backfill slices and renewals. There's no SQS FIFO per page; the queue lives next to the state it guards.
- **Scheduling:** each stream declares its schedule. A scheduler loop enqueues due runs, and priorities are **incremental > webhook re-fetch > backfill > reconcile**, with one run per (connection, stream) at a time and round-robin across orgs.
- **Checkpoints:** typed (`datetime` with lookback, or an opaque `token`), per (connection, stream, partition). A checkpoint is stored **only after the records before it are committed** (oryxa-platform's host rule). Runs are capped and resume.
- **Backfills** are planned as slices (time windows × partitions, e.g. repos or channels) stored as rows, so a crash resumes mid-backfill. They run at low priority.
- **Records store with a change feed** (Nango's idea, missing in oryxa-platform):
  - One row per (connection, stream, record id), with a content hash, `kind` (upsert or delete), a monotonic `seq`, and the ACL.
  - An unchanged hash writes nothing, so knowledge never re-embeds unchanged records.
  - A delete keeps a **tombstone**, so knowledge removes the record's chunks.
  - **knowledge pages `GET /v1/records?after=<seq>`.** That's the whole contract.
  - **Retention is ours to choose,** with no silent pruning.
- **Deletes, declared per stream:**
  - `feed`: the change feed reports removals (Drive `changes`, Graph delta, Gmail history);
  - `field`: an archived or trashed field;
  - `webhook`: a delete event;
  - `reconcile`: list every id on a slow schedule and tombstone the missing ones, **only after a complete listing with no errors.**

  A change in permissions is treated like a delete for the members who lost access.

## 5. Webhooks and polling

- **Ingress:** one URL, `https://hooks.oryxa.in/v1/<app>/<org-app-or-route>`.
  - It can run in the service, or in oryxa-platform's existing Cloudflare Worker forwarding to the queue. Recommendation: in the service first; move it to the edge only if volume needs it.
  - It verifies with the **org app's own secret**, using oryxa-platform's verifiers (`hmac`, `jwt`, `channel_token`, `client_state`) ported to Go.
  - It answers Slack's challenge and enqueues. No work happens on the request.
- **A webhook is a hint, not data:**
  - It re-fetches the named object, coalesced per id.
  - A **delete** payload is trusted after verification, since the object can't be fetched.
  - A poll can't overwrite a fresher webhook write (Nango's `ignore_if_modified_after`).
- **Subscriptions** (Drive channels, Graph subscriptions, Jira webhooks) are renewed by the queue before `renew_at`. A lapsed subscription falls back to polling at `fallback_poll_s`.
- **Polling** for apps without webhooks uses the same stream code on a schedule.
- **Triggers for agents:** the service emits a normalized `integrations.event.received.v1`:
  - `{connection, app, trigger, object_id, occurred_at, dedupe_key, data}`, deduped on (connection, trigger, dedupe_key);
  - batching and agents consume it as they consume Composio's triggers today.

## 6. Tools as MCP

- **One MCP endpoint, with search-and-execute meta-tools:** `search_tools`, `get_tool_schema` and `call_tool`. There's no flat list of hundreds of tools.
  - Tools come from manifests, named `<app>.<action>`.
  - A member sees only tools for the apps they've connected.
- **Every call is checked in order:**
  1. **The agent's grant (I3):** the app is allowed, and so is the write.
  2. **`effect` pinning:** a `write`, `external` or `delete` call asks for confirmation as today.
  3. The call is audited, naming the agent (I2).
- **Missing connection or scope:** the result is a connect link (Arcade's pattern). The agent shows it, the member connects, and the agent retries.
- **Responses are trimmed** to declared output fields, so models get small results.
- **Vendors' own MCP servers** (GitHub, Linear, Atlassian, Google) are an option per app later, proxied with the member's token and a pinned tool allowlist. They're not a replacement: they have no triggers or syncs.

## 7. Operations

- **Rate limiting, shared by every worker:**
  - a token bucket per (app, org app or Oryxa app, scope: app, connection or user), as each manifest declares;
  - it honours `Retry-After` and the remaining and reset headers, and narrows concurrency on 429s;
  - it protects our Oryxa apps, where all orgs share one quota, and each org app's own quota.
- **Health:**
  - each connection is `ok`, `needs_reauth`, `degraded` or `error`; each org app is `ok`, `expiring` or `broken`;
  - a change emits events, and members or admins are told in the app (and Slack, later);
  - logs per connection, with request and token IDs, never tokens.
- **Revocation:** when a member leaves the org, or the org is deleted, their connections are purged and revoked at the app, best-effort and retried. This is oryxa-platform's revocation consumer, ported.
- **Metrics:** calls, errors and 429s per app and org; sync lag per stream; queue depth.

## 8. Moving out of `apps`

1. Create `integrations/` with the service skeleton, its own database role, and the outbox and serviceauth wiring.
2. **Move** `apps/internal/signin` and `apps/internal/integration` into it, along with their tables (`apps_oauth_clients`, native connections). `apps` keeps Composio and Pipedream and gains a "native" broker that calls this service.
3. Add the egress proxy and handles, and the connector host with the gRPC contract. Port the 7 existing manifests to the new format.
4. Lift `gosdk` and the Gmail and Calendar connectors from oryxa-platform.

Nothing changes for members while this happens. Native connections are few today (`APPS_ORYXA_TOOLKITS` is empty in production).

## 9. Build order

| Phase | What | Done when |
|---|---|---|
| **P0** | Skeleton, move signin and the framework, egress proxy, connector host, the gate | Existing tests pass in the new service; one connector runs through the host |
| **P1** | Job queue, scheduler, checkpoints, records store and feed, deletes, rate limiter | A replay-tested stream syncs, resumes after a crash, and knowledge pages the feed |
| **P2** | **GitHub** (GitHub App, manifest flow, webhooks, issues/PRs/repos sync, tools) | Owner's smoke test on staging with a real org |
| **P3** | **Microsoft Graph** (mail, OneDrive/SharePoint, calendar, Teams; delta syncs; subscriptions) | Same |
| **P4** | **Google** (Drive, Calendar, Gmail with a BYO Internal app; Oryxa's own app uses `drive.file`) | Same |
| **P5** | **Slack** (a second app for members' data) and **Notion** | Same |
| **P6** | MCP gateway, connect-link results, org-app admin screen | An agent finds, calls and is refused tools as grants say |
| **Then** | **The loop agent:** it extends existing connectors (more streams and tools) and adds apps only when usage says so | |

P2 to P5 can run in parallel once P1 lands. P6 can start alongside P2.

## 10. The loop agent's lane

- **Paths:** it works only in `integrations/connectors/<app>/`. It never touches the core, never merges, and never creates accounts or OAuth apps.
- **Daily:**
  - it reads review comments on its open PRs and fixes them first;
  - then it takes the next item from `integrations/connectors/QUEUE.md`, which ranks new streams and tools for existing apps, and apps the usage numbers put next;
  - it's done when the gate (§3) is green, and opens one PR.
- **Owner steps** (creating Oryxa's apps, a live smoke test) are collected weekly, not asked daily.
- **Reading other projects:** only facts about APIs, such as endpoints, cursor fields and quirks, from any source. **Code** comes only from MIT or Apache sources (the GitHub MCP server, Activepieces pieces) or our own oryxa-platform. **Never** from Nango or Airbyte (ELv2), Pipedream (source-available) or n8n (Sustainable Use).

## Decisions for the owner

1. **Connectors as separate processes behind egress handles** (recommended: safer for agent-written code), or in-process Go like `apps` today (simpler, but a connector bug reaches tokens)?
2. **Master key:** Cloudflare Secrets Store as asked, or AWS KMS (already used for envelopes here, about $1 a month per key)? KMS keeps every token decrypt audited in CloudTrail and needs no cross-cloud call on each one. **Recommendation: KMS for the vault key; Secrets Store for edge-side secrets.**
3. **Webhook ingress** in the service first (recommended), or oryxa-platform's Cloudflare Worker from day one?
4. **First app after the core:** GitHub (recommended: no review gates, webhooks and engineering-team value), or Microsoft?
5. **oryxa-platform:** keep it as the reference for this work, or plan to merge more of it later (the broker, its SpiceDB model)? This doc takes only its contract, manifest, verifiers and Go connectors.
