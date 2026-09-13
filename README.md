# Oryxa

Oryxa is an AWS-backed Go monorepo. It starts with a single Go module and will grow as independently deployable services are introduced.

## Service layout

Each service owns a lowercase, hyphen-free directory at the repository root. The services so far are `/auth`, `/billing`, `/email` and `/integrations`; create additional service directories only when their domain and deployment boundary are clear.

```text
/
├── auth/                  # Identity and access service
├── billing/               # Subscriptions and entitlements
├── email/                 # Email delivery service
├── integrations/          # Third-party connection and automation control plane
├── contracts/             # Versioned event contracts services publish
├── internal/              # Module-private shared runtime boundaries
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
remains a delivery boundary: it sends what auth and billing events call for
but never performs their transitions. `/integrations` contains the Go control
plane, webhook worker, and lightweight TypeScript connector runtime built from
approved Activepieces pieces. It covers Slack, reviewed
GitHub/Google/Microsoft actions, and native Microsoft Graph subscriptions for
mail and calendar changes. Successful connections, trigger registrations,
execution claims, and domain outbox events are durable PostgreSQL records;
other services receive only the credential-free connection catalog. The full
Activepieces platform is not deployed.

## Shared PostgreSQL and observability

Auth and integrations use `/internal/postgreskit`. Each process owns one native
`pgxpool`; `sqlx` wraps that same pool through pgx's `database/sql` adapter, so
it does not create a second connection budget. PostgreSQL statements and
transactions remain on native pgx where its semantics matter.

The boundary discovers these database capabilities:

| Product | PostgreSQL extension name | Runtime behavior |
| --- | --- | --- |
| ltree | `ltree` | Version discovery and optional startup enforcement |
| pg_trgm | `pg_trgm` | Version discovery and optional startup enforcement |
| pgvector | `vector` | Per-connection pgx vector codec registration |
| [pgGraph](https://github.com/evokoa/pggraph) | `graph` | Version discovery and optional startup enforcement |

Applications do not run `CREATE EXTENSION`; the database image or managed
database control plane owns installation and upgrades. Set
`POSTGRES_REQUIRED_EXTENSIONS=ltree,pg_trgm,vector,graph` to fail startup when
the expected capability set is incomplete. `pgvector` is accepted as an alias
for `vector`, and `pgGraph`, `pg_graph`, or `pg graph` are accepted as aliases
for `graph`.

To inspect the mini database directly:

```sh
docker --context mac-mini exec oryxa-go-postgres-1 \
  psql -U oryxa -d oryxa -c \
  "SELECT extname, extversion FROM pg_extension WHERE extname IN ('ltree', 'pg_trgm', 'vector', 'graph') ORDER BY extname"
```

`/internal/observability` gives auth and integrations structured JSON logs,
W3C trace propagation, HTTP/AWS SDK/pgx spans, PostgreSQL pool metrics, and Go
runtime metrics. Set `OTEL_EXPORTER_OTLP_ENDPOINT` for OTLP/gRPC export, or use
the standard signal-specific trace and metric endpoint variables. With no
endpoint, structured logs remain enabled and telemetry export is a no-op.

Services talk through EventBridge, each consuming what it needs from its own SQS queue. `docker-compose.yml` runs the shared local dependencies; each service's README covers provisioning and running it.
