# Teams service

Teams owns teams inside an organization and who is on them. Auth remains the identity and organization/membership boundary; billing remains subscriptions and entitlements; email remains delivery; Teams never verifies a token itself: it forwards the caller's bearer token or session cookie to auth and acts on the membership auth returns.

Services talk through EventBridge. Teams publishes source `oryxa.teams` on `EVENT_BUS_NAME` in the same envelope every Oryxa publisher uses, and consumes what it needs from its own `teams-events` queue. Other services never read teams' data. Consumers must deduplicate by `event_id`, because outbox delivery is at least once.

```text
app ── teams CRUD ──▶ teams ── oryxa.teams ──▶ EventBridge ──▶ each consumer's own queue
                         ▲
auth ── organization created/updated/deleted, member removed ──▶ teams-events queue
```

## Control

Organization admin (auth owner or admin) and team admin are the same control. Either may update or delete the team, add members, change roles, remove others, and nest a team under it. Creating a top-level team is organization-admin only, because there is no team admin yet. The creator becomes the team's first admin.

| Who | Create team | See team | Manage team and members | Leave |
| --- | --- | --- | --- | --- |
| Organization owner or admin | Yes, including nested teams | Every team in the org | Yes | If on the team |
| Team admin | Nested team under theirs | Their team | Yes — same as an organization admin, on that team | Yes |
| Team member | No | Their team | No | Yes |
| Other organization member | No | No (404) | No | No |

The last team admin cannot be demoted or leave. Organization admins who are not on the team can still manage it, and can add another admin first. Adding a member requires they already belong to the organization.

A caller who is not in the organization gets `404`, as auth conceals which organizations exist.

"Organization owner or admin" above means a person's own session, such as a
chat they are watching. A token auth minted for an agent, which it answers
with `agent: true`, gets a plain member's reach whatever its member's role,
because the agent can be answering someone else entirely: a teammate's
interaction request, a room it was mentioned in. It sees and lists only the
teams its member is actually on, `can_manage` is true only where its member
is that team's own admin, and it creates no top-level team and makes no
admin-only write on a team its member merely oversees as an organization
admin. It still manages a team its member is genuinely that team's admin of,
the same as a plain member holding that role would. Files and rag ask teams
for the same team over an agent's credentials and additionally re-check the
answer themselves, in case a caller ever reaches them some other way.

## Tools

