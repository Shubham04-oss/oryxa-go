# Oryxa

Oryxa is an AWS-backed Go monorepo. It starts with a single Go module and will grow as independently deployable services are introduced.

## Service layout

Each service owns a lowercase, hyphen-free directory at the repository root. The services so far are `/agents`, `/auth`, `/billing`, `/email`, `/files`, `/integrations` and `/teams`; create additional service directories only when their domain and deployment boundary are clear.

```text
/
├── agents/                # Agent system; primary is its adk-go engine
├── auth/                  # Identity and access service
├── billing/               # Subscriptions and entitlements
├── email/                 # Email delivery service
├── files/                 # Organization object storage on S3
├── integrations/          # Third-party connection and automation control plane
├── teams/                 # Teams service
├── contracts/             # Versioned event contracts services publish
├── internal/              # Module-private shared runtime boundaries
├── test/                  # k6 stack journey for teams, integrations, and email
└── <service>/             # One directory per future service
```

Every service follows the same internal shape once it is created:

```text
/<service>
├── cmd/<service>/         # Executable entrypoint
├── internal/              # Service-private Go packages
├── infra/                 # AWS resources owned by this service
├── migrations/            # Optional, service-owned data migrations
├── README.md              # Service contract and local run instructions
└── go.mod                 # Only if the service needs an independent module
```

Keep service boundaries explicit: a service owns its API, data, AWS resources,
and deployment configuration. Avoid a generic `common` directory. A narrowly
scoped implementation used by services in this module can live in root
`/internal`; `/pkg` is reserved for stable contracts intended for external Go
consumers.

`/auth` owns Cognito-based Google and email/password sign-in,
PostgreSQL-backed verification/reset actions and
users/organizations/memberships, and transactional EventBridge publishing.
`/billing` owns organizations' Dodo Payments subscriptions and the
entitlements they grant, which auth enforces as member limits. `/email`
remains a delivery boundary: it sends what auth, billing and teams events call
for but never performs their transitions. A team-call notice goes out only
when that member's `notify_email` switch is on. `/integrations` contains the
Go control plane, webhook worker, and lightweight TypeScript connector runtime
built from approved Activepieces pieces. It covers Slack, GitHub, Google, and
Microsoft catalog operations, plus native GitHub hooks, Gmail and Calendar
watches, and Microsoft Graph subscriptions for mail and calendar changes.
Successful connections, trigger registrations,
execution claims, and domain outbox events are durable PostgreSQL records;
other services receive only the credential-free connection catalog. The full
Activepieces platform is not deployed. Each approved connector tool also has a
call-scoped `SKILL.md` embedded in the integrations catalog, so a direct tool or
future MCP server can load only the selected provider operation's instructions
for a smaller model. `/files` owns organization object storage: a private S3
bucket, PostgreSQL metadata, and credential-free `oryxa.files` events. Other
services never talk to the bucket; they ask files. `/teams` owns teams inside an
organization and who is on them: organization admins and team admins share the
same control. An AI has three tools: `list_teams`, `call_team`, and
`call_team_members`. It publishes as `oryxa.teams` and consumes from its own
EventBridge queue. Its contract lives in `teams/README.md`.

`/agents` holds the agent system. `/agents/primary` is its adk-go engine and
the first service with its own Go module: it replaces the root module with
`../..` for `/internal`, so adk-go's dependency upgrades stay out of the module
the other services share. The root `go test ./...` does not reach it;
`agents/primary/README.md` has its checks.

## Shared PostgreSQL and observability

Auth, billing, files, integrations, teams and the ADK session runtime use
`/internal/postgreskit`. Each process owns one native `pgxpool`; `sqlx` wraps
that same pool through pgx's `database/sql` adapter, so it does not create a
second connection budget. PostgreSQL statements and transactions remain on
native pgx where its semantics matter. ADK's GORM session service shares that
same pool and is isolated in the `agents_primary` schema.

The boundary discovers these database capabilities:

| Product | PostgreSQL extension name | Runtime behavior |
| --- | --- | --- |
| ltree | `ltree` | Version discovery and optional startup enforcement |
| pg_trgm | `pg_trgm` | Version discovery and optional startup enforcement |
| pgvector | `vector` | Per-connection pgx vector codec registration |
| [Apache AGE](https://age.apache.org/) | `age` | Version discovery and optional startup enforcement |

Applications do not run `CREATE EXTENSION`; the database image or managed
database control plane owns installation and upgrades. Set
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

`/internal/observability` gives agents primary, auth, billing, email, files, integrations and teams structured JSON logs,
W3C trace propagation, HTTP/AWS SDK/pgx spans, PostgreSQL pool metrics, and Go
runtime metrics. Set `OTEL_EXPORTER_OTLP_ENDPOINT` for OTLP/gRPC export, or use
the standard signal-specific trace and metric endpoint variables. With no
endpoint, structured logs remain enabled and telemetry export is a no-op.

Services talk through EventBridge, each consuming what it needs from its own SQS queue. `docker-compose.yml` runs the shared local dependencies — Postgres is `pgvector/pgvector:0.8.6-pg17` so `vector`, `ltree` and `pg_trgm` are available; LocalStack and Moto stand in for AWS (EventBridge, SQS, DynamoDB, and S3); Mailpit catches local SMTP at [http://localhost:8025](http://localhost:8025). Each service's README covers provisioning and running it. `/test` is the k6 client journey across teams, files, integrations, and email; see `test/README.md`.
