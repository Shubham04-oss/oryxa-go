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

Services still reach one another over HTTP, exactly as when each ran alone.
A URL variable naming a service the process runs — `AUTH_URL`, `TEAMS_URL`,
`FILES_URL`, `CONNECTIONS_URL`, `INTEGRATIONS_URL` — defaults to the process
itself; set, it wins, so a service can live elsewhere.

The image is built from the repository root:

```sh
docker build -t oryxa:dev .
docker compose -f compose.oryxa.yaml up -d --build
```

## Service layout

Each service owns a lowercase, hyphen-free directory at the repository root. The services so far are `/agents`, `/auth`, `/billing`, `/connections`, `/email`, `/files`, `/integrations`, `/knowledge`, `/rag` and `/teams`; create additional service directories only when their domain and deployment boundary are clear.

```text
/
├── agents/                # Agent system; primary is its adk-go engine
├── auth/                  # Identity and access service
├── billing/               # Subscriptions and entitlements
├── connections/           # Third-party apps, connections and their credentials
├── email/                 # Email delivery service
├── files/                 # Organization object storage on S3
├── integrations/          # Provider triggers and actions, over HTTP and MCP
├── knowledge/             # Extracts documents from connected apps for retrieval
├── rag/                   # Corpora, chunking, embeddings and grounded answers
├── teams/                 # Teams service
├── cmd/oryxa/             # The one binary every service runs in
├── contracts/             # Versioned event contracts services publish
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
when that member's `notify_email` switch is on. `/connections` owns how an
organization reaches third-party accounts: the apps it enables, the
connections its members, teams and admins make, the credentials behind them,
and issuing tokens to services that call providers. A credential leaves it
only in a token-issuance response; its contract lives in
`connections/README.md`. `/integrations` owns what providers send Oryxa: the subscriptions members
make on their connections, the webhook URLs providers deliver to, and the
`oryxa.integrations` events every delivery becomes. It also owns what Oryxa
does in a provider for a member: the actions a connection can run, offered
over HTTP and as one MCP endpoint per provider with a skill that says how to
use it. It stores no credential; it asks connections for one whenever it
registers, renews or removes something, or runs an action. Its contract lives
in `integrations/README.md`. `/files` owns organization object storage: a
private S3
bucket, PostgreSQL metadata, and credential-free `oryxa.files` events. Other
services never talk to the bucket; they ask files. `/knowledge` owns what has
been extracted from an organization's connected apps and when: the sources a
member asks to have indexed, the scheduled passes that read them a page at a
time, and the `oryxa.knowledge` events a retrieval pipeline holds. A document
is published only when its content hash changes, and it carries the scope of
the connection it was read through, because that is who may read it. Knowledge
stores no credential; it asks connections for one whenever it reads a
provider. Its contract lives in `knowledge/README.md`. `/rag` owns
retrieval: corpora and the documents in them, the chunks they become, the vectors behind
those, and the grounded answers they support. A corpus pins its embedding
provider, model, width and metric at creation and never changes them, because
vectors from two models do not share a space. Vectors live in pgvector or in
Amazon S3 Vectors; the chunk text stays in PostgreSQL either way, so a citation
addresses a byte range of a document still on disk. Rag takes extracted text
and says so: turning a PDF or a page into text stays upstream. Its contract
lives in `rag/README.md`. `/teams` owns teams inside an
organization and who is on them: organization admins and team admins share the
same control. An AI has three tools: `list_teams`, `call_team`, and
`call_team_members`. It publishes as `oryxa.teams` and consumes from its own
EventBridge queue. Its contract lives in `teams/README.md`.

`/agents` holds the agent system. `/agents/primary` is its adk-go engine,
where Krude loads each integrations provider as a toolbox and calls it as the
member it works for. It runs in the binary as the `agents` service, and its
development UI and benchmark are commands of their own; see
`agents/primary/README.md`.

## Agents and connected providers

An agent acts as the member it works for, with that member's own Cognito
identity, never as a service impersonating them: a run the member is present
for forwards their own token, and a run they are absent for asks auth for a
short-lived token under the grant they gave. Auth, connections and
integrations each let an agent's token do less than the member's own.

| | Where it stands |
| --- | --- |
| Connecting a provider | Members connect any of connections' 17 providers from the web app. An agent can list the member's connections but never change one |
| Acting in a provider | GitHub, Slack, Linear, Microsoft and HubSpot have actions, over integrations' HTTP API and one MCP endpoint each. Krude loads each as a toolbox, and every write asks the member first |
| Reacting to a provider | Integrations turns deliveries into `integrations.trigger.received.v1` events, but nothing starts an agent run from one, and no tool lets an agent subscribe |
| Running an agent as a member | Only `agents/primary`'s development UI runs Krude, acting through the member's grant. the `agents` service has no run API, and the web app has no screen to grant or withdraw agent access |

Not yet:

- Triggers into agent runs, and subscription tools, so an agent can watch as
  well as act. Each provider's skill already describes its events.
- Actions and triggers for the 12 other providers members can connect, such
  as Google, Notion, Atlassian and Salesforce.
- A run API in the `agents` service that forwards the member's own token, and a web
  screen for agent grants.
- Live runs: only GitHub's webhook has run against the real provider. The live
  action tests, the Slack, Linear and Microsoft trigger tests, and the agent
  grant against a provisioned Cognito agent client have not run yet.

## Shared PostgreSQL and observability

Auth, billing, connections, files, integrations, knowledge, rag, teams and the
ADK session runtime use `/internal/postgreskit`. The binary opens one native
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
