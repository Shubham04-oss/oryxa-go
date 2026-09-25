# Oryxa

Oryxa is an AWS-backed Go monorepo: one Go module, whose services build into one
static binary, `oryxa`. Each service still owns its API, its data and its AWS
resources; they deploy together.

## Run

Every service builds into one static binary, `oryxa`, from `cmd/oryxa`. It
serves every service's routes from one listener, runs their background work,
and installs their schemas before it serves.

```sh
docker compose up -d                  # Postgres, LocalStack, Moto SES, Mailpit
cp .env.example .env                  # then fill in what the services you run need
(set -a && . ./.env && go run ./cmd/oryxa)
curl -i http://localhost:8080/healthz
```

`ORYXA_SERVICES` picks which services a process runs — all of them by
default — so one image can still be deployed as several differently scaled
processes:

```sh
(set -a && . ./.env && ORYXA_SERVICES=auth,teams,files go run ./cmd/oryxa)
go run ./cmd/oryxa services           # what this binary carries
go run ./cmd/oryxa migrate            # install schemas, then exit
```

| Variable | Default | What it does |
| --- | --- | --- |
| `ORYXA_ADDRESS` | `:8080` | Where the one listener binds |
| `ORYXA_SERVICES` | all | Comma-separated services to run |
| `ORYXA_AUTO_MIGRATE` | `true` | Install schemas at start, before serving |
| `ORYXA_DATABASE_MAX_CONNS` | `32` | Size of the shared pool, unless `DATABASE_URL` sets `pool_max_conns` |
| `ORYXA_SHUTDOWN_TIMEOUT` | `30s` | Bound on draining requests, then on waiting for workers |
| `DATABASE_URL` | — | The PostgreSQL every service shares |
| `AWS_REGION`, `EVENT_BUS_NAME` | `ap-south-1`, `default` | Shared by every service |
| `DEPLOYMENT_ENVIRONMENT` (or `ENVIRONMENT`) | `development` | Named in logs and traces; `production` makes event queues required |
| `ORYXA_REQUIRE_EVENT_QUEUES` | on in production | Refuse to start while a selected service's events queue is unset |

Eleven services consume events, each from its own queue: `AGENTS_EVENTS_QUEUE_URL`,
`APPS_EVENTS_QUEUE_URL`, `AUTH_EVENTS_QUEUE_URL`, `BATCHING_EVENTS_QUEUE_URL`,
`BILLING_EVENTS_QUEUE_URL`, `EMAIL_QUEUE_URL`, `FILES_EVENTS_QUEUE_URL`,
`KNOWLEDGE_EVENTS_QUEUE_URL`, `RAG_EVENTS_QUEUE_URL`,
`REALTIME_EVENTS_QUEUE_URL` and `TEAMS_EVENTS_QUEUE_URL`, each made by its
service's `infra/*/cmd/provision`. Without its queue a service only warns — and then
never hears the events that remove what people deleted — so in production the
process refuses to start instead, naming every queue that is missing. Realtime
is the exception while it is off (neither `REALTIME_WORKER_URL` nor
`REALTIME_PURGE_SECRET` set): it hears nothing then, so its queue is not
required.

Services still reach one another over HTTP, exactly as when each ran alone.
A URL variable naming a service the process runs — `AUTH_URL`, `TEAMS_URL`,
`FILES_URL` — defaults to the process itself; set, it wins, so a service can
live elsewhere.

The image is built from the repository root:

```sh
docker build -t oryxa:dev .
docker compose -f compose.oryxa.yaml up -d --build
```

## Service layout

Each service owns a lowercase, hyphen-free directory at the repository root. The services so far are `/agents`, `/apps`, `/auth`, `/batching`, `/billing`, `/email`, `/files`, `/knowledge`, `/rag`, `/realtime` and `/teams`; create additional service directories only when their domain and deployment boundary are clear.

