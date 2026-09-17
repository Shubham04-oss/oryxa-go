# Oryxa

Oryxa is an AWS-backed Go monorepo. It starts with a single Go module and will grow as independently deployable services are introduced.

## Service layout

Each service owns a lowercase, hyphen-free directory at the repository root. The services so far are `/agents`, `/auth`, `/billing`, `/connections`, `/email`, `/files`, `/integrations`, `/knowledge` and `/teams`; create additional service directories only when their domain and deployment boundary are clear.

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
├── teams/                 # Teams service
├── contracts/             # Versioned event contracts services publish
├── internal/              # Module-private shared runtime boundaries
├── test/                  # k6 stack journey for teams, files, and email
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
provider. Its contract lives in `knowledge/README.md`. `/teams` owns teams inside an
organization and who is on them: organization admins and team admins share the
same control. An AI has three tools: `list_teams`, `call_team`, and
`call_team_members`. It publishes as `oryxa.teams` and consumes from its own
EventBridge queue. Its contract lives in `teams/README.md`.

`/agents` holds the agent system. `/agents/primary` is its adk-go engine,
where Krude loads each integrations provider as a toolbox and calls it as the
member it works for. It is the first service with its own Go module: it
replaces the root module with `../..` for `/internal`, so adk-go's dependency
upgrades stay out of the module
the other services share. The root `go test ./...` does not reach it;
`agents/primary/README.md` has its checks.

## Agents and connected providers

An agent acts as the member it works for, with that member's own Cognito
identity, never as a service impersonating them: a run the member is present
for forwards their own token, and a run they are absent for asks auth for a
short-lived token under the grant they gave. Auth, connections and
integrations each let an agent's token do less than the member's own.

| | Where it stands |
| --- | --- |
| Connecting a provider | Members connect any of connections' 17 providers from the web app. An agent can list the member's connections but never change one |
| Acting in a provider | GitHub, Slack, Linear and Microsoft have actions, over integrations' HTTP API and one MCP endpoint each. Krude loads each as a toolbox, and every write asks the member first |
| Reacting to a provider | Integrations turns deliveries into `integrations.trigger.received.v1` events, but nothing starts an agent run from one, and no tool lets an agent subscribe |
| Running an agent as a member | Only `agents/primary`'s development UI runs Krude, acting through the member's grant. `cmd/primary` has no run API, and the web app has no screen to grant or withdraw agent access |

Not yet:

- Triggers into agent runs, and subscription tools, so an agent can watch as
  well as act. Each provider's skill already describes its events.
- Actions and triggers for the 13 other providers members can connect, such
  as Google, Notion, Atlassian, HubSpot and Salesforce.
- A run API in `cmd/primary` that forwards the member's own token, and a web
  screen for agent grants.
- Live runs: only GitHub's webhook has run against the real provider. The live
  action tests, the Slack, Linear and Microsoft trigger tests, and the agent
  grant against a provisioned Cognito agent client have not run yet.

## Shared PostgreSQL and observability

Auth, billing, connections, files, integrations, knowledge, teams and the ADK
session runtime use `/internal/postgreskit`. Each process owns one native `pgxpool`; `sqlx` wraps
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

`/internal/observability` gives agents primary, auth, billing, connections,
email, files, integrations and teams structured JSON logs,
W3C trace propagation, HTTP/AWS SDK/pgx spans, PostgreSQL pool metrics, and Go
runtime metrics. Set `OTEL_EXPORTER_OTLP_ENDPOINT` for OTLP/gRPC export, or use
the standard signal-specific trace and metric endpoint variables. With no
endpoint, structured logs remain enabled and telemetry export is a no-op.

Services talk through EventBridge, each consuming what it needs from its own SQS queue. `docker-compose.yml` runs the shared local dependencies — Postgres is `pgvector/pgvector:0.8.6-pg17` so `vector`, `ltree` and `pg_trgm` are available; LocalStack and Moto stand in for AWS (EventBridge, SQS, DynamoDB, and S3); Mailpit catches local SMTP at [http://localhost:8025](http://localhost:8025). Each service's README covers provisioning and running it. `/test` is the k6 client journey across teams, files, and email; see `test/README.md`.
