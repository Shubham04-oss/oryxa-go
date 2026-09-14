# Auth service

Auth owns Google sign-in, email/password credentials, verification and password-reset state, browser sessions, users, organizations, memberships, and invitations. Cognito stores and validates passwords; PostgreSQL is the system of record for auth-owned action tokens and application state. Every PostgreSQL state change and its domain event are written in the same transaction, then an outbox worker publishes the event to EventBridge with at-least-once delivery.

Cloudflare is the public edge. Route the public auth hostname to this service without exposing port 8080 directly. Keep Cloudflare rate limiting and bot protection enabled on `/v1/auth/login`, `/v1/auth/callback`, and `/v1/auth/refresh`.

## Lifecycle

1. `GET /v1/auth/login` creates a one-time PostgreSQL login flow and redirects to Google through Cognito with OAuth state, nonce, and PKCE.
2. Cognito authenticates with Google and returns an authorization code to `GET /v1/auth/callback`.
3. Auth consumes the login flow, exchanges the code, validates the Cognito ID token and nonce, upserts the local user, and sets HttpOnly access and refresh cookies.
4. `POST /v1/auth/refresh` rotates the Cognito session. `POST /v1/auth/logout` revokes the refresh token, clears cookies, and publishes a sign-out event.
5. Password signup stores the password only in Cognito, suppresses Cognito email, and disables the account until an auth-owned verification token is redeemed.
6. Verification and reset links are generated and consumed by auth. Their delivery-requested events are the email service's only responsibility: email renders and sends the message but never verifies an account or changes a password.
7. Users create organizations, invite members, accept invitations, change roles, leave or remove members, and update or delete organizations through authenticated endpoints.

Access JWTs are verified locally against Cognito's signed OIDC keys. The auth state tables store only SHA-256 token hashes. Delivery outbox records temporarily contain the action URL needed by email and are scrubbed immediately after successful EventBridge publication. Verification tokens expire after 24 hours; reset tokens expire after 30 minutes. Token redemption uses a PostgreSQL claim lease to prevent concurrent reuse and recover safely after a crashed worker. Cookie-backed and public credential mutations require an allowed browser `Origin`; bearer-token API calls remain supported for authenticated APIs.

## API

| Method | Path | Purpose |
| --- | --- | --- |
| `GET` | `/healthz` | Liveness |
| `GET` | `/v1/auth/config` | Public client configuration |
| `GET` | `/v1/auth/login?return_to=...` | Start Google sign-in |
| `GET` | `/v1/auth/callback` | Complete Cognito authorization-code flow |
| `POST` | `/v1/auth/refresh` | Refresh browser session |
| `POST` | `/v1/auth/logout` | Revoke and clear session |
| `POST` | `/v1/auth/password/signup` | Create a disabled password account and request verification delivery |
| `POST` | `/v1/auth/password/verify` | Verify email and enable the account |
| `POST` | `/v1/auth/password/resend-verification` | Replace and redeliver a verification link |
| `POST` | `/v1/auth/password/login` | Authenticate an enabled, verified password account |
| `POST` | `/v1/auth/password/forgot` | Request password-reset delivery without account enumeration |
| `POST` | `/v1/auth/password/reset` | Set a new Cognito password and revoke refresh sessions |
| `GET` | `/v1/auth/me` | Current user and organizations |
| `GET`, `POST` | `/v1/organizations` | List or create organizations |
| `GET`, `PATCH`, `DELETE` | `/v1/organizations/{organizationID}` | Read, update, or delete an organization |
| `GET` | `/v1/organizations/{organizationID}/members` | List members |
| `PATCH`, `DELETE` | `/v1/organizations/{organizationID}/members/{userID}` | Change a role or remove/leave |
| `GET`, `POST` | `/v1/organizations/{organizationID}/invitations` | List or create invitations |
| `DELETE` | `/v1/organizations/{organizationID}/invitations/{invitationID}` | Revoke an invitation |
| `POST` | `/v1/invitations/accept` | Accept an invitation token |

Owners can update/delete organizations and change any member role. Owners and admins can list invitations and invite/remove members; admins cannot manage owners/admins or create admin invitations. The final owner cannot be demoted or removed, including under concurrent requests.