```text
/
├── agents/                # Agent system; primary is its adk-go engine
├── apps/                  # A member's third-party apps, through Composio
├── auth/                  # Identity and access service
├── batching/              # Coalesces app triggers before agent wakeups
├── billing/               # Subscriptions and entitlements
├── email/                 # Email delivery service
├── files/                 # Organization object storage on S3
├── knowledge/             # Extracts documents for retrieval
├── rag/                   # Corpora, chunking, embeddings and grounded answers
├── realtime/              # The web app's conversations: their purges, and their events on the bus
├── teams/                 # Teams service
├── cmd/oryxa/             # The one binary every service runs in
├── contracts/             # Versioned event and retrieval HTTP contracts
├── internal/              # Module-private shared runtime boundaries
├── test/                  # k6 stack journey for teams, files, and email
└── <service>/             # One directory per future service
```

Every service follows the same internal shape once it is created:

```text
/<service>
├── service.go             # Definition, Migrate and Mount: how the binary runs it
├── internal/              # Service-private Go packages
├── infra/                 # AWS resources owned by this service
└── README.md              # Service contract, provisioning and run instructions
```

Keep service boundaries explicit: a service owns its API, its data and its AWS
resources. Services run in one process, but they still reach one another only
over HTTP and events, never through another service's `internal` packages, so
any of them can move to a process of its own. A path belongs to exactly one
service: the binary refuses to start when two services' routes would match the
same request. Avoid a generic `common` directory. A narrowly
scoped implementation used by services in this module can live in root
`/internal`; `/pkg` is reserved for stable contracts intended for external Go
consumers.

