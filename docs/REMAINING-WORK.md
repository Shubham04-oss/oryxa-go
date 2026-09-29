# Remaining work

Everything known to be unfinished across the repository, in one place. Each
entry says what is missing, why it matters and what it needs — a decision,
code, credentials or an operator — and links to the document that holds the
detail. Those documents stay the source of truth for their own services; this
is the index.

Nothing here is a promise about behaviour. Where something was confirmed — by
a command, by reading the code, or against a provider's documentation — it
says so; where it was only noticed in passing, it says that instead.

Brought up to date on 2026-09-28, after that day's release, against `main` at
`4380f5c`: four reviews — this backlog against the code, web-next and the Go
services each searched for bugs, and the deployment read for launch — and a
reading of production itself: its queues, alarms, logs and traces, and one
read-only look at the shape of a thread Krude failed on. The bugs it confirmed
are in [Open bugs](#open-bugs); what it closed moved to
[Done since the last update](#10-done-since-the-last-update), with its
evidence, rather than being deleted. Anything this pass could not confirm one
way or the other is unchanged from the 2026-09-26 pass (`7f03a5a`).

**Direction.** Third-party apps reach first users through Composio rather than
Oryxa's own connection layer. The `apps` service is that: built, tested, and
run against a real Composio project since 2026-09-18, including a real
GitHub account end to end (§10). The `connections` and `integrations` services,
their event contracts, the generated provider clients in
`internal/providerapi`, knowledge's GitHub, Notion and Slack sources, and
Krude's integrations toolboxes were archived on 2026-09-17; everything is at
tag `archive/connections-and-integrations-2026-09-17`, and
`git checkout archive/connections-and-integrations-2026-09-17 -- <path>`
restores any of it. The provider research in `docs/research/` stays as
reference.

**Next, from 2026-09-28.** Where the day's plans ended up:

- **The product's target is [north-star.md](north-star.md): Switchboard.** It
  is an operator that brokers answers for others with the owner's one-click
  consent, and does work for its own owner. From the evening of 2026-09-28
  the focus is agents and teams, in that document's build order. Connectors
  and knowledge bases are on hold.
- **Integrations: our own sign-ins, not Nango.** Nango, Activepieces pieces
  and AgentCore Identity were each researched and ruled out
  ([integrations-research.md](design/integrations-research.md)). The archived
  `connections` service is only a reference.
- **The integrations plan** is in
  [integrations-plan.md](design/integrations-plan.md). It has three phases:
  1. One sign-in and token system in `apps`, covering OAuth2, API tokens and
     basic credentials, with fields each integration declares. One webhook
     ingress, where each integration declares how its app signs requests.
     Phase 1 is being built, and ships switched off.
  2. Integrations on official SDKs, built by cheap-model workers.
  3. AgentCore Gateway in front of apps' MCP server.
- **Composio stays the tool provider** meanwhile.
- **Knowledge bases are postponed.** That includes the Cedar permission layer
  (PR #109 and branches `feat/rag-authz` and `feat/kb-policies-web`), whose
  review found policy bypasses to fix before any merge.
- **The owner builds agents and teams in parallel:** screen context and
  naming, in [agents-and-teams.md](design/agents-and-teams.md).

First, [Open bugs](#open-bugs), then:

1. [The product loop is not closed](#1-the-product-loop-is-not-closed)
2. [Web app](#2-web-app)
3. [Decisions only the owner can make](#3-decisions-only-the-owner-can-make)
4. [Ready to build](#4-ready-to-build)
5. [Platform](#5-platform)
6. [Never run against the real dependency](#6-never-run-against-the-real-dependency)
7. [Before production](#7-before-production)
8. [Housekeeping](#8-housekeeping)
9. [Recently closed](#9-recently-closed)
10. [Done since the last update](#10-done-since-the-last-update)

## Open bugs

Confirmed on 2026-09-28, each by following its code path end to end or by
reproducing it. None is a security hole. Worst first.

### Some of Krude's runs fail at Vertex with a 400

In production on 2026-09-28, between 03:44 and 03:45 UTC, four runs on one
member's thread ended `run failed` with Vertex's `400 INVALID_ARGUMENT`,
"Request contains an invalid argument", on Krude's first model call. Short
messages on the same thread worked before and between them, and a run on
another thread in the same organization worked at the same moment. Ruled out,
each against Vertex directly with `gemini-3.5-flash-lite`: every tool
declaration Krude's toolboxes make, alone and all 39 together; an explicit
cache on its own, holding those declarations; tool calls in the history,
declared or not; and anything in the messages, which held no link, mention,
address or unusual character. A tool call missing its thought signature fails
too, but with a message of its own. The leading suspect is the explicit
prompt cache (`agents/primary/internal/modelcache`) together with what a
longer message brings into the prefix: memories from Memory Bank can make it
large enough to be cached, where a greeting's is not. That combination has
not been reproduced. Next: when a model call answers `400`, log the request's
shape — roles, part kinds, whether it was cached and the prefix's estimated
size, never its text — and see whether `AGENTS_PRIMARY_PROMPT_CACHE=false`
stops it.

### web-next

- **A refresh that never answers wedges every request.** `lib/api/client.ts`
  shares one refresh promise, cleared only once the fetch settles, and the
  fetch has no timeout, so every 401 in the app waits on it. Give the refresh
  an `AbortSignal.timeout`.
- **Cancel does not cancel deleting or leaving an organization.** In
  `workspace-organization-settings.tsx` the dialog's Cancel stays enabled while
  the request runs and only closes the dialog: the request completes and the
  page navigates away. Disable it while pending, or abort the request.
- **A room's agent messages load images from anywhere.** `room-markdown.tsx`
  renders an image, where `agent-timeline.tsx` makes it a link so that an
  answer cannot fetch from a server on its own; with no CSP, a crafted reply
  would tell another server every viewer's address. Make it a link there too.
- **An email with a stray space is refused on every auth form.**
  `lib/auth/schemas.ts` checks the format before it trims;
  `z.string().trim().pipe(z.email(...))` checks the trimmed value.
- **Cancelling a new knowledge base, or an upload, still creates it.**
  `knowledge-bases.tsx` and `knowledge-base-detail.tsx` reset the form but let
  the request finish, and its success opens or inserts what was cancelled; a
  retry uploads it again.

### Services

- **Rag skips its Origin check when the header is missing.**
  `rag/internal/httpserver/server.go` checks `Origin` on a cookie-authenticated
  write only when one is sent; apps and knowledge refuse a missing one.
- **Artifacts have no size limit.** `agents/primary/internal/s3artifact` stores
  what it is given and reads it back with an unbounded `io.ReadAll`, in the
  process every service shares.
- **Smaller, each dormant today.** `internal/eventauth`'s KMS calls take
  `context.Background()` and no timeout, which matters once services have KMS
  signing keys (§5); teams' limit of 20 fan-out calls a minute is kept per
  process, so it loosens with every replica; the ADK session write and its
  `agents.session.created/deleted.v1` outbox row are separate transactions,
  and nothing consumes those events yet; and S3 Vectors' search does not
  filter by document access, relying on rag checking every hit again in
  PostgreSQL, as it does today.

### Edge

- **`app.oryxa.in` sends no security headers**: no HSTS, CSP, frame
  protection, `X-Content-Type-Options` or referrer policy.
- **`/v1/internal/*` answers from the internet**, on both hosts. Its routes
  refuse a call without a service token (`401`), but nothing needs them to be
  public, and the tunnel forwards every path.

Not bugs, written down so that nobody chases them again: Composio calls
`/v1/apps/connect/callback` twice for each connection, and the second finds
the state spent and answers `404` while the member's browser follows the
first's redirect back into the app; and on 2026-09-21 a prefix of 3,974 tokens
was below Vertex's 4,096-token minimum for a cache, so those requests went
uncached, as designed.

## 1. The product loop is not closed

Each service works on its own. The paths between them — the ones that let an
agent act in an organization's apps, answer from its own documents, or react
to something that happened — do not exist yet. This is the largest gap.

### Apps has run against Composio, for GitHub only

The `apps` service is built: connecting an app through Composio's hosted flow,
tools behind Oryxa's member identity and write approvals, verified webhooks,
four MCP tools for agents, and Krude's `apps` toolbox over them
([apps/README.md](../apps/README.md)). Ordinary tests run against fakes; a
live pass and a real connected account, both below, cover the rest.

A first live pass ran on 2026-09-18 against a throwaway Composio project
through a cloudflared tunnel: the catalog, tool search and its read-only marks,
auth-config and connect-link creation, the webhook subscription provisioner,
webhook verification in all four of its cases, the MCP endpoint over plain
HTTP, and every refusal
([apps/README.md](../apps/README.md#what-has-and-has-not-been-run)). It found
two defects, both fixed: a sentinel's own words leaking into the wording a
model reads, and the read route answering `not_connected` where the honest
answer was `not_read_only`.

**Connecting an account end to end** ran on 2026-09-19, for a real GitHub
account: the hosted flow, a tool against the account, a subscribed trigger,
and a webhook delivery that reached apps and was recorded once — see
[Done since the last update](#10-done-since-the-last-update). Still unrun the
same way: **Slack, Notion, Google Drive and Gmail accounts**. Treat those as
proven against a stand-in only.

Then, in order:

- **An OAuth app of Oryxa's own**, for the apps Composio holds no app for.
  Those are refused today as not connectable, so the catalog is smaller than
  Composio's. Composio's own Google app is used first anyway, since an
  unverified Google app of Oryxa's is capped at 100 new users and, in testing,
  loses refresh tokens after seven days; moving later means members reconnect
  once.
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

The files source is mounted again. It reads as the member who registered it,
and a scheduled pass has no member at a browser, so it redeems the standing
agent grant they gave auth: `/internal/membertoken` exchanges one for a
short-lived token at auth's `/v1/internal/agent-tokens`, the route agents has
always used. It mounts once `FILES_URL` and `KNOWLEDGE_SERVICE_KEY_ID` are
set and auth lists knowledge in `AUTH_SERVICE_CALLERS` with the `agent`
purpose; short of any of those it says which is missing and stays off. So an
organization's own documents can finally be indexed — against a stand-in
auth, not yet against Cognito.

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

Team scope, once produced by nothing, now is — see
[Done since the last update](#10-done-since-the-last-update).

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
  stream, comes with runs that start from triggers. Since 2026-09-28 every
  merge to main deploys, so a run a deploy cuts short is routine, not rare.

### Wake-ups arrive through SQS; a run for an absent member is unproven

App trigger events now wake Krude: `agents` reads `apps.trigger.received.v1`
from its own queue, batches each member's events, and hands a batch to Krude
in the member's "Updates from your apps" thread, as data to report on, with
the member away ([agents/primary/README.md, Wake-ups](../agents/primary/README.md#wake-ups)).
The same queue brings auth's member-removed and organization-deleted events,
which remove the threads held there. The queue has existed since the first
production apply on 2026-09-19 (`deploy/production/main.tf`,
`deploy/scripts/provision-app.sh`), and events reach it: agents logged
`trigger batch recorded to wake Krude` 66 times on 2026-09-26 and 27. Not seen
yet is a batch waking Krude for a member who is away, which needs their
standing grant exchanged for a Cognito agent token (§6, Auth).
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

This is not the answer guard. `internal/answerguard`, which reads what one
member's agent answers another before they see it, runs in production since
2026-09-28 with a guardrail of its own (`deploy/production/answer_guard.tf`);
the conversation guardrail has no guardrail, resource or setting in `deploy/`
at all.

## 2. Web app

Since 2026-09-28 `app.oryxa.in` is web-next, released with every merge to
main; the old `web/` app's Worker stays deployed, untouched, for a rollback
([deploy/README.md, Releasing](../deploy/README.md#releasing)).
[web-next/PARITY.md](../web-next/PARITY.md) maps it against the old app. What
is left:

- **Missing surfaces.** Files; editing and running agent workflows, which are
  read-only; the agent-requests inbox, a count only, though side
  conversations carry requests between teammates; the ⌘K palette; and a
  history list and delete control for the primary agent's chat.
- **Home's overview** is an empty canvas whose saved-view labels open nothing.
  Its design is the owner's call.
- Its bugs are in [Open bugs](#open-bugs).

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

HTML extraction is decided and built — see
[Done since the last update](#10-done-since-the-last-update). Still open:

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
- **An end-to-end test** running the HTTP API, the application, the store and
  the adapters together. No active service has one; the archived connections
  and integrations services did.

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

### CI exists now

Done — see [Done since the last update](#10-done-since-the-last-update).

### Production deployment exists now

Done, and released from CI since 2026-09-28: every push to main that passes
CI builds the image, rolls the service onto it, records the release on main
and deploys web-next's Worker
([deploy/README.md, Releasing](../deploy/README.md#releasing)).
Infrastructure changes are still a `tofu apply` from a laptop.

### Services call one another over loopback HTTP, uncached

Files, knowledge, rag and teams check a caller's membership by calling auth's
`GET /v1/organizations/{id}` over HTTP, with no cache. In one binary
`AUTH_URL` defaults to the process itself, so each check is a request the
process makes to itself. There are at least eleven client packages like this:
seven for auth (billing, teams, files, apps, rag, knowledge, agents/primary),
two for teams and two for files. Calling in process when the target runs
alongside, and over HTTP only when a `*_URL` points elsewhere, would remove
the hop and most of the copies.

### Traces now carry across internal calls

Done — see [Done since the last update](#10-done-since-the-last-update).

### Rag relies on teams to install `vector`

Only teams' migrations run `CREATE EXTENSION`. Migrating rag on its own
(`ORYXA_SERVICES=rag`) against a database without `vector` fails. The root
README's position is that the database image or control plane owns
extensions; until one does, the order in `cmd/oryxa/services.go` is
load-bearing, and `TestTeamsMigratesBeforeRag` pins it.

### Event signatures are checked, not enforced

Every event is signed by the service that publishes it and checked before a
consumer acts on it (`internal/eventauth`), but nothing is refused yet:
`ORYXA_EVENT_SIGNATURES` defaults to `report`, which logs a signature that does
not check out ("event signature did not check out") and handles the event
anyway. No service has an event signing key, so each signs with one minted at
boot, which dies with the process — enforcing now would refuse whatever was
still queued across every restart and every rolling deploy. To enforce: give
each of the eleven services — `realtime`, added since this count was last
checked, included — a KMS key (`ECC_NIST_P256`, `SIGN_VERIFY`) in
`deploy/production`, set `<SERVICE>_EVENT_SIGNING_KEY_ID` for each and grant
the task `kms:Sign` and `kms:GetPublicKey` on them; release; and once a
release's logs show no failures, set `ORYXA_EVENT_SIGNATURES=enforce`. What
enforcing buys today is small: the queues admit only their own EventBridge
rules, and the one identity besides an administrator that can publish on the
bus — the task — would hold the keys too. It guards against a queue policy
opened too wide, and matters once services run apart.

## 6. Never run against the real dependency

Fakes and stub servers prove the request this code builds and the response it
reads. They prove nothing about whether the provider agrees.

| Area | What has never run | What it needs |
| --- | --- | --- |
| Rag | Bedrock reranking beyond construction — the least exercised code in the service; Cohere embedding; the S3 Vectors provisioner, which has never created anything (a temporary index was hand-created for two eval runs instead, then deleted). Vertex embedding and answering, and Bedrock answering (`Converse`), have all since run for real too — see [Done since the last update](#10-done-since-the-last-update) | An AWS account — S3 Vectors has no local emulation — and `RAG_ANSWER_MODEL` to answer ([rag/README.md](../rag/README.md#what-has-and-has-not-been-run)) |
| Apps | The outbox reaching the bus: EventBridge was still pointed at a dead local endpoint in the 2026-09-19 run below, so an event was written and held, never published for real. Slack, Notion, Google Drive and Gmail accounts, in every respect. Connecting a GitHub account end to end ran for real on 2026-09-19; the catalog, tool marks, link creation, the webhook provisioner, webhook verification, MCP and the refusals all ran for real on 2026-09-18 ([apps/README.md](../apps/README.md#what-has-and-has-not-been-run); see also [Done since the last update](#10-done-since-the-last-update)) | LocalStack (or a real bus) up to prove the outbox for real, and a member at a browser for Slack, Notion, Drive and Gmail |
| Auth | Whether a member has finished the agent grant against the agent client | A member to run the flow — the client itself is now provisioned, see [Done since the last update](#10-done-since-the-last-update) |
| Agents | A tool that asks for confirmation, against a real model; the sandbox and browser; a wake-up run for a member who is away (trigger batches do arrive, §1). Runs Gemini answers, knowledge search through rag, and the web app's conversations ran on 2026-09-18 | AgentCore credentials or a connected app, and a member's standing grant |
| Journey | `test/run.sh` has not run since PR #17 rewrote it for the one binary, and it covers teams, files and email only | Docker |
| Realtime | A purge against the real Worker. On 2026-09-24 the binary ran against LocalStack and a stand-in Worker that checks the signature from the contract alone: it purged, skipped a deleted organization's members, dropped a `400`, and retried a `503` into the dead-letter queue. That run used the body before `scope` and `removed_at`, which only unit tests have sent. On 2026-09-25 the binary served the Worker's `POST /v1/realtime/events` against PostgreSQL and LocalStack's bus, with the golden vectors signed by hand: it took both, acknowledged one sent again without a second event, refused a purge's signature and an invalid event, and published both events. Realtime has been on in production since 2026-09-28, with web-next serving `app.oryxa.in` and both sides holding the secret, but no purge and no Worker event had been observed there yet, and nothing consumes the events ([realtime/README.md](../realtime/README.md)) | A member leaving an organization or a teamspace, and a direct message, in production |

## 7. Before production

Each service's README has a Provision section for the resources it owns.
These are the steps that are easy to miss, because they live outside the
service that fails without them.

- **Billing's live mode.** Create the products in Dodo live mode and point the
  catalog's `product_id`s at them — `plans.example.json` holds placeholders —
  set `DODO_PAYMENTS_ENVIRONMENT=live_mode` with a live API key and webhook
  secret, and decide on Dodo's subscription grace period
  ([billing/README.md, Deploy](../billing/README.md#deploy)).
- **Database extensions.** Not directly confirmed, only inferred: production
  has run since 2026-09-19 (§5), which needs `vector`, `ltree`, `pg_trgm`,
  `btree_gist` and `btree_gin` already present. Still worth setting
  `POSTGRES_REQUIRED_EXTENSIONS` explicitly so a missing one fails startup
  rather than a query.
- **Composio.** Done — a project, a scoped API key and a webhook subscription
  already exist and have carried real traffic; see
  [Done since the last update](#10-done-since-the-last-update). Still open:
  IP allowlisting, and — for security-conscious customers — a decision on the
  self-hosted or customer-managed-key tier (§1).

Found by the 2026-09-28 launch review, each checked against the files or the
account:

- **An alarm on errors.** Written and not applied. Nothing alarmed on the
  application's own errors: a TypeSafe key that no longer worked failed every
  Jev call for four days, and only the logs said so. `hardening.tf` now
  counts `level = "ERROR"` in `/oryxa-production/app` and alarms on 3 in 15
  minutes.
- **CI runs every database suite.** `ci.yml` names a test database for eight
  services; knowledge, files, realtime and the outbox skip their integration
  tests in CI, while every merge now deploys.
- **Files that can be recovered.** Written and not applied: the files,
  artifacts and knowledge-base documents buckets are versioned, and a
  lifecycle rule removes older versions after 30 days, so a deleted object
  stays recoverable for 30 days (`hardening.tf`, `knowledge_base.tf`). Still
  open: the files and artifacts provisioners
  (`files/infra/storage/internal/provision`,
  `agents/primary/infra/artifacts`) put their own lifecycle rule alone back on
  every run of `provision-app.sh`, which drops that one until the next
  `tofu apply`. They need to write it too.
- **The auth key kept safe.** `aws_kms_key.auth` envelope-encrypts every agent
  grant with the minimum deletion window of 7 days and no `prevent_destroy`;
  the services' signing keys likewise.
- **No single point of failure.** One Fargate task and a single-AZ
  `db.t4g.small` (backed up for 7 days, deletion-protected) carry everything;
  deploy/README.md's "At launch" names a second task and Aurora or Multi-AZ.
- **A schema change someone reviews.** Migrations run at start, and a merge now
  releases with no approval. The ECS circuit breaker catches a task that never
  turns healthy, not a migration that runs and does damage, and which builds
  are safe to roll back to is kept in prose, release by release.
- **Alarms on what is only graphed.** SES bounces and complaints, EventBridge
  failed invocations and Bedrock throttling are now alarms, written and not
  applied (`hardening.tf`). Still open: every alarm mails one address.
- **Smaller.** Nothing limits the rate of sign-in attempts at Cloudflare. Two
  more are written and not applied: the RDS log groups, which never expired,
  now keep 30 days (`hardening.tf` declares them and imports the ones RDS
  made), and the task role's Cognito statement, which allowed `cognito-idp:*`
  on every pool, now names the eight admin calls auth makes, on the one pool
  (`compute.tf`).

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

## 10. Done since the last update

Kept here with its evidence rather than simply deleted, so the backlog does
not quietly lose its own history.

### 2026-09-28

- **Main releases from CI.** Every push to main that passes CI is released by
  `deploy.yml`, into a `production` environment only main may deploy to, with
  no required reviewer. Its first run found two things, both fixed in #105:
  the repository's OIDC tokens carry GitHub's immutable subject
  (`repo:Shubham04-oss@218535561/oryxa-go@1368173110:…`), which the deploy
  role now trusts, and `aws ecs wait services-stable` returned while a
  rollout still read `IN_PROGRESS`, which the workflow now waits out. It has
  since released `e8ecab7a289d` and `c18e1136a921` on its own, recording each
  on main and deploying the Worker.
- **Production is current**, through #106. The release that carried #85 and
  owner-approved requests stopped the old task first, as they need, which cost
  the API about 80 seconds; the runbook's command for that needed
  Availability Zone rebalancing off as well (#105).
- **web-next serves `app.oryxa.in`** (#103). **Realtime is on**, and **the
  answer guard is on**, both applied on 2026-09-28.
- **Billing sent members to a 404.** `BILLING_URL` was the API host, so every
  return from Dodo and every billing email link landed on nothing. It is
  `app.oryxa.in/settings/billing`, which both frontends serve (#103).
- **Jev had failed since about 2026-09-24.** Production's `TYPESAFE_API_KEY`
  was refused with a 401, so batching fell back to its plain digest,
  knowledge's sources waited as `quality_check_unavailable`, and the answer
  guard's choice of tools had no Jev. The valid key in `.env` was stored on
  2026-09-28.
- **Images carry their release** as `service_version` (#105), and Krude's
  shutdown no longer logs a 404 for each prompt cache Vertex had already let
  expire (#105).
- **web-next's settings and Tools:** role pickers commit only on an explicit
  choice, team settings follow a self-demotion, the invitation page offers
  another account (#104), and Tools asks Composio's catalog only from three
  characters, the shortest search Composio takes (#106).
- **Housekeeping.** 49 worktrees, about 22 GB, and 159 merged branches, 85
  local and 74 on GitHub, were removed. `deploy/production-release` is kept;
  main's deploy configuration supersedes it.

### 2026-09-26

Checked against `main` at `7f03a5a`. Each of these was still described above
as open, unrun, or missing as recently as 2026-09-18.

- **Composio, end to end for a real GitHub account.** Connecting it, a tool
  against it, a subscribed trigger, and a webhook delivery (recorded once
  despite Composio sending it three times) all ran on 2026-09-19
  ([apps/README.md](../apps/README.md#what-has-and-has-not-been-run)). Slack,
  Notion, Google Drive and Gmail accounts are unchanged (§1): proven only
  against stand-ins.
- **Team-scoped knowledge documents.** A team-scoped `files` upload now
  registers a team-scoped source (`knowledge/internal/application/connected.go`,
  `filePlacement`); commit `d203701`, 2026-09-19. See
  [knowledge/README.md](../knowledge/README.md), "Shared, team and private
  files index themselves" (§1 used to say no source produced one).
- **HTML extraction.** Decided and built: `knowledge/internal/transform/html.go`,
  and `golang.org/x/net` is now a direct dependency (`go.mod`); commit
  `6c19ded`, 2026-09-18. See
  [knowledge/TODO.md §8.1 and §9](../knowledge/TODO.md#8-decisions-that-are-not-mine-to-make)
  (§3.4 used to list this as an open choice).
- **Krude's answers render as Markdown** in the web app, not plain text:
  `web/src/components/threads/message-markdown.tsx` (react-markdown +
  remark-gfm). PR #34, merged 2026-09-18 (§2 used to say otherwise).
- **The web app has run against the real binary.** Production has served
  real members since 2026-09-19 (below) (§2 used to say every screen was only
  ever clicked against a stand-in).
- **Rag's Vertex embedding and answering, and Bedrock answering (`Converse`),
  have all run for real** — Vertex embedding on 2026-09-18 and 2026-09-23,
  Vertex answering and Bedrock answering (Nova Micro) both on 2026-09-23.
  Bedrock embedding is partly done: Titan v2 ran live on 2026-09-23; Cohere is
  still unrun. See
  [rag/README.md, What has and has not been run](../rag/README.md#what-has-and-has-not-been-run)
  (§6 used to list all four as never run).
- **CI exists.** `.github/workflows/ci.yml` builds and vets every package,
  runs the race tests, runs the database suites against a real PostgreSQL,
  and runs the web checks. Added by PR #82, merged 2026-09-23. It does not run
  `test/run.sh` (§6, Journey, is unchanged).
- **Production deployment exists and is live.** `deploy/production`
  (OpenTofu) and `deploy/scripts/*` (`release.sh`, `provision-app.sh`, and the
  rest) provision the AWS resources and ship the image; `app.oryxa.in` and
  `api.oryxa.in` are live behind Cloudflare today
  ([deploy/README.md](../deploy/README.md)).
  `deploy/production/release.auto.tfvars.json` records the image actually
  running. `deploy.yml` (also PR #82) automates a release on paper but is
  inert: the GitHub `production` environment is still missing the
  `CLOUDFLARE_API_TOKEN` secret and the `HYPERDRIVE_ID` variable
  ([deploy/README.md, "Releasing"](../deploy/README.md#releasing)), so
  `deploy/scripts/release.sh` by hand is still what ships the binary, and the
  web app (`web/`) still deploys by hand too, with `pnpm deploy`. (Superseded
  on 2026-09-28, above: main now releases from CI.)
- **Traces carry across every internal call.**
  `internal/observability.Transport()` wraps `otelhttp.NewTransport` and is
  already used by every internal client package's `http.Client` (§5 used to
  say a plain `http.Client` dropped the trace at each hop).
- **The Cognito agent client is provisioned.**
  `deploy/production/app.auto.tfvars.json` has held `agent_client_id` since
  commit `6f4395e`, 2026-09-19. Whether a member has finished the grant flow
  against it is still unverified (§6, Auth).
- **Both frontends have a screen for the agent grant.** `web/`'s Settings →
  Profile ("Background agent access") and web-next's Settings → Account both
  call it — see `auth/README.md`, Agent grants.
- **Composio itself is provisioned**: a real project, a scoped API key and a
  webhook subscription, exercised live since 2026-09-18/19 (§1, §7; see
  [apps/README.md](../apps/README.md#what-has-and-has-not-been-run)).