The point of a team here is that an AI can call it. There are three tools. Organization admins can open every team; everyone else only the teams they belong to — and that "everyone else" includes an agent calling with a token minted for an organization-admin member, per [Control](#control) above. A team the caller cannot see is `404`, same concealment as the rest of teams.

| Tool | Arguments | Result |
| --- | --- | --- |
| `list_teams` | none | Teams the caller can see: `id`, `name`, `path` |
| `call_team` | `query` — name first, then path, then team id | That team (`id`, `organization_id`, `name`, `path`) |
| `call_team_members` | `query` — same lookup | That team's people: `user_id`, email, display name, role |

```
GET  /v1/organizations/{organizationID}/tools
POST /v1/organizations/{organizationID}/tools/{name}
```

`GET` is the catalog (`name`, `description`, `parameters`). `POST` is the call; `list_teams` takes an empty body.

```json
{
  "team_id": "team_…",
  "name": "Engineering",
  "members": [
    { "user_id": "user-1", "email": "asha@example.com", "display_name": "Asha", "role": "admin" }
  ]
}
```

Each call goes on EventBridge (`oryxa.teams`) through the outbox. Calling a team or its members emits the team event and one `teams.member.pinged.v1` for every member. Listing does the same for every member of every listed team. Consumers that need to notify people subscribe to the ping — they never have to expand the team themselves.

These tools are reach-only. Creating, renaming, and changing membership stay on the HTTP API below.

## Database

Teams uses `/internal/postgreskit`: one instrumented native `pgxpool`, with sqlx sharing that pool. Local Postgres is `pgvector/pgvector:0.8.6-pg17` in the root `docker-compose.yml` — not alpine — so the extension files exist. Recreate the container after that image change (`docker compose up -d postgres`). A data volume from alpine 17 can stay; `CREATE EXTENSION` runs from teams migrations, and from `infra/postgres/init` on a fresh volume. Set `POSTGRES_REQUIRED_EXTENSIONS=ltree,pg_trgm,vector` to fail startup when those capabilities are missing.

| Extension | Where | Used for |
| --- | --- | --- |
| `ltree` | `teams.path`, `teams_users.path`, `teams_memberships.path` (not null, unique btree, GiST with `organization_id`) | Hierarchy. Teams: org label, then team label; nested teams append. Users: org label, then user label (flat). Members: the team's path, then user label — so people on nested teams sit under the parent (`organization_id = $org AND path <@ team.path`). |
| `pg_trgm` | GIN on `(organization_id, name)` (`btree_gin` + `gin_trgm_ops`) | Tenant-scoped name similarity (`%`, `similarity`, `ILIKE`) |
| `vector` | optional `teams.embedding vector(1536)` | Cosine nearest-neighbour (`<=>`) on a partial HNSW index (`vector_cosine_ops`). IVFFlat is not used. |
| `btree_gist` / `btree_gin` | with `organization_id` on the GiST and GIN indexes above | Keep tenant equality in the same index as path / name |

`LtreeLabel` keeps only `[A-Za-z0-9_]`. Create a nested team with `parent_id`; its `path` is the parent's path plus its own label. Deleting a team deletes its descendants in the same transaction. Name search uses `GET …/teams/search?q=`. People search uses `GET …/members/search?q=` against the org roster from auth; each hit has the person's org `path` and the visible teams they are on (each with the team `path` and `member_path`). The directory endpoint returns the complete current organization roster, but only the team tree and team links the caller may see; it is a read-only snapshot and emits no call or ping event. `GET …/teams/{id}/members/descendants` is everyone hanging under that team, including nested teams, still concealed to what the caller can see. Nearest-neighbour uses a 1536-wide embedding written with `PUT …/embedding` and queried with `POST …/teams/nearest`. Writes of any other width are rejected. The lookup uses HNSW with iterative scans so the organization and visibility filters still return enough neighbours. Search, descendants and nearest only return teams the caller can see.

## API

| Method | Path | Who | Purpose |
| --- | --- | --- | --- |
| `GET` | `/healthz` | Anyone | Liveness |
| `GET` | `/v1/organizations/{organizationID}/tools` | Members | Tool catalog: `list_teams`, `call_team`, `call_team_members` |
| `POST` | `/v1/organizations/{organizationID}/tools/{name}` | Members | Call one of those three tools |
| `GET` | `/v1/organizations/{organizationID}/teams` | Members | Organization admins see every team; others see teams they belong to |
| `GET` | `/v1/organizations/{organizationID}/directory` | Members | Current organization roster plus the team tree and team memberships visible to the caller |
| `GET` | `/v1/organizations/{organizationID}/members/search` | Members | Fuzzy people search: `?q=` required (at most 256 characters), `limit` optional. Each hit includes org `path` and `teams` (empty if they are on none the caller can see) |
| `GET` | `/v1/organizations/{organizationID}/teams/search` | Members | Name search: `?q=` required (at most 256 characters, since what a search costs grows with its length), `limit` optional (default 20, max 100) |
| `POST` | `/v1/organizations/{organizationID}/teams/nearest` | Members | Nearest teams: `{"embedding":[…1536…],"limit":20}` |
| `POST` | `/v1/organizations/{organizationID}/teams` | Organization admins; parent team admin when nesting | Create a team: `{"name": "Engineering"}` or `{"name": "Frontend", "parent_id": "team_…"}`; the caller becomes team admin |
| `GET` | `/v1/organizations/{organizationID}/teams/{teamID}` | Organization admins or team members | Team, the caller's membership, and `can_manage` |
| `GET` | `/v1/organizations/{organizationID}/teams/{teamID}/descendants` | Organization admins or team members | The team and visible teams under it |
| `PUT` | `/v1/organizations/{organizationID}/teams/{teamID}/embedding` | Organization or team admin | Store a 1536-wide embedding: `{"embedding":[…]}` |
| `PATCH` | `/v1/organizations/{organizationID}/teams/{teamID}` | Organization or team admin | Rename: `{"name": "Platform"}` |
| `DELETE` | `/v1/organizations/{organizationID}/teams/{teamID}` | Organization or team admin | Delete the team and its descendants |
| `PATCH` | `/v1/organizations/{organizationID}/teams/{teamID}/notification` | Team members | The caller's email switch for team-call notices: `{"notify_email": false}` |
| `GET` | `/v1/organizations/{organizationID}/teams/{teamID}/members` | Organization admins or team members | List members of that team |
| `GET` | `/v1/organizations/{organizationID}/teams/{teamID}/members/descendants` | Organization admins or team members | Members of the team and visible nested teams |
| `POST` | `/v1/organizations/{organizationID}/teams/{teamID}/members` | Organization or team admin | Add an organization member: `{"user_id": "...", "role": "member"}` |
| `PATCH` | `/v1/organizations/{organizationID}/teams/{teamID}/members/{userID}` | Organization or team admin | Change role: `{"role": "admin"}` |
| `DELETE` | `/v1/organizations/{organizationID}/teams/{teamID}/members/{userID}` | Self, or organization or team admin | Leave or remove |

Cookie-authenticated mutations must come from an origin in `TEAMS_ALLOWED_ORIGINS`. Team names are unique within an organization, 1–120 characters.

## Events

Teams publishes source `oryxa.teams` to `EVENT_BUS_NAME`. Contracts live in `contracts/events/teams/v1`. EventBridge detail uses the stable envelope:

```json
{
  "event_id": "evt_...",
  "version": 1,
  "occurred_at": "2026-09-13T12:00:00Z",
  "data": {}
}
```

| Detail type | When |
| --- | --- |
| `teams.team.created.v1` | A team is created |
| `teams.team.updated.v1` | A team is renamed |
| `teams.team.deleted.v1` | A team is deleted, or its organization is deleted |
| `teams.member.added.v1` | Someone is added, including the creator |
| `teams.member.role-changed.v1` | A team role changes |
| `teams.member.removed.v1` | A membership ends |
| `teams.team.called.v1` | An AI called a team (`call_team`) |
| `teams.team.members-called.v1` | An AI called a team's members (`call_team_members`) |
| `teams.teams.listed.v1` | An AI listed teams (`list_teams`) |
| `teams.member.pinged.v1` | One member was reached by a tool call |

`teams.member.removed.v1` `reason` is `left`, `removed`, `team_deleted`, `organization_deleted`, or `organization_membership_ended`. Deleting a team emits one removal for each member in the same transaction. Deleting an organization does the same for every team, so consumers never have to expand `teams.team.deleted.v1` or `auth.organization.deleted.v1` themselves.

A tool call writes the parent event and one `teams.member.pinged.v1` per affected member in the same transaction. `call_team` and `call_team_members` ping that team's members. `list_teams` pings the members of every listed team. The ping carries `user_id`, `email`, `role`, `tool`, and `notify_email`.

`notify_email` is the member's switch, on by default when they join. Only that member can flip it. Email (`/email`) is the delivery service: it sends `team-called` for `call_team` and `call_team_members` when `notify_email` is true, and skips the rest — listing, a switch that is off, or the person who ran the tool. EventBridge still pings everyone; the switch is only for mail.

No password, access token, refresh token, provider secret, or AWS credential is emitted.

Teams consumes `auth.organization.created.v1`, `auth.organization.updated.v1`, `auth.organization.deleted.v1` and `auth.member.removed.v1`. Organization deletion wipes the organization's teams. A member leaving or being removed from the organization is taken off every team they were on; `auth.member.removed.v1` with `reason` `organization_deleted` is ignored, because `auth.organization.deleted.v1` already owns that wipe.

To consume another event: add it to `AuthSubscriptions` and `NewConsumer` in `internal/events`, give `internal/application` a method for it, and re-run the provisioner so the rule matches it.

## Provision

Copy the root `.env.example` to the ignored `.env`. Its emulator block points the AWS services teams uses at the emulators in the root `docker-compose.yml`; remove it to provision AWS through the default credential chain. Then run:

```sh
(set -a && . ./.env && go run ./teams/infra/events/cmd/provision)
```

The command is repeatable. It creates or updates the `teams-events` queue with its dead-letter queue, and a `teams-auth-organizations` rule on `EVENT_BUS_NAME` whose target is the queue and which the queue policy admits alone. Put the printed `queue_url` in `TEAMS_EVENTS_QUEUE_URL`.

The embedded migrations are in `internal/postgres/migrations/` and are applied in name order when `oryxa` starts, unless `ORYXA_AUTO_MIGRATE=false`, or ahead of time with `go run ./cmd/oryxa migrate`. The emulators forget EventBridge state when they restart, so re-run the provisioner after one.

## Run locally

```sh
docker compose up -d
(set -a && . ./.env && go run ./teams/infra/events/cmd/provision)
(set -a && . ./.env && ORYXA_SERVICES=teams go run ./cmd/oryxa)
```

Logs are structured JSON from `/internal/observability`. HTTP access logs skip `/healthz`. AWS SDK and pgx spans export over OTLP/gRPC when `OTEL_EXPORTER_OTLP_ENDPOINT` (or the signal-specific endpoints) is set.

The simulated client journey for teams together with files and email is `/test` (`./test/run.sh`). It is k6 against one `oryxa` process running teams, files and email.

Without `TEAMS_EVENTS_QUEUE_URL` teams still starts, but it will not hear about organizations. The store has its own tests against PostgreSQL, which the unit test run skips:

```sh
TEAMS_TEST_DATABASE_URL='postgres://oryxa:oryxa-dev-password@localhost:5435/oryxa?sslmode=disable' go test ./teams/internal/postgres
```

## Deploy

Teams deploys as part of the single `oryxa` image, behind the same public edge as every service it runs alongside. Its runtime identity needs `events:PutEvents` on the event bus and `sqs:ReceiveMessage` and `sqs:DeleteMessage` on `teams-events`.