`/auth` owns Cognito-based Google and email/password sign-in,
PostgreSQL-backed verification/reset actions and
users/organizations/memberships, transactional EventBridge publishing, and
agent grants: a member's standing consent for agents to act as them while they
are away.
`/billing` owns organizations' Dodo Payments subscriptions and the
entitlements they grant, which auth enforces as member limits. `/email`
remains a delivery boundary: it sends what auth, billing and teams events call
for but never performs their transitions. A team-call notice goes out only
when that member's `notify_email` switch is on. `/files` owns organization
object storage: a private S3 bucket, PostgreSQL metadata, and credential-free
`oryxa.files` events. Other services ask Files for objects rather than reading
its bucket. `/knowledge` extracts documents from connected apps and Files,
classifies connected-app documents, and publishes versioned document events. `/rag` places those
documents into scoped corpora, indexes them, retrieves passages, and generates
grounded answers with citations. See [Knowledge and retrieval](#knowledge-and-retrieval),
[Knowledge](knowledge/README.md), and [RAG](rag/README.md). `/teams` owns teams inside an
organization and who is on them: organization admins and team admins share the
same control. An AI has three tools: `list_teams`, `call_team`, and
`call_team_members`. It publishes as `oryxa.teams` and consumes from its own
EventBridge queue. Its contract lives in `teams/README.md`.

`/apps` owns a member's third-party apps. Composio holds every credential and
runs the calls; apps holds what no vendor can decide: which connection a call
uses, derived from auth's answer about the caller and never from the request,
which tools may run without the member confirming, and what is announced when
something happens. A tool Composio marks read-only may run unconfirmed;
anything else needs an idempotency key, so a retry is not a second change. Apps
offers agents four MCP tools rather than the catalog's thousands, verifies every
Composio webhook before reading it, and publishes `oryxa.apps`. Its contract
lives in `apps/README.md`.

`/batching` consumes those app-trigger events and converts them into bounded,
durable `oryxa.batching` batches. Its default policy coalesces five events or
reaches a persisted per-member cadence deadline, adaptively changes the target
from rolling traffic, caps a batch at twenty, preserves every trigger payload,
and exposes a Jev-backed immediate lane. Agents consumes its
`batching.trigger-batch.ready.v1` rather than app triggers themselves, and
wakes Krude for each batch as it arrives. Its contract lives in
`batching/README.md`.

`/realtime` is the platform's way to what the web app keeps only in its
Worker's Durable Objects — direct messages and teamspace side conversations.
When a member leaves an organization or the organization is deleted, it hears
so from auth and sends the Worker one signed `POST /internal/realtime/purge`
for each, recording each purge done. The Worker in turn sends it signed
`POST /v1/realtime/events` calls for the messages posted and conversations
read there, which it publishes as `oryxa.realtime`: identities and lifecycle
state, never what was said. It is off, serving no route, until
`REALTIME_WORKER_URL`, `REALTIME_PURGE_SECRET` and its queue are set. Its
contracts, with golden signature vectors for the Worker's side, live in
`realtime/README.md`.

`/agents` holds the agent system. `/agents/primary` is its adk-go engine,
where Krude works as the member it assists. It runs in the binary as the
`agents` service, and its
development UI and benchmark are commands of their own; see
`agents/primary/README.md`.

## Knowledge and retrieval

Knowledge reads GitHub, Notion, Slack, Google Drive, and Gmail through a
member's Composio connection; it stores no provider credential. Connecting an
app can register bounded indexing sources automatically. With the uploader's
standing grant, Files uploads can create or refresh sources for organization,
team, or private member documents. Knowledge normalizes text and judges
connected-app documents to exclude low-value material and classify what
remains. It publishes again when content **or searchable provenance** changes.
Large text is read back through a narrow service-authenticated endpoint; PDF
and Office originals can be handed to the managed knowledge base for parsing.

RAG keeps one Knowledge corpus per owner within an organization: one for the
organization, one for each team that has documents, and one for each member
that has documents. Organization boundaries are enforced on storage and
retrieval. Team members and organization admins can search team documents;
private member documents are available to that member and organization admins.
Organization documents default to `access: members`; an admin can mark a
document `access: admins`. Files' access label follows the document into RAG,
and retrieval applies the ACL before a passage reaches reranking or generation.

The production configuration selects an Amazon Bedrock **Managed Knowledge
Base** for new corpora. One knowledge base serves the deployment, with
organization, corpus, and access filters on every retrieval; corpus separation
does not require a knowledge base per organization or member. RAG sends
retrieved passages to Gemini on Vertex AI for grounded answers. Existing
corpora retain their recorded backend. Local development uses pgvector; direct
S3 Vectors and Bedrock Converse remain available as alternative paths.

Matches and citations carry the originating provider, Knowledge source ID,
document kind, and semantic category when present. The agent's Knowledge tool
passes provider, kind, and category through with its search passages. The
versioned event types are in `contracts/events/knowledge/v1` and
`contracts/events/rag/v1`; the retrieval HTTP contracts are in
[`contracts/http`](contracts/http/README.md). Targeted tests and live Gemini
and Bedrock smoke tests have exercised the pipeline. Deployment still needs
to apply the ACL/provenance migrations and reindex existing documents before
the new metadata is guaranteed in production search results.

## Agents and third-party apps

An agent acts as the member it works for, with that member's own Cognito
identity, never as a service impersonating them: a run the member is present
for forwards their own token, and a run they are absent for asks auth for a
short-lived token under the grant they gave (`agents/primary/internal/memberauth`).

Third-party apps are the `/apps` service, on Composio. The `connections` and
`integrations` services, their event contracts, the generated provider clients,
knowledge's former direct-provider GitHub, Notion and Slack sources, and
Krude's integrations toolboxes were archived on 2026-09-17; the code is at tag
`archive/connections-and-integrations-2026-09-17`, and a `git checkout` of that
tag restores any of it.

| | Where it stands |
| --- | --- |
| Connecting an app | Composio's hosted flow, for every app Composio holds an app for or that takes a credential the member types in, started from the web app's Connections screen |
| Acting in an app | Krude's `apps` toolbox, over apps' MCP endpoint: reads run unconfirmed, writes ask the member and carry an idempotency key |
| Reacting to an app | Triggers deliver, verified, as `apps.trigger.received.v1`; batching groups them per member, and each batch wakes Krude in the member's "Updates from your apps" thread, to report what happened |
| Running an agent as a member | The `agents` service's run API: a member's threads with Krude, each run streamed as server-sent events with the member's own token on it, and a tool that asks first waiting for the member's answer. The web app has no screen to grant or withdraw agent access |

Not yet:

- An OAuth app of Oryxa's own, for apps Composio holds none for.
- A web screen for agent grants.
- The agent grant against a provisioned Cognito agent client has not run yet.
- An end-to-end run of apps against a real Composio project.

## Shared PostgreSQL and observability

Apps, auth, batching, billing, files, knowledge, rag, teams and the ADK session runtime use
`/internal/postgreskit`. The binary opens one native
`pgxpool` that every service shares, sized by `ORYXA_DATABASE_MAX_CONNS`; `sqlx`
wraps that same pool through pgx's `database/sql` adapter, so it does not create
a second connection budget. PostgreSQL statements and transactions remain on
native pgx where its semantics matter. ADK's GORM session service, used by the
agents development UI, keeps a pool of its own, isolated in the
`agents_primary` schema.

Every service's migrations run before that pool opens, on a short-lived pool
and under one advisory lock, so replicas starting together take turns and every
shared connection sees the extensions the migrations installed. Each file runs
once: `oryxa_migrations` records what was applied, by service, name and
checksum, so a restart skips unchanged files rather than repeating their locks
and backfills, and a file edited in place is applied again.

The boundary discovers these database capabilities:

| Product | PostgreSQL extension name | Runtime behavior |
| --- | --- | --- |
| ltree | `ltree` | Version discovery and optional startup enforcement |
| pg_trgm | `pg_trgm` | Version discovery and optional startup enforcement |
| pgvector | `vector` | Per-connection pgx vector codec registration; rag requires it |
| [Apache AGE](https://age.apache.org/) | `age` | Version discovery and optional startup enforcement |

The database image or managed database control plane should own installing
and upgrading extensions. Teams' migrations still run `CREATE EXTENSION IF NOT
EXISTS` for `vector`, `ltree`, `pg_trgm`, `btree_gist` and `btree_gin`, so they
run before rag's, whose tables need `vector`. Set
`POSTGRES_REQUIRED_EXTENSIONS=ltree,pg_trgm,vector,age` to fail startup when
the expected capability set is incomplete. `pgvector` is accepted as an alias
for `vector`, and `Apache AGE`, `apache_age`, or `apache-age` are accepted as
aliases for `age`.

To inspect the mini database directly:

```sh
docker --context mac-mini exec oryxa-go-postgres-1 \
  psql -U oryxa -d oryxa -c \
  "SELECT extname, extversion FROM pg_extension WHERE extname IN ('age', 'ltree', 'pg_trgm', 'vector') ORDER BY extname"
```

`/internal/observability` gives the binary structured JSON logs, each line
naming the `component` (the service) that wrote it,
W3C trace propagation, HTTP/AWS SDK/pgx spans, PostgreSQL pool metrics, and Go
runtime metrics, all under one `service.name`, `oryxa`, with spans named per
service. Set `OTEL_EXPORTER_OTLP_ENDPOINT` for OTLP/gRPC export, or use
the standard signal-specific trace and metric endpoint variables. With no
endpoint, structured logs remain enabled and telemetry export is a no-op.

Services talk through EventBridge, each consuming what it needs from its own SQS queue. `docker-compose.yml` runs the shared local dependencies — Postgres is `pgvector/pgvector:0.8.6-pg17` so `vector`, `ltree` and `pg_trgm` are available; LocalStack and Moto stand in for AWS (EventBridge, SQS, DynamoDB, and S3); Mailpit catches local SMTP at [http://localhost:8025](http://localhost:8025). Each service's README covers provisioning it, and the root `.env.example`
configures the binary and every provisioner alike. `/test` is the k6 client journey across teams, files, and email; see `test/README.md`.
