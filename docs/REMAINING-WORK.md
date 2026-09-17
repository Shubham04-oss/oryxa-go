# Remaining work

Everything known to be unfinished across the repository, in one place. Each
entry says what is missing, why it matters and what it needs — a decision,
code, credentials or an operator — and links to the document that holds the
detail. Those documents stay the source of truth for their own services; this
is the index.

Nothing here is a promise about behaviour. Where something was confirmed — by
a command, by reading the code, or against a provider's documentation — it
says so; where it was only noticed in passing, it says that instead.

Brought up to date on 2026-09-17, with PR #18 applied.

1. [The product loop is not closed](#1-the-product-loop-is-not-closed)
2. [Web screens that do not exist](#2-web-screens-that-do-not-exist)
3. [Decisions only the owner can make](#3-decisions-only-the-owner-can-make)
4. [Ready to build](#4-ready-to-build)
5. [Platform](#5-platform)
6. [Never run against the real dependency](#6-never-run-against-the-real-dependency)
7. [Before production](#7-before-production)
8. [Open research](#8-open-research)
9. [Housekeeping](#9-housekeeping)
10. [Recently closed](#10-recently-closed)

## 1. The product loop is not closed

Each service works on its own. The paths between them — the ones that let an
agent answer from an organization's own documents, or react to something that
happened in a connected app — do not exist yet. This is the largest gap.

### Knowledge does not feed rag

Knowledge publishes `knowledge.document.published.v1` and
`knowledge.document.removed.v1` for every document it extracts, and nothing
consumes them. Rag's consumer (`rag/internal/events/consumer.go`) takes only
auth, teams and files lifecycle events, and nothing outside rag calls its
document API. A document read from GitHub, Notion or Slack is therefore never
searchable. Confirmed by reading both consumers and searching for callers.

Rag's document model already has a source kind for extracted text
(`external`: the text and the URI it came from), so what is missing is the
bridge, not a new kind of document. Building it has to settle:

- **Which corpus** a source's documents land in, who creates it, and with
  which embedding model — a corpus pins its model for good.
- **Scope.** Every knowledge document carries its scope, team and member. A
  member-scoped connection's documents must not land in a corpus the whole
  organization can search
  ([knowledge/TODO.md §7](../knowledge/TODO.md#7-for-whoever-is-building-the-retrieval-side)).
- **Omitted bodies.** A body over 128 KiB is not carried in the event, and
  the endpoint that serves it answers only the source's creator or an org
  admin — not a service acting on its own.
- **Removal.** `knowledge.document.removed.v1` has to remove the rag document
  it caused, so the mapping between the two has to be kept.
- **Where extraction lives**, which is a decision (§3.4).

### Agents cannot search a corpus

No toolbox in `agents/primary` searches rag or cites from it, so Krude cannot
answer from an organization's own documents
([rag/README.md, Not yet](../rag/README.md#not-yet)).

### There is no run API

`agents/primary/service.go` mounts a health check and nothing else. Only
`cmd/devui` runs Krude, so every run acts through the member's agent grant,
and `memberauth.WithAccessToken` — forwarding the token of a member who is
present — is called only by tests
([agents/primary/README.md](../agents/primary/README.md#acting-for-a-member)).
The service's write deadline is deliberately zero so that a run can stream;
a run handler has to set its own through `http.ResponseController`.

### Triggers do not start runs

Integrations turns verified deliveries into
`integrations.trigger.received.v1`, and nothing turns one into an agent run as
the member the subscription belongs to. No tool lets an agent create a
subscription either, so an agent can act in a provider but not watch one,
although each provider's skill already describes its events
([integrations/README.md, Later](../integrations/README.md#later)).

The intended shape batches: events collect for a while, across providers,
before one run wakes the agent, rather than one run per event. That has two
consequences for the rest of this document. A trigger that arrives before the
batch would close gains nothing, so a provider that cannot sign its deliveries
can be polled instead (§3.1). And batching does not relax verification: a
forged event inside a batch still reaches the agent. Still open: what closes a
batch — a count, a maximum wait, or both, so that a lone event is not held
indefinitely — whether batches are per member or per subscription, and
whether anything, such as a direct mention, should skip the wait.

### Guardrails are off

`agents/primary/internal/guardrail` screens input and output through Bedrock's
`ApplyGuardrail` and is tested, but nothing wires it in
([agents/primary/README.md, Guardrails](../agents/primary/README.md#guardrails)).
Wiring it turns streaming off for the model calls it screens.

### Knowledge cannot read files

`Grants` in `knowledge/internal/application` has no implementation. It would
mint a member-scoped token so a background pass can read files, which has no
service identity: every files route wants a member. Until it exists a files
source fails with a clear message, and provider sources are unaffected.
`agents/primary/internal/memberauth` is the working precedent. It waits on
the consent question in §3.2
([knowledge/TODO.md §1](../knowledge/TODO.md#1-what-still-blocks-every-pass)).

## 2. Web screens that do not exist

The web app covers auth, onboarding, teams, files, connections, email
preferences and billing. It has nothing for the newer services:

- **Agent conversations.** `/app/threads` renders a `ServicePlaceholder`.
- **Agent grants.** No screen grants or withdraws an agent's access, although
  auth's API does both
  ([auth/README.md, Agent grants](../auth/README.md#agent-grants)).
- **Knowledge sources** — registering one, and how its runs went.
- **Rag corpora and documents**, with their ingest status.
- **Integrations subscriptions.**

## 3. Decisions only the owner can make

### 3.1 Triggers for Google Drive and Confluence: poll them

The first provider survey listed Google Drive, Jira, Notion and Confluence as
unable to have a verified delivery. Checked again on 2026-09-17 against each
provider's documentation, that was too strong
([PROVIDER-READINESS.md](../integrations/internal/providers/PROVIDER-READINESS.md#four-do-not-deliver-the-usual-way)).
Notion signs every delivery, and Jira signs with the app's own client secret,
so Notion is in §4 and Jira's token format in §8. Drive's `watch`
notifications and Confluence really cannot be signed: Drive's carry only a
token we choose, echoed back, and Confluence has no webhook API for OAuth 2.0
apps at all.

Because agents are to be woken in batches rather than per event (§1), those two
do not need pushed deliveries. Polling — Drive's `changes.list` from a saved
page token, Confluence's search by `lastmodified` — goes through the provider's
API with the connection's own credential, so it is verified by construction,
and while the poll interval is shorter than the batch window it costs the agent
nothing in latency. It needs no Pub/Sub topic, no Forge app and no
Oryxa-managed app; it needs a polled delivery kind in integrations (§4).

The signed push routes stay worth having once volume makes a request per
connection per interval expensive: the Workspace Events API for Drive,
generally available since 2026-05-18, which delivers through Pub/Sub with a
signed JWT but wants a topic in the OAuth app's Google Cloud project; and a
Forge app for Confluence and Jira, which Oryxa would build and each site admin
install. What is left to decide is the batch window, which sets how often to
poll.

### 3.2 What a member agrees to when knowledge reads files as them

`Grants` (§1) would let a background pass act as the member who registered a
files source, through auth's agent grant. That is a consent question before
it is a code question: whether registering a source is itself the member's
agreement, or a separate grant the member gives and can withdraw.

### 3.3 HTML extraction

Knowledge reads plain text, Markdown, CSV and JSON. Reading HTML means
promoting `golang.org/x/net` from an indirect dependency to a direct one,
which was left as a choice rather than made unilaterally. It unblocks
Microsoft Graph mail and Notion's richer blocks
([knowledge/TODO.md §8](../knowledge/TODO.md#8-decisions-that-are-not-mine-to-make)).

### 3.4 Rag's boundary and defaults

From [rag/README.md, Open decisions](../rag/README.md#open-decisions):

- **Where extraction lives.** Rag takes extracted text and refuses anything
  else, so that there is one answer about what a document says; knowledge
  extracts. Settling this comes before building the bridge in §1.
- **Whether reranking defaults on.** It is the largest quality gain per query
  and the largest latency cost, and opt-in today.
- **Whether answering has a default model.** There is none, so a deployment
  that forgets to set one retrieves but cannot answer. A guessed default would
  fail at the first question instead of at startup.

### 3.5 Knowledge, once volume is real

From [knowledge/TODO.md §4](../knowledge/TODO.md#4-worth-a-second-look):

- **Where document bodies live.** Postgres today, up to 4 MiB each and with no
  cap per source. Object storage may be the better home.
- **What a failing credential pauses.** Every source on that connection, since
  they share the credential and would fail the same way. The alternative
  pauses only the failing source and lets the others find out for themselves.

## 4. Ready to build

No decision stands in the way of these.

### Integrations

- **ClickUp and Asana.** Both hand back the secret that signs their
  deliveries, which `Registration.Secret` now carries
  ([PROVIDER-READINESS.md](../integrations/internal/providers/PROVIDER-READINESS.md#what-is-ready)).
- **Notion triggers.** Notion signs every delivery with an HMAC over the body;
  only creating a subscription is manual, in Notion's settings, which is the
  app delivery shape Slack and HubSpot already have. The new piece is an app
  delivery that takes its secret from Notion's one-time handshake rather than
  from an admin
  ([PROVIDER-READINESS.md](../integrations/internal/providers/PROVIDER-READINESS.md#notion--signed-but-set-up-by-hand)).
- **A polled delivery kind**, so Google Drive and Confluence can have triggers
  without signed pushes (§3.1). Integrations would keep a cursor per
  connection and call the provider on an interval, leased the way knowledge's
  passes are, with one request covering every subscription on that
  connection. Drive's cursor is a page token, which never expires, kept per
  user and per shared drive since each has its own change log; its entries
  are a file's current state, not a diff. Confluence's is a `lastmodified`
  watermark, precise only to the minute, so windows overlap and changes are
  deduplicated on content id and version. Jira can use it too until its token
  format is confirmed (§8).
- **A sweeper for `pending` subscriptions** left behind by a process that died
  mid-registration. Today only stopping one cleans it up.
- **Per-request headers on provider calls.** Graph's `Prefer` cannot be sent,
  so `microsoft_get_message` strips a message's HTML itself.
- **More of each provider's surface.** Graph subscriptions cover mail, a mail
  folder, calendar events and the drive root only.
- **The other twelve providers** members can connect have no actions or
  triggers; §3.1 and §8 say which can have them.

### Knowledge

From [knowledge/TODO.md §3, §5 and §6](../knowledge/TODO.md#3-finished-but-incomplete):

- **More providers.** Knowledge reads GitHub, Notion, Slack and files. Box,
  Zendesk, Intercom and Figma already have generated clients in
  `internal/providerapi`.
- **Notion page bodies.** A page is read as its title and properties; its
  blocks need a recursive call per page, which is worth designing for cost.
- **Slack thread replies** are not walked.
- **Team files** need their team ids up front, since files cannot enumerate
  teams. Asking teams first would let a source index everything an
  organization holds.
- **`Files.Fetch` re-lists the organization for every page**, which is
  quadratic in the number of files.
- **Notion's incremental passes read full breadth**, because Notion's search
  takes no `since`.
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

### Connections

From [connections/README.md, Later](../connections/README.md#later):

- **Oryxa-managed apps**, so an organization can connect without registering
  its own OAuth client.
- **Slack token rotation**, and Google domain-wide delegation and other
  service-account grants.

### Custom integrations from OpenAPI

The stated direction: an organization supplies an OpenAPI specification,
connections calls it through the same hardened client, and integrations offers
it as MCP tools behind a filter. Nothing exists for it yet;
`internal/providerapi` generates clients for first-party providers only.

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
image. The web app deploys by hand with `pnpm deploy`.

### Services call one another over loopback HTTP, uncached

Connections, files, integrations, knowledge, rag and teams check a caller's
membership by calling auth's `GET /v1/organizations/{id}` over HTTP, with no
cache. In one binary `AUTH_URL` defaults to the process itself, so each check
is a request the process makes to itself. There are fourteen client packages
like this: seven for auth, three for teams, two for files and two for
connections. Calling in process when the target runs alongside, and over HTTP
only when a `*_URL` points elsewhere, would remove the hop and most of the
copies.

### Traces stop at every internal call

`internal/observability` wraps incoming handlers, but the service clients use a
plain `http.Client`, so no trace context leaves the process and each hop starts
a new trace. The root README's "W3C trace propagation" holds for incoming
requests only. An `otelhttp` transport in one shared client constructor would
carry it.

### Envelope encryption exists twice

`connections/internal/secrets` and `integrations/internal/secrets` are the same
code — the problem the outbox had before it moved to `/internal/outbox`.

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
| Integrations | Only `TestLiveGitHubWebhook` has run live. The GitHub action test and the Slack, Linear and Microsoft trigger tests have not, and HubSpot has no live test at all. Linear's `issue(id:)` resolving an identifier such as `ENG-42` is checked only against the fake | Provider accounts and a public URL ([integrations/README.md, Tests](../integrations/README.md#tests)) |
| Knowledge | No live test exists; nothing has read a real GitHub, Notion or Slack | The same, following integrations' live tests |
| Rag | Bedrock and Vertex embedding and answering; Bedrock reranking, the least exercised code in the service; S3 Vectors; the provisioner, which has never created anything | An AWS account — S3 Vectors has no local emulation — and a Vertex project ([rag/README.md](../rag/README.md#what-has-and-has-not-been-run)) |
| Connections | Real KMS, EventBridge and SQS. The e2e stack substitutes AES-GCM for KMS, so the production envelope wiring is unverified end to end | An AWS account |
| Auth | The agent grant against a provisioned Cognito agent client | The agent client provisioned |
| Journey | `test/run.sh` has not run since PR #17 rewrote it for the one binary, and it covers teams, files and email only | Docker |
| Web | `pnpm check`, `typecheck`, `test` and `build` have not run since PR #17 pointed the development proxy at the one binary | pnpm |

## 7. Before production

Each service's README has a Provision section for the resources it owns.
These are the steps that are easy to miss, because they live outside the
service that fails without them.

- **Knowledge must be allowed to call connections.** Create the KMS signing
  key, then add
  `{"service": "knowledge", "key_id": "alias/oryxa-knowledge-identity", "purposes": ["action"]}`
  to `CONNECTIONS_SERVICE_CALLERS`, which is read at startup. Without it every
  pass fails at its first token request
  ([knowledge/TODO.md §1](../knowledge/TODO.md#1-what-still-blocks-every-pass)).
- **Billing's live mode.** Create the products in Dodo live mode and point the
  catalog's `product_id`s at them — `plans.example.json` holds placeholders —
  set `DODO_PAYMENTS_ENVIRONMENT=live_mode` with a live API key and webhook
  secret, and decide on Dodo's subscription grace period
  ([billing/README.md, Deploy](../billing/README.md#deploy)).
- **Database extensions.** Install `vector`, `ltree`, `pg_trgm`, `btree_gist`
  and `btree_gin` in the managed database, or let the migrating role create
  them, and set `POSTGRES_REQUIRED_EXTENSIONS` so a missing one fails startup
  rather than a query (§5).

## 8. Open research

From `docs/research/`, which flags about twenty items `[UNCONFIRMED]`. These
are the ones that shape code:

- **HubSpot's subscription auth contradicts itself.** The OpenAPI `security`
  block declares OAuth scopes `developers-write` and `private-apps-write`; the
  prose guide says a developer API key via `?hapikey`.
- **A HubSpot private app cannot manage webhooks through the API.** Whether an
  organization can register a subscription depends on its app type: a legacy
  public app can, a private app cannot, and a project-based app uses a
  configuration file.
- **HubSpot's deals path differs inside its own documentation** —
  `/crm/objects/2026-09/0-3` in the OpenAPI, `/crm/objects/2026-09/deals` in
  the guide — which is why there is no deals action.
- **Google Drive's maximum `watch` lifetime is not published.** The ~24 hours
  is community-reported.
- **Confluence's 3LO paths conflict**: the specification's `/wiki/rest/api/...`
  against the 3LO guide's `/rest/api/space`.
- **Atlassian's dynamic webhooks expire after 30 days** and need a scheduled
  refresh, and OAuth 2.0 allows five per app, user and site.
- **Jira's webhook token format.** A dynamic webhook delivery carries a token
  signed with the app's client secret, and Atlassian documents neither its
  algorithm, its claims, its lifetime nor whether it binds the body. One
  delivery captured from a Jira Cloud site settles all four. Secured admin
  webhooks, signed in `X-Hub-Signature`, are the documented fallback
  ([PROVIDER-READINESS.md](../integrations/internal/providers/PROVIDER-READINESS.md#atlassian-jira--signed-with-a-key-we-hold-in-a-format-nobody-documents)).
- **Seven providers are unsurveyed.** Airtable, Box, Dropbox, Figma, Intercom,
  Salesforce and Zendesk have connections, and nobody has checked whether they
  can have a verified delivery or what their actions would be. The survey pays
  for itself: it is what found that Atlassian's `/rest/api/3/search` is removed
  rather than deprecated, and that HubSpot's current paths are `2026-09` rather
  than `v3` — both would have been live bugs.

## 9. Housekeeping

- **Activepieces left a purpose behind in files.** `integrations.piece` is
  still an accepted purpose, in the check constraint of files' migrations, and
  `files/README.md` still describes the piece runner it was for. Removing it
  needs a migration that narrows the check.
- **Merged branches remain on the remote**: `claude/happy-hypatia-ypwooc`,
  `feat/agent-identity-and-tools`, `feat/agents-primary`,
  `feat/connections-providers`, `feat/connections-work-tracking`, `feat/rag`,
  `feat/single-binary`, `fix/invited-member-onboarding` and
  `spike/openapi-structs`.
- **Building under a sandbox** that cannot write `$HOME` needs `GOPATH` and
  `GOCACHE` pointed inside the workspace; both are gitignored.

## 10. Recently closed

Kept short; the pull requests hold the detail.

- **PR #13** closed six connections gaps — a hold racing credential deletion,
  outbox data validation, the `SharesProviderGrant` index, leases left on
  disconnect, anonymous outbox claims and outbox rows kept forever — and built
  HubSpot.
- **PR #17** runs every service from one static binary, `cmd/oryxa`.
- **PR #18** gave nine services one outbox, `/internal/outbox`, with owned
  claims and a week's retention; recorded applied migrations in
  `oryxa_migrations`, so each file runs once; added knowledge's periodic full
  sweep; paged rag's document lists and tested its access rules, events and
  clients; closed connections' eight recorded test gaps; and fixed auth's
  invitation links and agent-grant `return_to`. It also fixed three bugs found
  while proving those: HubSpot signatures checked over the path rather than
  the URL HubSpot signs, so every real delivery would have been refused; one
  HubSpot account's events reaching other accounts' subscriptions; and
  knowledge retracting what earlier runs of a multi-run full pass had read.
