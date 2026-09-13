# Integrations service

Status: the connection, action, refresh, and webhook execution foundations are
implemented. Slack has outbound actions plus its app-level `new-message`
trigger; GitHub and Google Workspace have reviewed outbound action slices;
Microsoft 365 has reviewed Outlook, Calendar, and OneDrive actions plus native
Graph webhook subscriptions for new mail and calendar changes. The
deterministic suite runs the real pinned piece code against mocked provider
endpoints, and the combined lifecycle runs against PostgreSQL and LocalStack on
the mini. The k6 client journey across integrations and teams is `/test`
(`./test/run.sh`). Live Cognito, KMS, Lambda, and provider sandbox validation
remains an AWS staging concern; no real customer credentials are used by the
local suite.

The integrations service connects an Oryxa organization to third-party
products. It reuses the MIT-licensed Activepieces piece framework and selected
community pieces as connector code without deploying the Activepieces app,
database, Redis queue, UI, workers or workflow engine.

## PostgreSQL and observability boundary

The Go process uses `/internal/postgreskit` and owns one instrumented native
`pgxpool`. Its `sqlx` facade shares that pool through pgx's `database/sql`
adapter rather than opening another connection budget. Native pgx remains the
transaction and repository API; sqlx is available for named/struct-oriented
queries where it is useful.

PostgreSQL capability discovery uses the actual SQL extension names:

