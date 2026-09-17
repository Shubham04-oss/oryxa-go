# Files service

Files owns object storage for an organization. Auth remains the identity and
organization/membership boundary. Teams remains the team graph: files forwards
the caller's credentials to teams when a file hangs on a team, and never copies
the tree. Billing and pipelines do not write to the bucket. They will attach later by file id and purpose.

S3 is the blob store. PostgreSQL is the source of truth for whether an object
exists, where it hangs, who created it, its purpose, checksum, and when it
expires. The files process is the only AWS principal that talks to the bucket.
Callers never receive credentials.

```text
app ── files HTTP ──▶ files ── PutObject/GetObject ──▶ S3
                         │
                         ├── GET teams /v1/organizations/{org}/teams/{team}
                         └── oryxa.files ──▶ EventBridge
auth ── organization created/updated/deleted, member removed ──▶ files-events queue
teams ── team deleted ──▶ files-events queue
```

## Placement and purpose

Privilege and placement are separate, as on teams. **Scope** is where the
object hangs. **Purpose** is why it exists.

| Scope | Who may create | Who may GET | List | Delete |
| --- | --- | --- | --- | --- |
| `member` (default) | Any org member; hangs under the caller | Owner or org admin | Owner's member files; org admin sees every member file | Owner or org admin |
| `team` | Anyone who can **see** the team | Anyone who can see the team | `?team_id=` after the same teams ACL | Creator, team `can_manage`, or org admin |
| `organization` | Org admin | Any org member, except `billing.attachment` (admin only) | Organization files; billing hidden unless admin | Org admin |

A caller who is not in the organization gets `404`, as auth conceals which
organizations exist. A team the caller cannot see is `404`, as teams conceals
it. A file the caller cannot GET or delete is also `404`. Creating an
organization file without being an org admin is `403`.

Keys follow placement, then the UTC calendar day of `created_at`, so a wipe
is still a prefix and a day's objects sit together:

```text
org/{organization_id}/member/{user_id}/{yyyy}/{mm}/{dd}/obj/{file_id}
org/{organization_id}/team/{team_id}/{yyyy}/{mm}/{dd}/obj/{file_id}
org/{organization_id}/organization/{yyyy}/{mm}/{dd}/obj/{file_id}
```

Objects are immutable after create. There is no overwrite of a key; a new
upload is a new `file_` id.

## Purposes

The purpose allowlist is closed so lifecycle and later pipelines cannot invent
a storage class:

| Purpose | Default retention | Notes |
| --- | --- | --- |
| `upload` | 30 days | Default |
| `pipeline.artifact` | 7 days | |
| `billing.attachment` | 90 days | Organization scope only |

Retention may be set between 1 and 90 days. The bucket lifecycle expires
everything under `org/` at 90 days as a backstop, and aborts abandoned
multipart uploads after one day.

Allowed content types: `application/octet-stream`, `application/json`,
`application/pdf`, `application/zip`, `text/plain`, `text/csv`,
`text/markdown`, `image/png`, `image/jpeg`, `image/gif`, `image/webp`. Size
limit is 8 MiB. Filenames are a single path segment, 1–255 characters.

## API

Cookie-authenticated mutations must come from an origin in `FILES_ALLOWED_ORIGINS`.

| Method | Path | Who | Purpose |
| --- | --- | --- | --- |
| `GET` | `/healthz` | Anyone | Liveness |
| `GET` | `/v1/organizations/{organizationID}/files` | Members | List. Query `scope`, `team_id`, `purpose`, `year`, `month`, `day` (UTC, from `created_at`; month needs year, day needs month). Default is the caller's member files plus organization files they may see. Team files need `team_id` |
| `POST` | `/v1/organizations/{organizationID}/files` | Members | Create. JSON `{filename, content_type, purpose, scope, team_id, retention_days, content}` with base64 `content`, or `multipart/form-data` with a `file` part. `scope` defaults to `member`. `team_id` is required when `scope` is `team` |
| `GET` | `/v1/organizations/{organizationID}/files/{fileID}` | Per placement | Metadata |
| `GET` | `/v1/organizations/{organizationID}/files/{fileID}/content` | Per placement | Bytes, through this service |
| `POST` | `/v1/organizations/{organizationID}/files/{fileID}/download-url` | Per placement | Short-lived presigned GET against S3 |
| `DELETE` | `/v1/organizations/{organizationID}/files/{fileID}` | Per placement | Delete metadata and the object |

