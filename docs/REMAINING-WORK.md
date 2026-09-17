# Remaining work

Everything known to be unfinished across the repository, in one place. Each
entry says what is missing, why it matters and what it needs — a decision,
code, credentials or an operator — and links to the document that holds the
detail. Those documents stay the source of truth for their own services; this
is the index.

Nothing here is a promise about behaviour. Where something was confirmed — by
a command, by reading the code, or against a provider's documentation — it
says so; where it was only noticed in passing, it says that instead.

Brought up to date on 2026-09-17, after the `apps` service replaced
connections.

**Direction.** Third-party apps reach first users through Composio rather than
Oryxa's own connection layer. The `apps` service is that: built and tested, and
not yet run against a real Composio project. The `connections` and `integrations` services,
their event contracts, the generated provider clients in
`internal/providerapi`, knowledge's GitHub, Notion and Slack sources, and
Krude's integrations toolboxes were archived on 2026-09-17; everything is at
tag `archive/connections-and-integrations-2026-09-17`, and
`git checkout archive/connections-and-integrations-2026-09-17 -- <path>`
restores any of it. The provider research in `docs/research/` stays as
reference.

1. [The product loop is not closed](#1-the-product-loop-is-not-closed)
2. [Web app](#2-web-app)
3. [Decisions only the owner can make](#3-decisions-only-the-owner-can-make)
4. [Ready to build](#4-ready-to-build)
5. [Platform](#5-platform)
6. [Never run against the real dependency](#6-never-run-against-the-real-dependency)
7. [Before production](#7-before-production)
8. [Housekeeping](#8-housekeeping)
9. [Recently closed](#9-recently-closed)

## 1. The product loop is not closed

Each service works on its own. The paths between them — the ones that let an
agent act in an organization's apps, answer from its own documents, or react
to something that happened — do not exist yet. This is the largest gap.

### Apps has never run against Composio

The `apps` service is built: connecting an app through Composio's hosted flow,
tools behind Oryxa's member identity and write approvals, verified webhooks,
four MCP tools for agents, and Krude's `apps` toolbox over them
([apps/README.md](../apps/README.md)). Every test runs against fakes, so
nothing has touched Composio, and no member has connected anything.

A first live pass ran on 2026-09-18 against a throwaway Composio project
through a cloudflared tunnel: the catalog, tool search and its read-only marks,
auth-config and connect-link creation, the webhook subscription provisioner,
webhook verification in all four of its cases, the MCP endpoint over plain
HTTP, and every refusal
([apps/README.md](../apps/README.md#what-has-and-has-not-been-run)). It found
two defects, both fixed: a sentinel's own words leaking into the wording a
model reads, and the read route answering `not_connected` where the honest
answer was `not_read_only`.

What is still unrun: **connecting an account end to end**. The hosted flow
needs a member at a browser, so nothing past activation — a tool against a real
app, a subscribed trigger, a real delivery — has happened. Treat those as
proven against a stand-in only.

Then, in order:

- **An OAuth app of Oryxa's own**, for the apps Composio holds no app for.
  Those are refused today as not connectable, so the catalog is smaller than
  Composio's. Composio's own Google app is used first anyway, since an
  unverified Google app of Oryxa's is capped at 100 new users and, in testing,
  loses refresh tokens after seven days; moving later means members reconnect
  once.
- **Triggers do not start agent runs** (see below) — a trigger delivers and
  publishes, and nothing acts on it.
- **A tool allow-list per organization**, for an admin who wants writes limited
  to certain apps.

Known risk to revisit before security-conscious customers: in May 2026 an
attacker reached Composio's shared execution layer and took about 5,000 GitHub
tokens and 5,241 customer API keys, including tokens under customers' own OAuth
apps. Self-hosting or customer-managed keys are on Composio's enterprise tier.

### Knowledge has nothing to read

Its provider sources were archived, and its files source needs a grant to read
as the member who registered it, so the binary mounts no extractor
([knowledge/TODO.md §1](../knowledge/TODO.md#1-what-still-blocks-every-pass)).
App sources return on Composio, which never hands out a token: an extractor
will call through Composio rather than with a credential of its own.

### Knowledge does not feed rag

Knowledge publishes `knowledge.document.published.v1` and
`knowledge.document.removed.v1` for every document it extracts, and nothing
consumes them. Rag's consumer (`rag/internal/events/consumer.go`) takes only
auth, teams and files lifecycle events, and nothing outside rag calls its
document API. Confirmed by reading both consumers and searching for callers.

Rag's document model already has a source kind for extracted text
(`external`: the text and the URI it came from), so what is missing is the
bridge, not a new kind of document. Building it has to settle:

- **Which corpus** a source's documents land in, who creates it, and with
  which embedding model — a corpus pins its model for good.
- **Scope.** Every knowledge document carries its scope, team and member. A
  member-scoped source's documents must not land in a corpus the whole
  organization can search
  ([knowledge/TODO.md §7](../knowledge/TODO.md#7-for-whoever-is-building-the-retrieval-side)).
- **Omitted bodies.** A body over 128 KiB is not carried in the event, and
  the endpoint that serves it answers only the source's creator or an org
  admin — not a service acting on its own.
- **Removal.** `knowledge.document.removed.v1` has to remove the rag document
  it caused, so the mapping between the two has to be kept.
- **Where extraction lives**, which is a decision (§3.3).

### Agents cannot search a corpus

No toolbox in `agents/primary` searches rag or cites from it, so Krude cannot
answer from an organization's own documents
([rag/README.md, Not yet](../rag/README.md#not-yet)).

### There is no run API

`agents/primary/service.go` mounts a health check and nothing else. Only
`cmd/devui` runs Krude, and nothing outside tests forwards a present member's
token or mints an agent token for an absent one
([agents/primary/README.md](../agents/primary/README.md#acting-for-a-member)).
The service's write deadline is deliberately zero so that a run can stream;
a run handler has to set its own through `http.ResponseController`.

### Triggers do not start runs

Nothing turns an event from an app into an agent run as the member it belongs
to. Apps publishes `apps.trigger.received.v1` with the app's payload, and no
consumer reads it. The intended shape batches: events collect for a while, across apps,
before one run wakes the agent, rather than one run per event. A trigger that
arrives before the batch would close gains nothing, so a polled trigger is as
good as a pushed one; and batching does not relax verification, because a
forged event inside a batch still reaches the agent. Still open: what closes a
batch — a count, a maximum wait, or both, so that a lone event is not held
indefinitely — whether batches are per member or per subscription, and
whether anything, such as a direct mention, should skip the wait.

### Guardrails are off

`agents/primary/internal/guardrail` screens input and output through Bedrock's
`ApplyGuardrail` and is tested, but nothing wires it in
([agents/primary/README.md, Guardrails](../agents/primary/README.md#guardrails)).
Wiring it turns streaming off for the model calls it screens.

## 2. Web app

The web app is to be refactored, and until then it runs ahead of the backend:

- **Its connection screens call archived APIs.** `/app/connections`, the
  tools settings page and onboarding's tool step call connections' routes,
  which the binary no longer serves. Apps serves a different shape —
  `/v1/organizations/{org}/apps/connections` and a hosted link the browser is
  sent to ([apps/README.md, API](../apps/README.md#api)).
- **Agent conversations.** `/app/threads` renders a `ServicePlaceholder`.
- **Agent grants.** No screen grants or withdraws an agent's access, although
  auth's API does both
  ([auth/README.md, Agent grants](../auth/README.md#agent-grants)).
- **Knowledge sources** and **rag corpora and documents** have APIs and no
  screens.
- `pnpm check`, `typecheck`, `test` and `build` have not run since PR #17
  pointed the development proxy at the one binary.

## 3. Decisions only the owner can make

### 3.1 How agent wake-ups batch

What closes a batch, what it is keyed by, and what skips the wait (§1). This
also sets how often an app is polled for a trigger that cannot be pushed.

### 3.2 What a member agrees to when knowledge reads files as them

`Grants` would let a background pass act as the member who registered a
files source, through auth's agent grant. That is a consent question before
it is a code question: whether registering a source is itself the member's
agreement, or a separate grant the member gives and can withdraw.

### 3.3 Rag's boundary and defaults

From [rag/README.md, Open decisions](../rag/README.md#open-decisions):

- **Where extraction lives.** Rag takes extracted text and refuses anything
  else, so that there is one answer about what a document says; knowledge
  extracts. Settling this comes before building the bridge in §1.
- **Whether reranking defaults on.** It is the largest quality gain per query
  and the largest latency cost, and opt-in today.
- **Whether answering has a default model.** There is none, so a deployment
  that forgets to set one retrieves but cannot answer. A guessed default would
  fail at the first question instead of at startup.

### 3.4 Knowledge's content

- **HTML extraction.** Knowledge reads plain text, Markdown, CSV and JSON.
  Reading HTML means promoting `golang.org/x/net` from an indirect dependency
  to a direct one, which was left as a choice rather than made unilaterally
  ([knowledge/TODO.md §8](../knowledge/TODO.md#8-decisions-that-are-not-mine-to-make)).
- **Where document bodies live.** Postgres today, up to 4 MiB each and with no
  cap per source. Object storage may be the better home
  ([knowledge/TODO.md §4](../knowledge/TODO.md#4-worth-a-second-look)).

## 4. Ready to build

No decision stands in the way of these.

### The Composio project key is in two services

Knowledge's GitHub, Notion and Slack sources read through Composio's proxy with
`COMPOSIO_API_KEY` of their own, because a scheduled pass has no member to act
as. Apps offers `POST /v1/organizations/{org}/apps/read`, which derives the
account from who is asking and refuses anything but a read — but nobody is
asking during a pass.

That key reaches every user in the Composio project, so having it in two
services doubles where it can leak from. Moving knowledge behind apps' read
route needs a token that acts as the member who registered the source, which is
the same thing knowledge's files source waits on (§3.2). Until then both
services read it from the same `.env`, and rotating it means rotating both.

### Knowledge

From [knowledge/TODO.md §3, §5 and §6](../knowledge/TODO.md#3-finished-but-incomplete):

- **Team files** need their team ids up front, since files cannot enumerate
  teams. Asking teams first would let a source index everything an
  organization holds.
- **`Files.Fetch` re-lists the organization for every page**, which is
  quadratic in the number of files.
- **An end-to-end test** in `internal/e2e`, like the other services have.

### Rag

From [rag/README.md, Known limits](../rag/README.md#known-limits):

- **No re-index path** moves a corpus to a new embedding model.
- **Text search is fixed at `english`.** It is a stored generated column, so
  changing it is a migration, not a setting.
- **Answers do not stream.**
- **Oryxa's guardrails cover Bedrock answers only.** A Vertex answer gets
  Vertex's own safety settings.
- **No per-organization limit on embedding spend.** Concurrency and retry
  limits are per process.
- **`rag_vectors_other` is an exact scan**, slow in proportion to its size,
  for any width without a table of its own.
- **Token counts are a heuristic**, biased to over-count.

### Customers' own APIs

A customer's internal API, described by an OpenAPI specification and offered
to agents as MCP tools. Composio does not cover these, so it stays Oryxa's to
build.

## 5. Platform

### No CI

There is no `.github/workflows`, and no other CI configuration. Pull requests
merge with nothing building, vetting or testing them. A first pipeline would
build and vet every package and run the race tests, which need nothing; run
the database suites against a `pgvector/pgvector:0.8.6-pg17` service
container with each `*_TEST_DATABASE_URL` set; and run the web checks.

### No production deployment

The repository builds the image (`Dockerfile`, `compose.oryxa.yaml`), and each
service has a provisioner for the AWS resources it owns — event rules and
queues, the files bucket, the Cognito pools, SES delivery, vector indexes —
under `*/infra/*/cmd/provision`. Nothing provisions where the binary runs, its
PostgreSQL, the network or the public edge, and nothing builds and ships the
image. The web app deploys by hand with `pnpm deploy`. Where to host is
deliberately undecided for now.

### Services call one another over loopback HTTP, uncached

Files, knowledge, rag and teams check a caller's membership by calling auth's
`GET /v1/organizations/{id}` over HTTP, with no cache. In one binary
`AUTH_URL` defaults to the process itself, so each check is a request the
process makes to itself. There are nine client packages like this: five for
auth, two for teams and two for files. Calling in process when the target
runs alongside, and over HTTP only when a `*_URL` points elsewhere, would
remove the hop and most of the copies.

### Traces stop at every internal call

`internal/observability` wraps incoming handlers, but the service clients use a
plain `http.Client`, so no trace context leaves the process and each hop starts
a new trace. The root README's "W3C trace propagation" holds for incoming
requests only. An `otelhttp` transport in one shared client constructor would
carry it.

### Rag relies on teams to install `vector`

Only teams' migrations run `CREATE EXTENSION`. Migrating rag on its own
(`ORYXA_SERVICES=rag`) against a database without `vector` fails. The root
README's position is that the database image or control plane owns
extensions; until one does, the order in `cmd/oryxa/services.go` is
load-bearing, and `TestTeamsMigratesBeforeRag` pins it.

## 6. Never run against the real dependency

Fakes and stub servers prove the request this code builds and the response it
reads. They prove nothing about whether the provider agrees.

| Area | What has never run | What it needs |
| --- | --- | --- |
| Rag | Bedrock and Vertex embedding and answering; Bedrock reranking, the least exercised code in the service; S3 Vectors; the provisioner, which has never created anything | An AWS account — S3 Vectors has no local emulation — and a Vertex project ([rag/README.md](../rag/README.md#what-has-and-has-not-been-run)) |
| Apps | Connecting an account end to end, and therefore every path past it: a tool against a real app, a subscribed trigger, a real delivery, and the outbox reaching the bus. The catalog, tool marks, link creation, the webhook provisioner, webhook verification, MCP and the refusals all ran for real on 2026-09-18 ([apps/README.md](../apps/README.md#what-has-and-has-not-been-run)) | A member at a browser to finish Composio's hosted page, and LocalStack up for the bus |
| Auth | The agent grant against a provisioned Cognito agent client | The agent client provisioned |
| Journey | `test/run.sh` has not run since PR #17 rewrote it for the one binary, and it covers teams, files and email only | Docker |

## 7. Before production

Each service's README has a Provision section for the resources it owns.
These are the steps that are easy to miss, because they live outside the
service that fails without them.

- **Billing's live mode.** Create the products in Dodo live mode and point the
  catalog's `product_id`s at them — `plans.example.json` holds placeholders —
  set `DODO_PAYMENTS_ENVIRONMENT=live_mode` with a live API key and webhook
  secret, and decide on Dodo's subscription grace period
  ([billing/README.md, Deploy](../billing/README.md#deploy)).
- **Database extensions.** Install `vector`, `ltree`, `pg_trgm`, `btree_gist`
  and `btree_gin` in the managed database, or let the migrating role create
  them, and set `POSTGRES_REQUIRED_EXTENSIONS` so a missing one fails startup
  rather than a query (§5).
- **Composio.** A project, a scoped API key and IP allowlisting, the webhook
  subscription (`go run ./apps/infra/composio/cmd/provision`, whose secret is
  shown once), and — for security-conscious customers — a decision on the
  self-hosted or customer-managed-key tier (§1).

## 8. Housekeeping

- **Building under a sandbox** that cannot write `$HOME` needs `GOPATH` and
  `GOCACHE` pointed inside the workspace; both are gitignored.

## 9. Recently closed

Kept short; the pull requests hold the detail.

- **PR #13** closed six connections gaps and built HubSpot.
- **PR #17** runs every service from one static binary, `cmd/oryxa`.
- **PR #18** gave nine services one outbox, `/internal/outbox`, with owned
  claims and a week's retention; recorded applied migrations in
  `oryxa_migrations`, so each file runs once; added knowledge's periodic full
  sweep; paged rag's document lists and tested its access rules, events and
  clients; and fixed auth's invitation links and agent-grant `return_to`,
  along with three bugs found while proving those.
- **PR #20** built `/apps`: a member's third-party apps on Composio, with the
  safety layer Oryxa keeps — the connection a call uses derived from auth, a
  write needing an idempotency key and the member's confirmation, verified
  webhooks, four MCP tools for agents, and knowledge's GitHub, Notion and Slack
  sources back on top of it. A first live pass against a real Composio project
  found and fixed two defects
  ([apps/README.md](../apps/README.md#what-has-and-has-not-been-run)).
- **Housekeeping, 2026-09-18**: the eleven merged branches are off the remote
  (the suspended Linear work is a stash, untouched), and files' check
  constraint no longer accepts the retired `integrations.*` purposes —
  migration 004 re-points any straggler at `upload`, which cannot extend its
  life because every object carries its own `expires_at`.
- **The connections archive** removed connections, integrations, their event
  contracts, the generated provider clients, knowledge's provider sources and
  Krude's integrations toolboxes, in favour of Composio. With them went the
  backlog they carried — provider surveys and research questions, live
  provider tests, a polled delivery kind, managed OAuth apps, and envelope
  encryption kept in two copies — all recoverable from the archive tag.