| Product | Extension name | Integrations behavior |
| --- | --- | --- |
| ltree | `ltree` | Discover version; optionally require at startup |
| pg_trgm | `pg_trgm` | Discover version; optionally require at startup |
| pgvector | `vector` | Register pgx codecs on every pooled connection |
| [pgGraph](https://github.com/evokoa/pggraph) | `graph` | Discover version; optionally require at startup |

Set `POSTGRES_REQUIRED_EXTENSIONS=ltree,pg_trgm,vector,graph` to enforce the
full set. The aliases `pgvector`, `pgGraph`, `pg_graph`, and `pg graph` are
normalized to the corresponding SQL names. Extension installation and upgrades
remain database provisioning concerns; application migrations never run
`CREATE EXTENSION`. A pgvector-based image does not automatically include
pgGraph, so the production and mini images must explicitly provide both.

Observability includes JSON access/application logs, W3C trace propagation,
Cloudflare public-endpoint HTTP spans and metrics, AWS SDK spans, privacy-safe
pgx spans, pool metrics, and Go runtime metrics. Access logs use normalized
route patterns and do not record provider payloads, query strings, credentials,
or inbound client request IDs. `/healthz` is excluded from access logs and
server spans.

OTLP/gRPC export is enabled by `OTEL_EXPORTER_OTLP_ENDPOINT` or the standard
signal-specific trace/metric endpoint variables. Without an endpoint the
service keeps structured logs and uses no telemetry exporter. Use
`DEPLOYMENT_ENVIRONMENT`, `LOG_LEVEL`, `LOG_ADD_SOURCE`, and the standard OTLP
TLS/header variables for resource and transport configuration.

## Decision

Build a small Oryxa-owned piece runtime rather than self-hosting Activepieces.

An Activepieces piece is a TypeScript module that exports metadata, auth
requirements, actions and triggers. An action ultimately exposes
`run(context)`; triggers expose `onEnable`, `onDisable`, `run` and related
hooks. Oryxa can load an approved, pinned bundle and provide the compatible
context those functions need.

This gives us the catalog's provider-specific code without inheriting a second
product platform. It is not a promise that every upstream piece works. The
runtime supports an explicit subset of the framework, and CI rejects any piece
or operation that requires unsupported capabilities.

The boundary is:

- Go owns the public API, organization authorization, OAuth sessions,
  credential custody, connection lifecycle, commands, idempotency and Oryxa
  domain events.
- A narrow TypeScript runner loads approved Activepieces bundles, validates
  their auth, resolves dynamic properties and calls actions or trigger hooks.
- The runner is stateless and has no database, KMS, EventBridge or other AWS
  data-plane permissions. It receives one scoped credential in memory for one
  invocation and cannot enumerate connections.
- Pieces are connector adapters, not Oryxa domain services. Other services
  depend on Oryxa contracts and never import piece types.

## Implemented slices

Slack is the first compatibility target because it exercises a BYOC OAuth
application, provider identity, an app-level webhook, raw-body HMAC
verification and trusted workspace routing.

The current implementation includes:

- a static Go catalog with a closed, operation-level allowlist pinned to
  `@activepieces/piece-slack@0.17.10`,
  `@activepieces/piece-github@0.8.11`,
  `@activepieces/piece-gmail@0.15.0`,
  `@activepieces/piece-google-calendar@0.10.3`,
  `@activepieces/piece-google-drive@0.9.1`,
  `@activepieces/piece-microsoft-outlook@0.5.3`,
  `@activepieces/piece-microsoft-outlook-calendar@0.2.8`, and
  `@activepieces/piece-microsoft-onedrive@0.4.7`;
- admin-only BYOC registration and member authorization application logic,
  with one-use hashed state, fixed callbacks, bounded return URLs, encrypted
  PKCE verifiers, exact scope checks and a domain-shaped connected event;
- an actual Slack OAuth v2 adapter for authorization URL creation, code
  exchange and authorizing user/workspace identity extraction;
- a Microsoft identity-platform OAuth adapter using the tenant-scoped
  `organizations` endpoint, PKCE, offline access, refresh-token rotation, and
  Graph-backed user/tenant identity projection;
- AES-256-GCM envelope encryption using a fresh KMS data key and authenticated
  organization/resource binding, plus an AWS KMS adapter;
- a webhook admission core that checks route state, body size and timestamp,
  opens only the route's signing secret, invokes piece verification and
  parsing, handles challenges synchronously, deduplicates, and enqueues the
  exact authenticated raw payload;
- DynamoDB conditional claim/lease and SQS webhook queue adapters, tested
  against LocalStack on the `mac-mini` context;
- PostgreSQL persistence for org-enabled OAuth apps, successful connections
  and durable trigger/action execution state and their transactional outbox
  events, plus a credential-free cross-service connection catalog;
- Cognito bearer verification and PostgreSQL organization-role authorization;
- HTTP endpoints for BYOC create/rotation/disable, member catalog and consent,
  OAuth callback, connection disconnect, app-level webhook setup, trigger
  enable/disable and provider webhook ingress;
- a member catalog that presents every enabled app as either `available` or
  `connected`, with only safe provider identity metadata;
- an AWS Lambda broker client that gives the TypeScript runner the signing
  secret only for `events.verify`, not for parsing;
- a TypeScript runtime with a closed operation protocol and static allowlist.
  It imports the real published provider pieces; unsupported Activepieces
  flow, file, waitpoint and scheduling capabilities fail closed. The upstream
  license notice is retained beside the runtime;
- a Go SQS consumer that fans an authenticated app-level delivery only to
  active registrations for the same organization, profile and Slack workspace,
  invokes `trigger.run`, and maps output to a stable Slack message event;
- a native Microsoft Graph subscription controller that creates opaque
  per-connection callback routes, seals each subscription's `clientState`,
  validates notification batches, strips verified mail/calendar content before
  SQS, renews leases, and durably cleans them up on disconnect, profile disable,
  explicit trigger/connection/profile disable, or permanent reauthorization
  failure;
- an EventBridge outbox dispatcher, deployable Go and Lambda-runner images,
  and a minimal CloudFormation data-plane template.

The reviewed action surface is deliberately small:

- Slack: post a message, add a reaction, and find a channel;
- GitHub: create an issue, comment on an issue, get an issue, and list pull
  requests;
- Google Workspace: send via Gmail, create/list Calendar events, and list/get
  Drive metadata. Attachments and Drive downloads remain disabled because the
  runner intentionally has no file-storage capability;
- Microsoft 365: send via Outlook, create/list Calendar events, and list
  OneDrive files. OneDrive downloads remain disabled for the same file-storage
  boundary.

The reviewed trigger surface is equally explicit:

- Slack `new-message` uses the organization app-level webhook and requires the
  channel/private-channel history scopes configured by the profile;
- Microsoft `new-email` creates a per-connection Graph subscription for new
  Inbox messages and requires `Mail.Read`;
- Microsoft `calendar-event-changed` creates a per-connection Graph
  subscription for created, updated, or deleted events and requires
  `Calendars.Read`.

GitHub and Google triggers are not enabled yet. Their future webhook or polling
paths require separate provider-specific lifecycle and replay reviews; adding
an Activepieces package to the lock file alone never exposes an operation.

A connection belongs to a provider, not to one Activepieces package. One
Google consent grant can therefore execute reviewed Gmail, Calendar, and Drive
operations, and one Microsoft consent grant can execute reviewed Outlook,
Calendar, and OneDrive operations, when the organization enabled and the
member granted the required scopes. The action catalog selects the exact piece
and version for each call; the member's encrypted provider grant remains the
common authorization boundary.

## OAuth application modes

Connections and OAuth applications are different resources. An OAuth
application supplies a provider client ID, client secret, redirect URI and
allowed scopes; a connection is the grant made by one provider account when a
member completes consent.

Oryxa supports two credential profiles behind the same connection flow:

### Organization BYOC

This is the first mode. An organization admin registers an internal/private app
in the provider's developer console, configures Oryxa's exact callback URI, and
submits its client ID and secret to Oryxa. The profile belongs to that
organization and provider. Authorized organization members can then select it
and complete normal OAuth consent without ever seeing its client secret.

Admin ownership of the OAuth app does not make every resulting provider
connection organization-wide. Each connection records the consenting member,
provider subject/account and provider tenant/workspace. Organization policy
decides whether that connection remains personal, is shared with selected
workflows, or is made organization-wide.

### Oryxa managed

Later, Oryxa may register and verify its own provider application for one-click
connection. The member sees the same Oryxa connection flow, but the selected
credential profile is platform-owned. Managed client secrets are one per
provider and environment in Secrets Manager; they are never copied into tenant
records.

An organization explicitly chooses its default profile. Oryxa never silently
switches an existing connection between BYOC and managed credentials: refresh
tokens are bound to the client that issued them, so changing profile requires
fresh consent. BYOC remains available even after a managed profile exists.

### Permissions and scopes

- Only an organization owner/admin may create, rotate, disable or delete a
  BYOC profile.
- A member needs an integration connection permission to begin consent.
- The catalog policy defines the maximum scopes for each approved operation;
  an admin may narrow them but cannot expand them from the API.
- The callback checks the granted scopes and provider identity before making a
  connection active. Missing required scopes produce `reauthorization_required`
  rather than a partially working connection.
- Disabling or rotating an OAuth profile stops new authorizations. Existing
  connections are revalidated and marked for consent when the provider no
  longer accepts their refresh token.

The public callback URI is stable, for example
`https://connect.oryxa.example/v1/oauth/callback/<provider>`. Provider-specific
redirect URI rules are part of the catalog manifest shown to the admin during
BYOC setup.

## OAuth lifecycle

```text
Admin registers BYOC client ID + secret
  -> Go API validates role and catalog policy
  -> envelope-encrypt secret with organization/profile KMS context
  -> persist the org-enabled credential profile in PostgreSQL

Member starts connection
  -> choose allowed profile
  -> create one-use state bound to org, member, profile, provider and return URI
  -> generate PKCE verifier/challenge when supported
  -> redirect to provider consent

Provider callback
  -> atomically consume state and verify expiry
  -> verify callback provider and exact redirect URI
  -> decrypt selected app secret and exchange code
  -> validate granted scopes and resolve provider account/tenant identity
  -> encrypt tokens
  -> atomically save the PostgreSQL connection and connected outbox event
```

For a member, an active organization OAuth app has one of two V1 states:

- `available`: the organization enabled the app and this member has no active
  connection for its profile;
- `connected`: the member completed consent and the safe connection identity
  is present.

Only one active connection is allowed per organization, member and app
profile. The application checks before redirecting and PostgreSQL enforces the
same rule with a partial unique index, closing concurrent callback races. A
disabled app is not shown. Another member still sees an app as `available`
when a teammate has connected it, because grants remain member-owned in V1.

OAuth state stores no plaintext secrets. It is random, hashed at rest,
single-use, short-lived and bound to the initiating Cognito user and
organization membership. The callback does not trust organization, member or
return-URL values supplied directly in query parameters. PKCE supplements the
client secret; it does not replace state or redirect validation.

The implemented API surface is intentionally resource-oriented:

```text
POST   /v1/organizations/{org_id}/integration-apps
PATCH  /v1/organizations/{org_id}/integration-apps/{profile_id}
DELETE /v1/organizations/{org_id}/integration-apps/{profile_id}
GET    /v1/organizations/{org_id}/integrations
POST   /v1/organizations/{org_id}/connections/authorize
GET    /v1/oauth/callback/{provider}
DELETE /v1/organizations/{org_id}/connections/{connection_id}
POST   /v1/organizations/{org_id}/integration-apps/{profile_id}/webhook-routes
POST   /v1/organizations/{org_id}/triggers
DELETE /v1/organizations/{org_id}/triggers/{trigger_id}
POST   /v1/organizations/{org_id}/connections/{connection_id}/actions/{action_name}
POST   /v1/webhooks/{opaque_route}
```

Secret values are accepted only on create/rotation and are never returned.
Admin profile reads may expose the client ID and configuration metadata. The
member integrations response omits client IDs and all encrypted fields; it
returns the profile/provider, allowed scopes, `available|connected`, and safe
identity metadata for an active connection.

## Why not the full Activepieces stack

The full platform is valuable when we want its visual flow builder, scheduling,
durable multi-step workflows, run history and administration. Those are not
requirements for a connector catalog, and deploying them would add an app
tier, worker tier, a separate PostgreSQL database, Redis and object storage.

We should revisit self-hosting or licensed embedding only if Oryxa decides to
offer customer-authored, arbitrary multi-step automations. Until then, Oryxa
defines the workflows in Go and uses pieces for the individual provider call
or trigger.

## Upstream feasibility check

The current Activepieces source confirms that `createPiece`, `createAction` and
`createTrigger` construct ordinary objects with callable functions. Its own
engine adds property processing, context adapters, retries, checkpoints and
flow control around those functions; our runtime must replace only the subset
we admit.

At upstream commit `89aeeae8c1eb98428210ec7d214f933b96aa1987`, a source scan
found 732 community piece directories. At least 140 use the framework store,
39 use file services, 12 use run-control hooks, 233 declare webhook triggers
and 179 declare polling triggers. Only a small fraction of piece packages have
local test/spec files. These numbers make compatibility gating and our own
contract tests mandatory; copying the catalog does not make it production
ready by itself.

## Repository shape

The intended implementation stays within this service boundary:

```text
/integrations
├── cmd/integrations/          # Go API and command entrypoint
├── internal/                  # Domain, PostgreSQL, ephemeral DynamoDB, KMS and events
├── runtime/                   # Small TypeScript piece adapter and Lambda entrypoint
├── pieces.lock.json           # Approved package, version, integrity and capabilities
├── infra/                     # AWS resources owned by integrations
└── README.md
```

Consume exact published package versions for
`@activepieces/pieces-framework`, `@activepieces/pieces-common` and each
approved piece. Do not vendor the entire upstream monorepo. If Oryxa must patch
a piece, copy only that piece into `integrations/runtime/pieces/<name>`, retain
its license notice and record the upstream commit and patch in the lock file.

CI bundles the allowlist with esbuild into a Lambda container image. Nothing
installs from npm at runtime. The generated catalog is static JSON derived from
piece metadata so the Go API and frontend do not execute TypeScript just to
list integrations.

## Supported framework surface

### Version 1

- Piece and action metadata.
- Secret text, basic, custom and OAuth2 auth definitions.
- Auth validation and connection display-name resolution.
- Static properties, dynamic dropdowns and dynamic property definitions.
- Actions using `auth` and `propsValue`.
- A no-op output updater and tags collector.
- A scoped key-value `store` adapter when an approved action genuinely needs
  it.

### Added deliberately, piece by piece

- Polling triggers: one EventBridge Scheduler schedule per active trigger.
- Files: S3-backed `files.write` with strict size, type and retention limits.
- Secondary connections: a brokered `connections.get` limited to explicitly
  linked connection IDs.

### First-class trigger support

- Per-connection webhook triggers with stable callback URLs.
- App-level/shared webhooks used by providers such as Slack, including piece
  `events.verify` and `events.parseAndReply` hooks.
- Trigger `onEnable`, `onDisable`, `onRenew`, `onHandshake` and `run` hooks.
- A DynamoDB-backed trigger store compatible with the framework's flow-scoped
  key-value operations.
- Raw-body signature verification, timestamp windows, provider-event
  deduplication and synchronous challenge responses.

### Not supported

- Activepieces flows, routers, loops, formulas or project variables.
- Waitpoints, pauses, resume URLs or synchronous flow responses.
- Custom code and arbitrary custom API-call actions.
- AI agents, tables or Activepieces project APIs.
- Any piece operation that expects Activepieces internal server endpoints.

The compatibility manifest is per action and trigger, not just per piece. A
piece may have ten simple actions and one unsupported action; Oryxa can expose
the ten safe actions without pretending the entire piece is compatible.

## Minimal runtime

```text
Cloudflare
  -> existing Oryxa API origin
     -> Go integrations API/broker
        -> PostgreSQL + ephemeral DynamoDB + KMS
        -> synchronous Invoke of piece-runner Lambda

Admin BYOC setup
  -> Go integrations API
  -> encrypted organization OAuth credential profile in PostgreSQL

Member OAuth
  -> Cloudflare connect callback
  -> Go integrations API
  -> selected BYOC or managed credential profile
  -> encrypted member/provider connection + outbox in PostgreSQL

Oryxa EventBridge event
  -> integration command SQS queue + DLQ
  -> Go dispatcher Lambda
     -> claim idempotency + decrypt one connection
     -> synchronous Invoke of piece-runner Lambda
     -> record result + publish domain event through outbox

Provider webhook
  -> Cloudflare callback route
  -> Go integrations ingress
     -> preserve raw bytes + look up opaque route
     -> Invoke piece verification/handshake hook synchronously
     -> reject replay + enqueue accepted payload
  -> webhook SQS queue + DLQ
  -> Go dispatcher -> piece trigger.run
  -> map output to an Oryxa command or domain contract

Microsoft Graph subscription
  -> Go broker creates one opaque per-connection callback + sealed clientState
  -> Graph validates the callback and delivers change notifications
  -> hourly broker scan renews subscriptions approaching expiry
  -> disconnect/profile disable durably queues Graph removal and local cleanup
```

The TypeScript runner uses an AWS Lambda container image because approved
pieces bring their own npm dependencies. Build it for arm64 in the same style
as the other services, pin the base image and deploy by immutable ECR digest.
The runner does not need a VPC: public SaaS calls avoid NAT cost and latency.

The Go broker is the security boundary. It reads and decrypts exactly one
connection from PostgreSQL, invokes the runner synchronously with a bounded payload, then
discards the plaintext. The runner execution role can write logs and nothing
else. In particular, it cannot read DynamoDB, call KMS, fetch arbitrary Secrets
Manager values or publish to EventBridge.

## Minimal AWS services

Reuse the regional EventBridge bus, Cloudflare-fronted API origin, existing
PostgreSQL cluster and observability. Integrations adds only:

- PostgreSQL tables for enabled OAuth apps, durable successful connections and
  the integrations outbox; this is not a separate Activepieces database;
- one DynamoDB table only for OAuth state, trigger runtime state, execution
  claims, opaque webhook routes and short-lived webhook deduplication;
- one KMS key for envelope-encrypted connection credentials;
- one standard webhook queue and one DLQ, isolated so provider bursts cannot
  starve outbound integration commands;
- one Go API/broker/dispatcher image, split into Lambda handlers later only if
  scaling or permissions require it;
- one TypeScript piece-runner Lambda container and ECR repository;
- an in-process hourly renewal scan and minute-level cleanup worker for the
  current always-on Go broker; their bounded entry points can move to
  EventBridge Scheduler when the broker is split into Lambda handlers;
- EventBridge Scheduler later for approved polling triggers;
- S3 only after an approved piece requires files or payloads too large for the
  normal invocation path.

There is no Activepieces RDS database, Redis/ElastiCache cluster, ECS service,
load balancer or Activepieces S3 run-log bucket.

## Data and credential model

Durable product state lives in PostgreSQL:

```text
integrations_oauth_apps       # org-enabled BYOC/managed profiles
integrations_connections      # successful member grants and encrypted tokens
integrations_triggers         # durable member-owned trigger registrations
integrations_trigger_executions # per-delivery/trigger retry and idempotency state
integrations_action_executions # member action idempotency, safe result and audit state
integrations_microsoft_graph_cleanup # leased subscription teardown jobs
integrations_outbox           # domain event written with the state transition
integrations_connection_catalog (view) # credential-free active connections
```

`GET /v1/organizations/{org_id}/integrations` left-joins the active profiles
to the requesting member's active connections. It does not cross-join every
organization membership or materialize one row per member, so merely enabling
an app does not multiply database rows.

The catalog view is the approved cross-service SQL surface. It contains
organization/member/profile IDs, pinned piece identity, provider account and
tenant identity, scopes and timestamps. It contains no OAuth client secret,
access token, refresh token, ciphertext, encrypted data key or KMS context.
Production service roles may receive `SELECT` on this view but not on the
underlying connection table. A service that must execute an integration asks
the integrations broker to do so; it never receives the grant.

[`infra/postgres-grants.sql`](infra/postgres-grants.sql) is the DBA-applied
grant boundary: it revokes public access to every private integrations table
and grants a named cross-service role `SELECT` on the safe view only.

Ephemeral and high-churn coordination remains in a single DynamoDB table:

```text
PK                                                   SK
OAUTH_STATE#<state_hash>                             STATE
WEBHOOK#<opaque_route>                               ROUTE
WEBHOOK_PROFILE#<org>#<profile>#<trigger>            ROUTE
WEBHOOK_CONNECTION#<org>#<profile>#<trigger>#<conn>  ROUTE
TRIGGER_STATE#<org>#<trigger>                        STATE
DEDUPE#webhook:<dedupe_hash>                         CLAIM
```

PostgreSQL connection rows contain safe metadata plus an AES-256-GCM
ciphertext and encrypted data key. KMS encryption context binds the data key
to the organization and connection IDs. OAuth state, dedupe records and
execution claims carry DynamoDB TTLs. Do not rely on TTL for correctness
because expiry deletion is asynchronous; every read also checks the logical
expiry timestamp.

Organization BYOC client secrets use the same envelope-encrypted PostgreSQL
pattern, bound to the organization and profile ID. Platform-managed client IDs
and secrets live in Secrets Manager, one secret per provider/environment.
Customer access/refresh tokens remain in the encrypted PostgreSQL connection
record in either mode.

Expiring grants use the existing conditional connection version:

1. The Go broker takes a short refresh lease.
2. It decrypts the current value and performs the provider's standard OAuth
   refresh using the same BYOC client that issued the grant.
3. It conditionally updates PostgreSQL with `WHERE version = <old>` and
   increments the version.
4. A competing invocation reloads the winner instead of overwriting it.

Refresh occurs shortly before expiry. A permanent provider rejection marks
the connection `reauthorization_required`, disables its triggers, and commits
the state transition with a credential-free domain event. Access tokens,
refresh tokens, OAuth client secrets, and raw provider errors never enter that
event.

## Operations

The runner accepts a closed operation envelope:

```text
catalog.metadata
auth.validate
auth.connection-identifier
property.options
action.run
trigger.enable
trigger.disable
trigger.renew
trigger.handshake
trigger.run
events.verify
events.parse-and-reply
```

Each request names an exact piece version and operation, carries an invocation
ID, organization ID, deadline and a size-limited input. The runner resolves the
name only from its compiled allowlist. It never accepts a package URL, module
path, JavaScript body or arbitrary export name.

Responses contain structured output or a stable error category. Both sides
redact configured secrets and token-shaped fields before logging. Raw action
inputs and outputs are not logged by default.

The public action endpoint requires an `Idempotency-Key`. PostgreSQL claims the
key per organization, connection, and action, hashes both the key and canonical
input, and persists only a provider-neutral result. A successful replay returns
that result without calling the provider again. A failed or indeterminate
side-effecting call is never retried automatically with the same key; the
caller must make the risk explicit by supplying a new key. This prevents the
broker from turning an uncertain provider response into a duplicate email,
message, issue, or calendar event.

## Events and retries

EventBridge continues to carry Oryxa domain facts, not arbitrary vendor
callbacks. Provider output must map to a product-owned command or event before
it reaches the shared bus. Raw payloads are transient; if a use case needs
replay, enable the optional encrypted S3 path with a short retention policy.

SQS is at-least-once. Webhook admission claims the provider delivery in
DynamoDB before enqueue. The worker separately claims each
`trigger_id + invocation_id` in PostgreSQL, so a retry skips trigger executions
already committed while retrying only failed or abandoned attempts. The
execution-success transition and its domain outbox record are one transaction.
The execution identity uses the admission dedupe hash, not a random worker ID,
so even a retry after an uncertain Dynamo commit reaches the same execution
record.
No local record can close the failure window around an external side effect;
side-effecting actions must still use provider idempotency keys when available.

Connection and catalog lifecycle facts may use source `oryxa.integrations`:

- `integrations.connection.connected.v1`;
- `integrations.connection.reauthorization-required.v1`;
- `integrations.connection.disconnected.v1`;
- `integrations.oauth-app.registered.v1`;
- `integrations.oauth-app.credentials-rotated.v1`;
- `integrations.oauth-app.disabled.v1`;
- `integrations.trigger.enabled.v1`;
- `integrations.trigger.disabled.v1`;
- `integrations.slack.message-received.v1`;
- `integrations.slack.message-sent.v1`;
- `integrations.slack.reaction-added.v1`;
- `integrations.github.issue-created.v1`;
- `integrations.github.issue-comment-created.v1`;
- `integrations.google.gmail-message-sent.v1`;
- `integrations.google.calendar-event-created.v1`;
- `integrations.microsoft.outlook-email-accepted.v1`;
- `integrations.microsoft.calendar-event-created.v1`;
- `integrations.microsoft.outlook-email-received.v1`;
- `integrations.microsoft.calendar-event-changed.v1`;
- `integrations.catalog.operation-enabled.v1`;
- `integrations.catalog.operation-disabled.v1`.

Runner failures, throttles and DLQ depth are operational telemetry unless a
customer-facing remediation workflow exists.

## Webhook ingestion

Webhooks use an opaque, unguessable route rather than organization or member
IDs in the URL. A route points to the exact credential profile and pinned
piece/trigger version. For Slack it is shared by active member triggers using
that organization profile; disabling one trigger removes only that trigger
from fan-out, while disconnecting its connection disables all of its triggers
transactionally.

There are two routing modes:

1. A per-connection webhook is registered by `trigger.enable` using its unique
   Oryxa callback URL. The route directly identifies the trigger.
2. An app-level webhook is registered once per OAuth credential profile. The
   piece-level event processor verifies and parses it, then returns a trusted
   provider account/workspace identifier. PostgreSQL selects only active
   connections and trigger subscriptions with that identifier. Managed OAuth apps use
   the same lookup but may fan out across organizations; payload-supplied Oryxa
   IDs are never trusted for routing.

The synchronous ingress path performs only what is required before a provider
deadline:

1. Retain the exact method, path, query, headers and raw body; do not parse and
   re-encode before signature verification.
2. Enforce route state, body size and a provider-specific timestamp window.
3. Invoke `events.verify`, `events.parseAndReply`, or `trigger.handshake` when
   the manifest requires it.
4. Atomically claim the provider event ID, or a canonical request hash when no
   ID exists, with a DynamoDB TTL.
5. Put the accepted envelope on the webhook SQS queue and return the provider's
   required 2xx/challenge response.

Downstream trigger execution, domain mapping and product work never block the
provider response. The worker deletes an SQS message only after every selected
trigger has succeeded or was already completed; failures remain for the queue's
redrive policy and DLQ. Ordering is opt-in per provider/account; standard SQS
is the default.

Cloudflare caching, browser challenges and HTML transformations are disabled
for OAuth and webhook callback paths. Cloudflare rate limits are
provider-aware and are not authentication. The piece/provider signature,
timestamp and dedupe key remain authoritative, and direct access to the AWS
origin is restricted independently.

Slack V1 uses its app-level webhook configured by the organization admin, so
there is no provider subscription lease to renew. Microsoft Graph uses one
subscription and opaque route per connection/trigger. The random
subscription `clientState` is encrypted with the route secret; the durable
trigger state stores only the Graph subscription ID and expiry. An immediate
and hourly broker scan renews subscriptions in their final 24 hours while a
PostgreSQL advisory lease excludes duplicate scans across broker replicas.
Trigger disable, profile disable, disconnect, and permanent refresh failure
atomically enqueue cleanup with the PostgreSQL state transition; the cleanup
worker closes ingress before attempting the idempotent provider deletion. If
the disconnected grant can no longer authorize deletion, it retains the job
until the maximum remote lease boundary rather than repeatedly calling Graph.

## Security and governance

Pieces are third-party executable code that receive customer credentials.
Their MIT license permits reuse, but their position in the catalog is not a
security approval.

- Pin every package and transitive dependency with integrity hashes.
- Run the checked-in install-time hardening rule that forces TLS certificate
  verification and removes provider request/response logging from the bundled
  Activepieces HTTP client; CI fails if any pinned piece retains either unsafe
  upstream behavior. Provider failures returned to Go are opaque categories,
  never upstream messages.
- Generate an SBOM, scan dependencies and the image, and retain notices.
- Review outbound hosts, OAuth scopes, logging and dynamic code use per
  operation.
- Run a contract test for metadata, auth, property resolution and every enabled
  action/trigger against provider sandboxes where possible.
- Use reserved concurrency and per-organization rate limits to contain both
  spend and provider throttling.
- Give the runner no AWS data permissions and no environment secrets.
- Never expose an operation that the capability scanner marks unsupported.
- Promote upstream changes through review, staging canaries and explicit lock
  file updates; never track `latest` or install at runtime.

This lightweight model cannot strongly restrict the runner's public internet
destinations without adding a VPC egress proxy/NAT path. Initially, compensate
with a small reviewed allowlist, no AWS credentials, bounded time/memory and
separate runner images for higher-risk pieces. Add controlled egress only when
the catalog size or compliance requirements justify the supporting services.

The published pieces are self-contained. `pieces-framework` is therefore a
development-only dependency used for type and compatibility fixtures, not a
production runtime dependency. At the pinned versions, a full development-tree
`npm audit` reports transitive advisories in framework features this runner
does not ship; `npm audit --omit=dev` reports zero production dependency
advisories. This reduces the deployed surface but does not replace review of
the code bundled inside each piece.

## Verification

Run the deterministic Go checks from the repository root:

```sh
go test ./integrations/...
go vet ./integrations/...
go test -race ./integrations/...
```

Run the real pinned-piece compatibility checks:

```sh
cd integrations/runtime
npm ci
npm run build
npm test
npm audit --omit=dev
```

The tests cover ciphertext tampering and cross-organization KMS binding,
admin/member permissions, catalog scope ceilings, hashed single-use OAuth
state, PKCE, wrong-provider callbacks, partial grants, credential-free domain
events, real HTTP token exchange shape, exact Slack raw-body signatures,
challenge replies, stale webhook timestamps, invalid signatures, replay,
inactive routes, SQS failure recovery, post-enqueue claim failures, member
`available|connected` projection, duplicate active-connection races and
transactional connection/outbox rollback, Cognito bearer extraction,
organization/member connection ownership, trigger listener identity,
cross-tenant trigger foreign keys, durable execution claims, Slack output
mapping, action key/input idempotency conflicts, failed-write no-retry
behavior, concurrent OAuth refresh leases, Google/GitHub provider exchange,
Microsoft authorization-code/refresh rotation and identity projection,
provider-neutral action projections, Graph callback validation and
`clientState` isolation, subscription compensation, renewal pagination,
durable disconnect/profile-disable cleanup, create-vs-disable serialization,
cross-replica renewal and migration exclusion, EventBridge publication and SQS
redelivery idempotency.

The combined mini test runs the HTTP registration and OAuth callback, member
availability isolation, webhook-route registration, trigger enable, signed
webhook admission, real Dynamo dedupe, real SQS transport, worker execution,
PostgreSQL execution claims, action replay without a second provider call, and
credential-free action/trigger outbox results:

```sh
INTEGRATIONS_TEST_DATABASE_URL='postgresql://oryxa:oryxa-dev-password@192.168.1.4:5435/oryxa?sslmode=disable' \
INTEGRATIONS_TEST_AWS_ENDPOINT=http://192.168.1.4:4566 \
  go test -count=1 -v ./integrations/internal/e2e
```

Exercise the durable connection lifecycle against PostgreSQL on the mini:

```sh
INTEGRATIONS_TEST_DATABASE_URL='postgresql://oryxa:oryxa-dev-password@192.168.1.4:5435/oryxa?sslmode=disable' \
  go test -count=1 -v ./integrations/internal/postgres
```

With LocalStack healthy on the mini, exercise real DynamoDB conditional writes
and SQS delivery from the development machine:

```sh
INTEGRATIONS_TEST_AWS_ENDPOINT=http://192.168.1.4:4566 \
  go test -count=1 -v \
  ./integrations/internal/dynamo ./integrations/internal/queue
```

## Next delivery phases

The implemented slices establish the control-plane, connection, action,
refresh, app-level webhook, and execution boundaries. The next work is staging
and trigger/provider expansion rather than another architecture spike.

### AWS staging

- Provision DynamoDB, KMS, SQS/DLQ, the Go broker and arm64 runner Lambda in
  `ap-south-1`.
- Put OAuth and webhook callback routes through Cloudflare without caching or
  browser challenges.
- Register a real Slack BYOC app and validate Cognito discovery, KMS binding,
  Lambda invocation, DLQ redrive and provider-side retries with non-production
  credentials.
- Register a Microsoft Entra multi-tenant BYOC app and validate Outlook,
  Calendar, and OneDrive delegated scopes plus Graph notification create,
  validation, renewal, delivery, and teardown using non-production accounts.
- Add alarms for queue age, DLQ depth, outbox lag and execution failures.

### Catalog and trigger expansion

- Add GitHub and Google webhook triggers only after their signature,
  installation/account routing, subscription lifecycle, and replay rules have
  provider-specific contract tests.
- Add EventBridge Scheduler only for approved polling triggers.
- Add S3 only when a supported operation actually requires file handling.
- Introduce Oryxa-managed one-click OAuth credentials only after BYOC staging
  is stable; existing connections are never silently moved between clients.

## Sources reviewed

- [Activepieces piece definitions](https://www.activepieces.com/docs/build-pieces/building-pieces/piece-definition)
- [Activepieces piece authentication](https://www.activepieces.com/docs/build-pieces/piece-reference/authentication)
- [Activepieces trigger model](https://www.activepieces.com/docs/build-pieces/piece-reference/triggers/overview)
- [Activepieces source and framework](https://github.com/activepieces/activepieces)
- [Activepieces repository license](https://github.com/activepieces/activepieces/blob/main/LICENSE)
- [AWS Lambda container images for Node.js](https://docs.aws.amazon.com/lambda/latest/dg/nodejs-image.html)
- [AWS Lambda with SQS](https://docs.aws.amazon.com/lambda/latest/dg/with-sqs.html)
- [DynamoDB condition expressions](https://docs.aws.amazon.com/amazondynamodb/latest/developerguide/Expressions.OperatorsAndFunctions.html)
- [DynamoDB TTL](https://docs.aws.amazon.com/amazondynamodb/latest/developerguide/TTL.html)
- [GitHub OAuth apps](https://docs.github.com/en/apps/oauth-apps/building-oauth-apps/authorizing-oauth-apps)
- [GitHub OAuth scopes](https://docs.github.com/en/apps/oauth-apps/building-oauth-apps/scopes-for-oauth-apps)
- [Google OAuth 2.0 web-server flow](https://developers.google.com/identity/protocols/oauth2/web-server)
- [Google OpenID Connect](https://developers.google.com/identity/openid-connect/openid-connect)
- [Gmail OAuth scopes](https://developers.google.com/workspace/gmail/api/auth/scopes)
- [Google Drive scopes](https://developers.google.com/workspace/drive/api/guides/api-specific-auth)
- [Microsoft identity platform authorization-code flow](https://learn.microsoft.com/en-us/entra/identity-platform/v2-oauth2-auth-code-flow)
- [Microsoft Graph create subscription](https://learn.microsoft.com/en-us/graph/api/subscription-post-subscriptions?view=graph-rest-1.0)
- [Microsoft Graph webhook delivery](https://learn.microsoft.com/en-us/graph/change-notifications-delivery-webhooks)
- [Microsoft Graph change notification resource](https://learn.microsoft.com/en-us/graph/api/resources/changenotification?view=graph-rest-1.0)
- [AWS KMS encryption-context controls](https://docs.aws.amazon.com/kms/latest/developerguide/conditions-kms.html)