Events carry ids, placement, purpose, size, and sha256 — never bytes, never
AWS credentials.

Team-scoped operations need `TEAMS_URL`. Without it, `scope=team` is invalid.

## Events

Files publishes source `oryxa.files` to `EVENT_BUS_NAME`. Contracts live in
`contracts/events/files/v1`.

| Detail type | When |
| --- | --- |
| `files.object.created.v1` | An object is stored |
| `files.object.deleted.v1` | An object is deleted |

`files.object.deleted.v1` `reason` is `deleted`, `organization_deleted`,
`organization_membership_ended`, or `team_deleted`.

Files consumes `auth.organization.created.v1`, `auth.organization.updated.v1`,
`auth.organization.deleted.v1`, `auth.member.removed.v1`, and
`teams.team.deleted.v1`. Organization deletion removes the `org/{id}/` prefix
from S3 and the organization's metadata. A member leaving the organization
removes **member-scoped** files under that user; team and organization files
they created stay. `auth.member.removed.v1` with `reason` `organization_deleted`
is ignored, because `auth.organization.deleted.v1` already owns that wipe.
Deleting a team removes that team's prefix and rows.

## Provision

Copy the root `.env.example` to the ignored `.env`. Its emulator block points
the AWS services files uses at the emulators in the root `docker-compose.yml`;
delete that block to provision AWS through the default credential chain. Then run:

```sh
(set -a && . ./.env && go run ./files/infra/storage/cmd/provision)
```

The command is repeatable. It creates or updates:

- the private `FILES_BUCKET_NAME` bucket (default `oryxa-files`) with public
  access blocked, AES-256 bucket encryption, object-ownership
  `BucketOwnerEnforced`, a 90-day expiry lifecycle, and abort of incomplete
  multipart uploads;
- the `files-events` queue with its dead-letter queue;
- a `files-auth-organizations` rule on `EVENT_BUS_NAME` whose target is the
  queue and which the queue policy admits alone. The pattern is auth
  organization events **or** `teams.team.deleted.v1`.

Put the printed `queue_url` in `FILES_EVENTS_QUEUE_URL`. LocalStack forgets
buckets and EventBridge state when it restarts, so re-run the provisioner
after one. After enabling S3 in `docker-compose.yml`, recreate that container
once (`docker compose up -d --force-recreate eventbridge`).

## Run locally

```sh
docker compose up -d
(set -a && . ./.env && go run ./files/infra/storage/cmd/provision)
(set -a && . ./.env && ORYXA_SERVICES=files go run ./cmd/oryxa)
```

Schemas install automatically at start unless `ORYXA_AUTO_MIGRATE=false`; install them ahead of time with `go run ./cmd/oryxa migrate`. Files deploys as part of the single `oryxa` image, alongside whichever other services `ORYXA_SERVICES` names.

Logs are structured JSON from `/internal/observability`. HTTP access logs skip
`/healthz`. The simulated client journey includes files at `/test`
(`./test/run.sh`).

The store has its own tests against PostgreSQL, and the blob adapter against
LocalStack S3, which the unit test run skips:

```sh
FILES_TEST_DATABASE_URL='postgres://oryxa:oryxa-dev-password@localhost:5435/oryxa?sslmode=disable' go test ./files/internal/postgres
FILES_TEST_AWS_ENDPOINT=http://localhost:4566 go test ./files/internal/blob
```

## Deploy

Files deploys as part of the single `oryxa` image, behind the same public edge as every other service it runs alongside.
Its runtime identity needs `s3:PutObject`, `s3:GetObject`, `s3:DeleteObject`,
`s3:ListBucket`, `s3:AbortMultipartUpload` on the files bucket,
`events:PutEvents` on the event bus, and `sqs:ReceiveMessage` /
`sqs:DeleteMessage` on `files-events`. The piece-runner role still gets logs
only.
