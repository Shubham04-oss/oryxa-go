# Remaining work

Everything known to be unfinished across the repository, in one place. Each
entry says what is missing, why it matters and what it needs — a decision,
code, credentials or an operator — and links to the document that holds the
detail. Those documents stay the source of truth for their own services; this
is the index.

Nothing here is a promise about behaviour. Where something was confirmed — by
a command, by reading the code, or against a provider's documentation — it
says so; where it was only noticed in passing, it says that instead.

Brought up to date on 2026-09-18, with the run API.

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

### Knowledge reads three apps, and no files

GitHub, Notion and Slack are back, reading through Composio against a
connection a member made in apps, so knowledge holds no credential of its own
([knowledge/README.md](../knowledge/README.md)). They mount only when
`COMPOSIO_API_KEY` is set, which is also why the project key is in two services
(see above).

The files source is still built and unmounted: it reads as the member who
registered it and needs a grant to do that
([knowledge/TODO.md §1](../knowledge/TODO.md#1-what-still-blocks-every-pass)).
So an organization's own documents — the obvious first thing anyone would
index — are the one thing knowledge cannot read. That waits on §3.2.

Nothing has run a pass against a real app, either: it needs a connected
account, which needs a member at a browser.

### Knowledge feeds rag

Built: rag consumes `knowledge.document.published.v1` and
`knowledge.document.removed.v1` (`rag/internal/events/consumer.go`, handlers
in `rag/internal/application/knowledge_events.go`) and indexes what they
carry through its ordinary document path — `Store.UpsertDocument`, then the
same ingest worker any other document goes through. One corpus per owner,
matching a document's own scope (member, team or organization), created the
first time it is needed at rag's own defaults; a `rag_knowledge_documents`
table maps knowledge's document id to the rag document it became, so a later
publish updates it in place — or moves it, if its reference changed — and a
removal finds it. Detail in
[rag/README.md, Knowledge documents](../rag/README.md#knowledge-documents).

Verified: unit tests for every branch (a new document, an unchanged hash, a
changed hash, a changed reference, an out-of-order event, an omitted body, a
refused scope, two different owners, all three scopes) and integration tests
for the mapping table against a real PostgreSQL, including its cascade when a
corpus or a document goes away some other way. Not run end to end against a
live knowledge pass publishing onto a real queue rag consumes from.

What it does not close:

- **Omitted bodies stay unindexed.** A document over knowledge's 128 KiB
  event budget cannot be indexed at all, because rag has no way to fetch it
  back — knowledge's body endpoint answers only the source's creator or an
  org admin, never a service. The fix is an internal, service-authenticated
  body route in knowledge; not built.
- **Only member-scoped documents flow today.** Knowledge only ever sets a
  connection-based source (`github`, `notion`, `slack`) to member scope, and
  its one organization-scoped source, `files`, is still unmounted (see
  above). So this bridge's team and organization branches are proven by unit
  tests against a synthetic event, not by anything real knowledge code has
  ever emitted, and team scope is not producible by any source at all yet.

### Krude searches corpora, but not a team's by default

Krude's `knowledge` toolbox searches rag as the member and cites what it finds
([agents/primary/README.md, Knowledge toolbox](../agents/primary/README.md#knowledge-toolbox)).
Left out on purpose: it searches the corpora rag lists by default — the
member's own and the organization's — and a team's only when named, since
listing a member's teams means asking teams first. It ran live on 2026-09-18
against rag embedding with Vertex: Krude loaded the toolbox, searched an
organization's corpus as the member and answered from its documents, over the
API and in the web app. That run found rag listing every corpus as empty, so
the toolbox had searched none (PR #33).

### A run is not durable

The `agents` service now serves the run API: a member's threads with Krude,
each run streamed as server-sent events with the member's own token on it,
confirmations answered through it, one run per thread held by a lease across
replicas, and `agents.run.completed.v1` / `agents.run.failed.v1` with a
per-model cost ([agents/primary/README.md, Run API](../agents/primary/README.md#run-api)).
What is left:

- **No confirmation has met a real model.** Gemini answers: on 2026-09-18
  runs streamed `gemini-3.8-flash`'s answers — a thread and its title, the
  answer as it was written, a follow-up that remembered the first question,
  run records and `agents.run.completed.v1` with a per-model cost — and Krude
  searched rag through its knowledge toolbox. No tool that asks for
  confirmation has run for real: the sandbox needs AgentCore credentials, and
  an app's write a connected account. Confirmations approved and declined, and
  isolation between members and organizations, are proven against ADK's real
  runner with a scripted model.
- **A run ends with its request.** A closed tab or a deploy stops it; what it
  had stored stays in the thread. A durable run, one that outlives its
  stream, comes with runs that start from triggers.

### Wake-ups have never run against a real queue

App trigger events now wake Krude: `agents` reads `apps.trigger.received.v1`
from its own queue, batches each member's events, and hands a batch to Krude
in the member's "Updates from your apps" thread, as data to report on, with
the member away ([agents/primary/README.md, Wake-ups](../agents/primary/README.md#wake-ups)).
The same queue brings auth's member-removed and organization-deleted events,
which remove the threads held there. Proven against the development
PostgreSQL and ADK's runner with a scripted model; no queue has been
provisioned, so nothing has been delivered through SQS, and the member-absent
grant wake-ups rely on has never run against Cognito.
Still open, each a product decision: what, if anything, skips the batch wait
(a direct mention); and whether Krude should ever do more on waking than
report, which would need the member to say what, per trigger.

### Guardrails have never screened a real conversation

`agents/primary/internal/guardrail` screens the member's conversation through
Bedrock's `ApplyGuardrail` once `AGENTS_PRIMARY_GUARDRAIL_ID` and its version
are set, turning streaming off for the answers it screens
([agents/primary/README.md, Guardrails](../agents/primary/README.md#guardrails)).
It is proven against a stand-in for Bedrock only: no guardrail has been
created in an AWS account, and which policies it holds — topics, words, PII,
prompt attacks — is a decision for whoever owns the account.

## 2. Web app

The web app now reaches what a member needs day to day: conversations with
Krude, streamed, with confirmations answered in place (PR #29); agent access,
files and app updates in conversations, and the collections Krude searches
(PR #31); and connections on the apps service, with triggers that wake Krude
and knowledge sources that feed its search (PR #32). The screens that called
the archived connections service are gone. `pnpm check`, `typecheck`, `test`
and `build` pass on the three together. What is left:

- **Nothing has run against the real binary.** Every screen was clicked
  through against a stand-in for it; signing in to the real one needs Cognito.
- **Krude's answers are plain text.** Markdown in an answer shows as written.
- **Collections are read-only.** A member cannot make a rag corpus, add a
  document or upload a file for Krude to search from the web app, and the
  files source waits on §3.2.
- **Trigger settings are simple.** A trigger whose configuration needs more
  than plain text, number or yes/no fields shows as needing setup.
- The web app still deploys by hand (§5).

## 3. Decisions only the owner can make

### 3.1 How agent wake-ups batch

Built with defaults, each a setting: per member, a batch closes at 5 events
or when the oldest has waited 2 minutes, at most 20 at once; nothing skips
the wait; and Krude only reports what happened, changing nothing. Revisit
what should skip the wait, and whether a member can give Krude standing
instructions per trigger (§1).

### 3.2 What a member agrees to when knowledge reads files as them

`Grants` would let a background pass act as the member who registered a
files source, through auth's agent grant. That is a consent question before
it is a code question: whether registering a source is itself the member's
agreement, or a separate grant the member gives and can withdraw.

### 3.3 Rag's boundary and defaults

From [rag/README.md, Open decisions](../rag/README.md#open-decisions):

- **Whether reranking defaults on.** It is the largest quality gain per query
  and the largest latency cost, and opt-in today.
- **Whether answering has a default model.** There is none, so a deployment
  that forgets to set one retrieves but cannot answer. A guessed default would
  fail at the first question instead of at startup.

**Where extraction lives** was one of these and is now settled as a default,
not a redesign: rag takes extracted text and refuses anything else, so that
there is one answer about what a document says, and knowledge is the one
extractor feeding it — which is what §1's bridge is built on. It can still be
revisited — see
[rag/README.md, Knowledge documents](../rag/README.md#knowledge-documents).

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
| Rag | Bedrock embedding; Bedrock and Vertex answering; Bedrock reranking, the least exercised code in the service; S3 Vectors; the provisioner, which has never created anything. Vertex embedding ran on 2026-09-18 | An AWS account — S3 Vectors has no local emulation — and `RAG_ANSWER_MODEL` to answer ([rag/README.md](../rag/README.md#what-has-and-has-not-been-run)) |
| Apps | Connecting an account end to end, and therefore every path past it: a tool against a real app, a subscribed trigger, a real delivery, and the outbox reaching the bus. The catalog, tool marks, link creation, the webhook provisioner, webhook verification, MCP and the refusals all ran for real on 2026-09-18 ([apps/README.md](../apps/README.md#what-has-and-has-not-been-run)) | A member at a browser to finish Composio's hosted page, and LocalStack up for the bus |
| Auth | The agent grant against a provisioned Cognito agent client | The agent client provisioned |
| Agents | A tool that asks for confirmation, against a real model; the sandbox and browser; a wake-up. Runs Gemini answers, knowledge search through rag, and the web app's conversations ran on 2026-09-18 | AgentCore credentials or a connected app, and the `agents-events` queue |
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
- **PR #23** gave the `agents` service its run API: a member's threads with
  Krude, each run streamed with the member's own token on it, confirmations
  answered through the API, one run per thread across replicas, and run
  events with a per-model cost. Krude's assembly moved out of `cmd/devui` so
  the service and the development UI run the same agent.
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