Inviting and accepting answer `402 Payment Required` when the organization is at the member limit its plan allows; see [Member limits](#member-limits).

## Provision Cognito

Copy `.env.example` to the ignored `auth/.env`, provide a globally unique Cognito domain prefix, Google OAuth credentials, and the exact public callback/logout URLs. Authenticate the AWS SDK default credential chain and run:

```sh
go run ./auth/infra/cognito/cmd/provision
```

The repeatable provisioner creates or updates an Essentials-tier user pool in `ap-south-1`, enables deletion protection, registers Google, and creates a public client with authorization-code, password-auth, 15-minute access tokens, and refresh-token rotation support. Direct Cognito self-signup remains disabled: auth creates password users administratively with Cognito delivery suppressed. Add the provisioner's `google_redirect_uri` output to the Google OAuth client's authorized redirect URIs. Google credentials are provisioning inputs and must not be passed to the auth container.

## PostgreSQL and local runtime

The embedded migrations are in `internal/postgres/migrations/` and are applied in name order automatically with `AUTH_AUTO_MIGRATE=true`; each is safe to run again. To apply them explicitly to the existing mini container:

```sh
cat auth/internal/postgres/migrations/*.sql | docker --context mac-mini exec -i oryxa-go-postgres-1 \
  sh -c 'psql -v ON_ERROR_STOP=1 -U "$POSTGRES_USER" -d "$POSTGRES_DB"'
```

After filling `auth/.env`, build or start auth on the shared `oryxa-go_default` network:

```sh
docker --context mac-mini compose -f compose.auth.yaml build auth
docker --context mac-mini compose -f compose.auth.yaml up -d auth
```

`DATABASE_URL` should address `oryxa-go-postgres-1:5432`; `EVENTBRIDGE_ENDPOINT` should address `oryxa-go-eventbridge-1:4566` for the local EventBridge emulator.

### PostgreSQL client and extensions

Auth uses the module-private PostgreSQL boundary in `/internal/postgreskit`.
The process owns one native `pgxpool`, and its `sqlx` handle shares that pool
through pgx's `database/sql` adapter rather than opening a second set of
connections. Migrations use a transaction-scoped PostgreSQL advisory lock, so
multiple replicas can start with `AUTH_AUTO_MIGRATE=true` safely.

Startup discovers the installed versions of `ltree`, `pg_trgm`, pgvector and
Apache AGE. Their PostgreSQL extension names are `ltree`, `pg_trgm`, `vector`,
and `age` respectively. If pgvector is installed, its pgx codecs are registered
on every newly opened connection. The service does not install or upgrade
extensions itself.

Set the following after the database image or managed control plane contains
all four extensions:

```env
POSTGRES_REQUIRED_EXTENSIONS=ltree,pg_trgm,vector,age
```

This makes a missing capability a startup error. Product-name aliases such as
`pgvector`, `Apache AGE`, `apache_age`, and `apache-age` are normalized to their
SQL extension names. In particular, a pgvector PostgreSQL image does not imply
that Apache AGE is installed; the database image must bundle both when both are
required.

### Observability

Logs are structured JSON and carry `service`, `service_version`, and
`environment`. HTTP access logs use normalized Go route patterns and omit URL
queries, provider payloads, credentials, and forwarded client request IDs.
`/healthz` is excluded from access logs and server spans.

W3C trace context flows through the Cloudflare-fronted public HTTP endpoint.
The service records HTTP, AWS SDK, privacy-safe pgx, pool, and Go runtime
telemetry. Export uses OTLP/gRPC when `OTEL_EXPORTER_OTLP_ENDPOINT`,
`OTEL_EXPORTER_OTLP_TRACES_ENDPOINT`, or
`OTEL_EXPORTER_OTLP_METRICS_ENDPOINT` is set. Leave them unset for local
structured-logs-only mode. `DEPLOYMENT_ENVIRONMENT`, `LOG_LEVEL`,
`LOG_ADD_SOURCE`, and the standard OTLP TLS/header variables configure the
resource and transport.

The runtime AWS identity needs `cognito-idp:AdminCreateUser`, `AdminDeleteUser`, `AdminSetUserPassword`, `AdminDisableUser`, `AdminEnableUser`, `AdminGetUser`, `AdminUpdateUserAttributes`, and `AdminUserGlobalSignOut` on the configured user pool, and `sqs:ReceiveMessage` and `sqs:DeleteMessage` on its events queue. Production should use a task role. On the mini, provide short-lived AWS credentials through the ignored runtime environment; the LocalStack EventBridge and SQS clients use isolated dummy signing credentials internally, when `EVENTBRIDGE_ENDPOINT` and `SQS_ENDPOINT` are set, and do not replace the Cognito credentials.

## Member limits

Billing decides how many members each organization's plan allows and publishes it as `billing.entitlements.changed.v1`. Auth consumes those events from its own queue and keeps the latest revision per organization; a revision no newer than the stored one changes nothing.

- Inviting counts members and unexpired pending invitations, so the invitations out can't together exceed the limit.
- Accepting counts only members, since the invitation being accepted was counted when it was sent. A limit lowered in between refuses the acceptance and leaves the invitation pending.
- Both checks lock the organization first, so concurrent requests can't pass the limit together. Existing members are never removed when a limit drops.
- An organization billing has published nothing for, or whose plan sets no limit, has no limit. So does every organization when `AUTH_EVENTS_QUEUE_URL` is unset.

Provision the queue the entitlements arrive on, then set `AUTH_EVENTS_QUEUE_URL` to the printed `queue_url`:

```sh
go run ./auth/infra/events/cmd/provision
```

The command is repeatable. It creates or updates the `auth-events` queue with its dead-letter queue, and an `auth-billing-entitlements` rule on `EVENT_BUS_NAME` whose target is the queue and which the queue policy admits alone. Against the local emulators, run it with `AWS_ENDPOINT_URL=http://localhost:4566` and dummy credentials, and give auth `SQS_ENDPOINT=http://oryxa-go-eventbridge-1:4566`.

## Domain events

Auth publishes source `oryxa.auth` to `EVENT_BUS_NAME`. Contracts live in `contracts/events/auth/v1`:

- `auth.user.registered.v1`
- `auth.user.signed-in.v1`
- `auth.user.signed-out.v1`
- `auth.user.email-verification.delivery-requested.v1`
- `auth.user.email-verified.v1`
- `auth.user.password-reset.delivery-requested.v1`
- `auth.user.password-reset.v1`
- `auth.organization.created.v1`
- `auth.organization.updated.v1`
- `auth.organization.deleted.v1`
- `auth.member.invited.v1`
- `auth.member.invitation-revoked.v1`
- `auth.member.joined.v1`
- `auth.member.role-changed.v1`
- `auth.member.removed.v1`

EventBridge detail uses a stable envelope:

```json
{
  "event_id": "evt_...",
  "version": 1,
  "occurred_at": "2026-09-13T12:00:00Z",
  "data": {}
}
```

Consumers must deduplicate by `event_id`, because outbox delivery is at least once. No password, access token, refresh token, provider secret, or AWS credential is emitted.

Every ended membership emits `auth.member.removed.v1`, whose `reason` is `left`, `removed`, or `organization_deleted`: deleting an organization emits one for each member in the same transaction, so consumers never have to expand `auth.organization.deleted.v1` themselves. Membership events carry the member's `email` and the `organization_name`, which is enough to notify someone without reading auth's data.

Billing consumes `auth.organization.created.v1`, `auth.organization.updated.v1` and `auth.organization.deleted.v1`: it announces each new organization's first entitlements and cancels a deleted organization's subscription. See `billing/README.md`. Teams consumes those three and `auth.member.removed.v1`, so team data stays in step with the organizations it belongs to and members who leave an organization leave its teams. See `teams/README.md`.

Email consumes `auth.user.registered.v1` (welcome), the delivery requests `auth.user.email-verification.delivery-requested.v1`, `auth.user.password-reset.delivery-requested.v1` and `auth.member.invited.v1` (it sends the supplied action URL and never executes auth state transitions), and `auth.member.invitation-revoked.v1`, `auth.member.role-changed.v1` and `auth.member.removed.v1` (notices to the affected member). See `email/README.md`.
