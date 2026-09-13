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

## Provision Cognito

Copy `.env.example` to the ignored `auth/.env`, provide a globally unique Cognito domain prefix, Google OAuth credentials, and the exact public callback/logout URLs. Authenticate the AWS SDK default credential chain and run:

```sh
go run ./auth/infra/cognito/cmd/provision
```

The repeatable provisioner creates or updates an Essentials-tier user pool in `ap-south-1`, enables deletion protection, registers Google, and creates a public client with authorization-code, password-auth, 15-minute access tokens, and refresh-token rotation support. Direct Cognito self-signup remains disabled: auth creates password users administratively with Cognito delivery suppressed. Add the provisioner's `google_redirect_uri` output to the Google OAuth client's authorized redirect URIs. Google credentials are provisioning inputs and must not be passed to the auth container.

## PostgreSQL and local runtime

The embedded migration is at `internal/postgres/migrations/001_auth.sql` and can be applied automatically with `AUTH_AUTO_MIGRATE=true`. To apply it explicitly to the existing mini container:

```sh
docker --context mac-mini exec -i oryxa-go-postgres-1 \
  sh -c 'psql -v ON_ERROR_STOP=1 -U "$POSTGRES_USER" -d "$POSTGRES_DB"' \
  < auth/internal/postgres/migrations/001_auth.sql
```

After filling `auth/.env`, build or start auth on the shared `oryxa-go_default` network:

```sh
docker --context mac-mini compose -f compose.auth.yaml build auth
docker --context mac-mini compose -f compose.auth.yaml up -d auth
```

`DATABASE_URL` should address `oryxa-go-postgres-1:5432`; `EVENTBRIDGE_ENDPOINT` should address `oryxa-go-eventbridge-1:4566` for the local EventBridge emulator.

The runtime AWS identity needs `cognito-idp:AdminCreateUser`, `AdminDeleteUser`, `AdminSetUserPassword`, `AdminDisableUser`, `AdminEnableUser`, `AdminGetUser`, `AdminUpdateUserAttributes`, and `AdminUserGlobalSignOut` on the configured user pool. Production should use a task role. On the mini, provide short-lived AWS credentials through the ignored runtime environment; the LocalStack EventBridge client uses isolated dummy signing credentials internally and does not replace the Cognito credentials.

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

Email consumes `auth.user.registered.v1` (welcome), the delivery requests `auth.user.email-verification.delivery-requested.v1`, `auth.user.password-reset.delivery-requested.v1` and `auth.member.invited.v1` (it sends the supplied action URL and never executes auth state transitions), and `auth.member.invitation-revoked.v1`, `auth.member.role-changed.v1` and `auth.member.removed.v1` (notices to the affected member). See `email/README.md`.
