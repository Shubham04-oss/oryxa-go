# Realtime service

The web app keeps direct messages and teamspace side conversations only in its Worker's Durable Objects. No Go service holds a copy. Realtime is the platform's way to them, in both directions:

- **Purges.** When a member leaves an organization, or the organization is deleted, those conversations must go, and no Go service can delete them. So must a member's side conversations in a teamspace when they leave its team, and all of a teamspace's when it is deleted. Realtime hears the first two from auth and the last two from agents, on its own queue, and turns each into one signed call that has the Worker purge them. Each purge done is recorded as `realtime.purge.completed.v1`.
- **Events.** The Worker tells realtime what happens in those conversations, a message posted or a conversation read, on `POST /v1/realtime/events`. Realtime publishes each on the bus as `oryxa.realtime`, so other services can react. As agents' request events do, they carry identities and lifecycle state, never what was said: no bodies, no previews, no titles.

Realtime is off until it is configured. Once on, it keeps its outbox, and the ids of the Worker's recent events, in PostgreSQL.

```text
auth ──── auth.member.removed.v1, auth.organization.deleted.v1 ──────────────┐
                                                                             ├──▶ realtime-auth-memberships rule
agents ── agents.teamspace.member.removed.v1, agents.teamspace.deleted.v1 ───┘
                                                                                             │
                                                                          realtime-events queue (dead letters: realtime-events-dlq)
                                                                                             │
                                                                                         realtime ── POST /internal/realtime/purge ──▶ Worker
                                                                                           ▲  │
                                                                POST /v1/realtime/events ──┘  └──▶ realtime_outbox ──▶ the bus, as oryxa.realtime
                                                                      (from the Worker)
```

## The purge

Realtime makes the call. The Worker serves it. Both sides implement exactly this:

```http
POST {REALTIME_WORKER_URL}/internal/realtime/purge
Content-Type: application/json
X-Oryxa-Timestamp: 1790208000
X-Oryxa-Signature: v1=ee12ee93bb00fb5e9901c4a0ac8ed193a85539bfb2c76241f9182f2fcc71b90e

{"event_id":"evt_Zx8rP0kQ3mB7tY2wN5cV1A","organization_id":"org_2fQx9LmR7vTk4WcA","scope":"member","user_id":"6c1d5f0e-2b7a-4c8e-9f3d-1a2b3c4d5e6f","removed_at":"2026-09-24T10:00:00Z"}
```

- **Body:** compact JSON, in this order, with five keys always present, and `teamspace_id` exactly when the scope is a teamspace's:
  - `event_id` is the id of the event that asked for the purge (the envelope's `event_id`): auth's for a member's or an organization's, agents' for a teamspace's. It stays the same however many times the event is delivered or published.
  - `organization_id` is the organization.
  - `scope` is `member`, `organization`, `teamspace_member` or `teamspace`. The scope is always stated, so an empty `user_id` can never mean the whole organization, or the whole teamspace, by accident.
  - `user_id` is the member whose conversations go. It is `""` when `scope` is `organization` or `teamspace`.
  - `teamspace_id` is the teamspace whose side conversations go, for `teamspace_member` and `teamspace`, and absent for the other two, so a member's or an organization's body is exactly what it was before teamspaces had purges.
  - `removed_at` is when the membership ended (the event's `removed_at`, auth's or agents'), when the organization was deleted (its `deleted_at`), or when the teamspace was (its `changed_at`). It is written in UTC, in RFC 3339, with as many digits of a fraction of a second as the event had, up to nine. Auth's and Teams' timestamps have them, so most real bodies carry a fraction. It has none when the time is a whole second. In a member's purge, in the organization or a teamspace, the Worker deletes only what was written at or before `removed_at`, so a purge that runs late, after a member was invited back, leaves what they wrote since.
- **`X-Oryxa-Timestamp`:** when this attempt was signed, in Unix seconds. Every retry is signed afresh, so the Worker can refuse one signed too long ago. The Worker's side allows five minutes.
- **`X-Oryxa-Signature`:** `v1=` followed by the lowercase hex HMAC-SHA256 of the timestamp in decimal, a `.`, and the body exactly as sent. The key is `REALTIME_PURGE_SECRET`'s characters as written. It is not decoded from hex or base64, even when it looks like hex.
- **User-Agent:** `oryxa-realtime/1`. Redirects are not followed.

What each scope takes:

| Scope | `user_id` | `teamspace_id` | What the Worker purges |
| --- | --- | --- | --- |
| `member` | the member | absent | The member's conversations in the organization, up to `removed_at` |
| `organization` | `""` | absent | Everything the organization kept |
| `teamspace_member` | the member | the teamspace | The side conversations in that teamspace the member is part of, by the member's rule: up to `removed_at`, and what they wrote after it through a connection opened before it |
| `teamspace` | `""` | the teamspace | Every side conversation in that teamspace, entirely, as for an organization |

| The Worker answers | Realtime |
| --- | --- |
| `204` | Done. Realtime records `realtime.purge.completed.v1` (see [Events realtime publishes](#events-realtime-publishes)), and the message is deleted. If the record fails, the purge is still done, and it is logged as `realtime purge done but not recorded`. The message is kept, so the queue delivers it again: the purge is sent again, which the Worker answers `204` again, and recorded then. |
| `400` or `413` carrying the Worker's own refusal, `{"error":{"code":…}}` | The Worker refusing the body for good: it cannot be read as a purge, or it is over 4 KB. It is logged as `realtime purge dropped`, with the status, the start of the Worker's answer and the purge's fields, and the message is deleted. |
| `401` | A bad signature, which comes from the secret or the clock, not from this purge. It is logged, and the message is kept for the queue to deliver again, so it reaches the dead-letter queue and can be redriven once the secret is fixed. |
| `408`, `429` | The Worker asking for later. Kept and retried. |
| Any other `4xx`, including a `400` or `413` without the Worker's refusal | Not the Worker refusing this purge. A `403` is usually Cloudflare challenging the call, and a `404` or `405` a URL that does not reach the Worker's route, or a Worker without it. Kept and retried, so it reaches the dead-letter queue, whose alarm mails, and can be redriven once that is put right. |
| `5xx`, anything else, no answer in time, or no connection | Kept and retried. A member's purge has 30 seconds, and an organization's 120, since the Worker answers that only once it has purged every member. A teamspace member's has a member's 30, and a teamspace's an organization's 120, since it too visits every member. |

A message that keeps failing is delivered again every fifteen minutes, which outlasts a batch of five organizations' purges the Worker never answers. After five receives, about 75 minutes, it moves to `realtime-events-dlq`, which keeps it for 14 days. The purge is idempotent, so a repeat is always safe.

### The purge's golden vectors

The Worker's verification can be tested against these. `internal/purge/purge_test.go` pins them, and `openssl dgst -sha256 -hmac "$secret"` over `"$timestamp.$body"` reproduces them.

| | |
| --- | --- |
| Secret | `5f2b8e1c9a7d4036b1e8c2f94a6d0e3b7c1f5a9d2e8b4c6f0a3d7e1b9c5f2a84` (64 characters, used as they are) |
| Timestamp | `1790208000` (2026-09-24T00:00:00Z) |
| Member body | `{"event_id":"evt_Zx8rP0kQ3mB7tY2wN5cV1A","organization_id":"org_2fQx9LmR7vTk4WcA","scope":"member","user_id":"6c1d5f0e-2b7a-4c8e-9f3d-1a2b3c4d5e6f","removed_at":"2026-09-24T10:00:00Z"}` |
| Its signature | `v1=ee12ee93bb00fb5e9901c4a0ac8ed193a85539bfb2c76241f9182f2fcc71b90e` |
| Organization body | `{"event_id":"evt_Qm4sT8vY1bN6cX3zL0pK7g","organization_id":"org_2fQx9LmR7vTk4WcA","scope":"organization","user_id":"","removed_at":"2026-09-24T10:00:00Z"}` |
| Its signature | `v1=ec0202dfc34ba5b946ce5e384b187d7358b3609549a049b3b3bfb51ef73d2177` |
| A real event's shape: the member body with a fraction of a second | `{"event_id":"evt_Zx8rP0kQ3mB7tY2wN5cV1A","organization_id":"org_2fQx9LmR7vTk4WcA","scope":"member","user_id":"6c1d5f0e-2b7a-4c8e-9f3d-1a2b3c4d5e6f","removed_at":"2026-09-24T10:00:00.123456789Z"}` |
| Its signature | `v1=0828ea12f32328f23603c3f910c57474459a38a6fe79fce8afcfa12bf5780548` |
| Teamspace member body | `{"event_id":"evt_Tm5sPq2Lx8vB3nK7cR1dWa","organization_id":"org_2fQx9LmR7vTk4WcA","scope":"teamspace_member","user_id":"6c1d5f0e-2b7a-4c8e-9f3d-1a2b3c4d5e6f","teamspace_id":"tsp_JixqQoilaC2tgma5jqRHtg","removed_at":"2026-09-24T10:00:00Z"}` |
| Its signature | `v1=3620d1cb2086b41144ba9ff1ca76fb29ddd528bbd377751c6501db56a0b6ef1a` |
| Teamspace body | `{"event_id":"evt_Ts9kW2mQ4xN7vB1cL5pR8e","organization_id":"org_2fQx9LmR7vTk4WcA","scope":"teamspace","user_id":"","teamspace_id":"tsp_JixqQoilaC2tgma5jqRHtg","removed_at":"2026-09-24T10:00:00Z"}` |
| Its signature | `v1=78cbbeee18c9c16fd29dbc722a730faf37526bed853126b8d8d03817983334ec` |

The Worker's side agreed on the first two vectors. The third is here so that the Worker's parsing of `removed_at` can be tested on what auth actually sends. JavaScript's `Date.parse` reads it, to the millisecond. The last two are the teamspace purges', pinned in the contract both sides build them from. The member and organization bodies did not change when they were added.

## What starts a purge

| Detail type | Purge |
| --- | --- |
| `auth.member.removed.v1`, `reason` `left`, `removed` or `organization_deleted` | That member's conversations in that organization: `scope` `member`, their `user_id`, and `removed_at` from the event's `removed_at` |
| `auth.organization.deleted.v1` | Everything the organization kept: `scope` `organization`, `user_id` `""`, and `removed_at` from the event's `deleted_at` |
| `agents.teamspace.member.removed.v1` | That member's side conversations in that teamspace: `scope` `teamspace_member`, their `user_id`, the event's `teamspace_id`, and `removed_at` from the event's `removed_at`, which is when Teams ended their place on the teamspace's team |
| `agents.teamspace.deleted.v1` | Every side conversation in that teamspace: `scope` `teamspace`, `user_id` `""`, the event's `teamspace_id`, and `removed_at` from the event's `changed_at` |

Deleting an organization removes every member, each with an event of their own, and each of those is purged too, although the organization's purge takes everything it kept. The organization's purge reaches only the conversations the Worker's registry knows of, and nothing guarantees that is all of them. A member's purge that finds nothing left is still answered `204`.

A teamspace is bound to one team, so leaving the team is leaving the teamspace. Agents says so whenever Teams removes someone from a team that has a teamspace, whatever the reason: leaving it, being removed, the team or the organization deleted, or leaving the organization. Deleting a team deletes its teamspace, so it asks for both kinds of purge, a teamspace member's for each member and the teamspace's, in no set order. Once the teamspace's has run, a teamspace member's finds nothing left, and is answered `204`. A Teams event delivered again has agents say it again, under a new `event_id` but with the same `removed_at`, so the Worker must take a teamspace member's purge it has already done, under another id, as a repeat too. A teamspace's id is never used again, so its purge takes everything in it, whenever it runs.

The envelope's signature is checked before anything in an event is used, as in every consumer (`internal/eventauth`), against the key of the service its source names: auth's for `oryxa.auth`, agents' for `oryxa.agents`. The host holds a key for every service in the process, so agents' events verify as auth's do. By default a signature that does not check out is logged and the event handled anyway (`ORYXA_EVENT_SIGNATURES=report`). With `enforce` it is refused. So with report-only signatures, the platform's default, an event forged onto the bus would still be acted on, and its purge sent. That is the platform's current stance on signatures, not realtime's alone: `enforce` waits for every service to sign with a KMS key.

Realtime accepts a signature up to 21 days old, where other consumers accept one a day old. A purge can wait on its queue and dead-letter queue for the 14 days they keep it, and then be replayed from the bus's archive, which keeps a week, and under `enforce` it must still verify. A purge acted on late is only a repeat.

An event realtime cannot read, meaning one that is not JSON, not a type realtime subscribes to from its source, not envelope version 1, missing its organization, member, teamspace or time, or refused for its signature, stays on the queue and ends in the dead-letter queue, as in every consumer. Each source's types are taken only from that source: `agents.teamspace.deleted.v1` from `oryxa.auth` is refused.

A `removed_at`, `deleted_at` or `changed_at` more than five minutes after realtime received the event is a removal that has not happened. Purged up to it, a member would lose what they wrote after being invited back, so its purge is never sent. It is dropped, and logged as `realtime purge dropped` with the purge's fields. Five minutes allows for the publisher's clock and realtime's disagreeing.

A purge the Worker has done is recorded as `realtime.purge.completed.v1` before its message is deleted. Its `event_id` comes from the event that asked for the purge, so a purge delivered and done twice records one event while the outbox keeps the first.

Realtime remembers the last 1,024 events it purged and recorded. A message delivered again after that is then deleted without a second call. This only saves calls. It does not guarantee anything, because it forgets on restart. The Worker's idempotence is the guarantee.

To consume another event: add it to `Subscriptions`, under its source, and `requestFor` in `internal/events`, and re-run the provisioner so the rule matches it.

## The Worker's events

The Worker makes this call, and realtime serves it. The Worker's side is in web-next's `REALTIME.md`. Both sides implement exactly this:

```http
POST {ORYXA_API_ORIGIN}/v1/realtime/events
Content-Type: application/json
X-Oryxa-Timestamp: 1790208000
X-Oryxa-Signature: v1=75442e7357021586eb3717f2c97b091546d26cc31bcfdecafe571f36e7836cfa

{"events":[{"id":"evt_golden_posted_1","type":"realtime.message.posted.v1","detail":{"organization_id":"org_1","conversation":{"kind":"direct","id":"org_1~user-a~user-b"},"participant_user_ids":["user-a","user-b"],"message":{"id":"msg_1","seq":1},"author":{"kind":"member","user_id":"user-a"},"posted_at":"2026-09-24T10:00:00.000Z"}}]}
```

- **Where:** `ORYXA_API_ORIGIN` is the API's public origin. `app.oryxa.in` sends `/v1/*` to the tasks through the tunnel, as `api.oryxa.in` sends everything.
- **`X-Oryxa-Signature`:** `v1=` followed by the lowercase hex HMAC-SHA256 of `events.`, the timestamp exactly as sent, a `.`, and the body exactly as sent. The key is `REALTIME_PURGE_SECRET`, the purge's own secret, trimmed of surrounding whitespace on both sides and otherwise used as written. The `events.` prefix keeps the two directions apart: a purge's string starts with its timestamp, a digit, so a purge's signature never verifies here, and this one never verifies as a purge.
- **`X-Oryxa-Timestamp`:** when the call was signed, in Unix seconds, digits only. Realtime takes it within 300 seconds of its own clock, either way, both ends included. Each header appears once.
- **Body:** at most 256 KiB, holding 1 to 100 events: `{"events":[{"id":…,"type":…,"detail":{…}},…]}`.
  - `id` is 1 to 200 of `A-Z a-z 0-9 _ -`. The Worker makes it once, when it records the event, and keeps it across retries. Realtime deduplicates on the id alone, so it must be unique across every conversation, not only within one object.
  - `type` is `realtime.message.posted.v1` or `realtime.conversation.read.v1`, and nothing else: a completed purge is realtime's own to say.
  - `detail` is the event's payload, as [Events realtime publishes](#events-realtime-publishes) shows it.

Realtime checks the size, as the body declares it, and the signature's headers before it reads the body, reads at most 256 KiB, and checks the signature over the body before anything reads it. It then checks every event, records them all in its outbox in one transaction, and answers `204`. An event whose id it accepted in the last seven days is acknowledged again without a second event.

In each event's detail it checks:

- `conversation.kind` is `direct` or `side`, and `conversation.id` is the Durable Object's name: `{organization}~{person}~{person}` for a direct conversation, `{organization}~{teamspace}~{person}~{person}` for a side one. Each id in it is 1 to 128 of `A-Z a-z 0-9 _ -`, and the two people are different and sorted, by byte, as the web app builds the name.
- `organization_id` is the name's first id. `conversation.teamspace_id` is present exactly for a side conversation, and is the name's second id.
- For a message: `participant_user_ids` is the name's two people, in its order; `message.id` is 1 to 200 printable ASCII characters, without spaces; `message.seq` is at least 1; `author.kind` is `member` or `agent`; `author.user_id` is one of the two people, for an agent the one it belongs to; and `author.agent_id` is present exactly for an agent, as `message.id` is written.
- For a read: `reader_user_id` is one of the two people, and `read_through_seq` is at least 1.
- `posted_at` and `read_at` are RFC 3339 in UTC, ending in `Z`, with up to nine digits of a fraction of a second, or none. The Worker writes three (`Date.prototype.toISOString()`). They are from 2020 on, and no more than a day ahead of realtime's clock.

Fields the contract does not name are ignored, so either side can deploy first. They are never published either: each event is rewritten from the contract's own Go types before it is recorded.

| Realtime answers | When | The Worker then |
| --- | --- | --- |
| `204` | Every event is recorded, or was already | Deletes the rows |
| `400` `{"error":{"code":"invalid_request","message":…}}` | Any event is invalid. The message names the first, by its place and id, and says why, such as `events[3] (evt_…): detail: author.user_id must be one of the conversation's two people`. Nothing is recorded. | Moves the batch to its dead-letter table |
| `413` `{"error":{"code":"invalid_request",…}}` | The body is over 256 KiB, as declared or as read, or holds more than 100 events | As for `400` |
| `401` `{"error":{"code":"unauthorized",…}}` | A header is missing or doubled, the timestamp is outside the window, or the signature is malformed or does not match | Retries with backoff |
| `408` | The body did not arrive whole | Retries with backoff |
| `500` | The batch could not be recorded | Retries with backoff |
| `404` | Realtime is off, and its route is not served. The contract names `503` for this; realtime serves its route only while it is on and fully configured, so a process where it is off has no such route, and the host answers `404`. The Worker retries either. | Retries with backoff |

Realtime logs each refusal with its reason (`the Worker's events refused`), and each batch it takes with how many events were new (`realtime events accepted`).

### The events' golden vectors

The Worker's signing can be tested against these. `internal/httpserver/server_test.go` pins them, has realtime take each body whole, and has it refuse the purge's signature of the same body. `openssl dgst -sha256 -hmac "$secret"` over `"events.$timestamp.$body"` reproduces them.

| | |
| --- | --- |
| Secret | `5f2b8e1c9a7d4036b1e8c2f94a6d0e3b7c1f5a9d2e8b4c6f0a3d7e1b9c5f2a84` (64 characters, used as they are) |
| Timestamp | `1790208000` (2026-09-24T00:00:00Z) |
| A message posted | `{"events":[{"id":"evt_golden_posted_1","type":"realtime.message.posted.v1","detail":{"organization_id":"org_1","conversation":{"kind":"direct","id":"org_1~user-a~user-b"},"participant_user_ids":["user-a","user-b"],"message":{"id":"msg_1","seq":1},"author":{"kind":"member","user_id":"user-a"},"posted_at":"2026-09-24T10:00:00.000Z"}}]}` |
| Its signature | `v1=75442e7357021586eb3717f2c97b091546d26cc31bcfdecafe571f36e7836cfa` |
| The purge's signature of it, refused | `v1=a8cb24541792769a083259a5396d3d53bbd95c34cd72296d65acaf993e8f583e` |
| A side conversation read | `{"events":[{"id":"evt_golden_read_1","type":"realtime.conversation.read.v1","detail":{"organization_id":"org_1","conversation":{"kind":"side","id":"org_1~tsp_1~user-a~user-b","teamspace_id":"tsp_1"},"reader_user_id":"user-b","read_through_seq":7,"read_at":"2026-09-24T10:05:00.123Z"}}]}` |
| Its signature | `v1=68488d8dd95e857bc0550ff45cbad1cacb526bd14b30b6cdad7b12216d52adfc` |
| The purge's signature of it, refused | `v1=6478f2e3e872191fa991b20d7569b715ed46a1ea533b5590717ab300e5119d8d` |

## Events realtime publishes

Realtime publishes on source `oryxa.realtime`, in the envelope every Oryxa publisher uses, signed as realtime. Contracts live in `/contracts/events/realtime/v1`. Nothing consumes them yet, so no rule or queue matches them.

| Detail type | When | Data |
| --- | --- | --- |
| `realtime.message.posted.v1` | The Worker stored a finished message: a person's, or an agent's final answer in a side conversation. Never streaming, typing, request cards or failures. | `organization_id`, `conversation` (`kind`, `id`, and `teamspace_id` for a side one), `participant_user_ids` (the two, sorted), `message` (`id`, `seq`), `author` (`kind`, `user_id`, and `agent_id` for an agent), `posted_at` |
| `realtime.conversation.read.v1` | A member's read position advanced because they read what someone else posted. An author's own post moves their position without a read event, since the posted event says it. The Worker sends only the newest it had not sent. | `organization_id`, `conversation`, `reader_user_id`, `read_through_seq`, `read_at` |
| `realtime.purge.completed.v1` | The Worker answered `204` to a purge: the deletion's audit trail | `organization_id`, `scope` (`member`, `organization`, `teamspace_member` or `teamspace`), `user_id` (for a member's, in the organization or a teamspace), `teamspace_id` (for a teamspace's), `removed_at`, `source_event_id` (the event that asked for it, auth's or agents'), `completed_at` |

An event happened, as the envelope's `occurred_at` says, at its `posted_at`, `read_at` or `completed_at`. Its `event_id` is derived from what it is about, rather than drawn at random: from the Worker's id for the Worker's events, and from the event that asked for the purge for a completed purge. So an event recorded twice is one row in the outbox and one `event_id` to a consumer.

**Consumers must order by `seq`, not by arrival.** The shared dispatcher publishes each batch it takes from the outbox in parallel, up to eight events at once, and publishes a failed one again later. EventBridge keeps no order either. So a conversation's events reach the bus in any order: on 2026-09-25 a direct message's `seq` 2 reached it before its `seq` 1. A consumer puts a conversation's messages in order by `message.seq`, and takes a reader's position as the highest `read_through_seq` it has seen for them, not the last to arrive.

## Storage

Realtime's schema is two tables, installed through postgreskit under `realtime` in `oryxa_migrations` (`internal/postgres/migrations`). Migrations run whether realtime is on or off, so turning it on needs none.

- `realtime_outbox` is the outbox every service has, which the shared dispatcher publishes from, and forgets a week after publishing.
- `realtime_accepted_events` holds the ids of the Worker's events realtime has accepted. A worker deletes, every hour, those accepted more than seven days ago. The Worker gives up on an event seven days after recording it, which was before realtime took it, so no retry arrives once its id is gone.

## Configuration

| Variable | What it is |
| --- | --- |
| `REALTIME_WORKER_URL` | The web app's origin, where the Worker serves `/internal/realtime/purge`. `https://app.oryxa.in` in production. Locally, web-next's `http://localhost:3001`. It has no path: realtime appends the route, which the Worker matches exactly, so a URL with a path is refused. |
| `REALTIME_PURGE_SECRET` | The key every call is signed with, both ways: the purge realtime sends, and the events the Worker sends. The Worker holds the same value. Realtime takes any 32 characters or more, but make it with `openssl rand -hex 32`: in production, `deploy/scripts/put-secrets.sh` stores only exactly 64 hex characters, and the Worker must hold the same 64 hex characters, since each side uses them as written. Realtime trims surrounding whitespace, but neither side strips quotes or a comment copied from `.env`. |
| `REALTIME_EVENTS_QUEUE_URL` | The queue the provisioner prints |
| `REALTIME_EVENTS_QUEUE_NAME` | The provisioner's name for that queue: `realtime-events` unless set |

Realtime runs only once the first three are set, and then needs `DATABASE_URL`, as every service that keeps anything does. Its route is served only while it runs. A process running every service says once which are missing and runs without realtime (`realtime is off`). A process given `ORYXA_SERVICES=realtime`, alone or in a list, refuses to start instead. A value that is set but malformed, such as a relative URL or a short secret, always refuses. A production start (`DEPLOYMENT_ENVIRONMENT=production`) refuses while `REALTIME_EVENTS_QUEUE_URL` is unset, as for every consumer, but only once `REALTIME_WORKER_URL` or `REALTIME_PURGE_SECRET` is set. With neither, realtime is off and hears nothing, so a release can carry it before its queue exists.

## Provision

Copy the root `.env.example` to the ignored `.env`. Its emulator block points SQS and EventBridge at LocalStack in the root `docker-compose.yml` (`docker compose up -d eventbridge`, on `:4566`). Remove that block to provision AWS through the default credential chain. Then run:

```sh
(set -a && . ./.env && go run ./realtime/infra/events/cmd/provision)
```

The command is repeatable. It creates or updates the `realtime-events` queue and `realtime-events-dlq`, with a fifteen-minute visibility timeout, a redrive after five receives, 14 days' retention and SQS-managed encryption. It also creates a `realtime-auth-memberships` rule on `EVENT_BUS_NAME`, matching the four detail types above, each from its own source: an `$or` of auth's two from `oryxa.auth` and agents' two from `oryxa.agents`. The rule kept its name when agents' events were added, since the provisioner deletes no rule, and a renamed one would have left the old one delivering beside it. Re-run the command, or `deploy/scripts/provision-app.sh`, wherever the rule was made before, so it matches agents' events too. The rule's target is the queue, with the dead-letter queue for an event EventBridge cannot deliver to it, and each queue's policy admits that rule alone. Put the printed `queue_url` in `REALTIME_EVENTS_QUEUE_URL`. LocalStack forgets it all when it restarts, so re-run the command after a restart.

## Run locally

```sh
docker compose up -d eventbridge
(set -a && . ./.env && go run ./realtime/infra/events/cmd/provision)
(set -a && . ./.env && ORYXA_SERVICES=realtime go run ./cmd/oryxa)
```

With web-next running its Worker on `:3001`, and `REALTIME_WORKER_URL` and the same `REALTIME_PURGE_SECRET` on both sides, removing a member from an organization purges their conversations. With teams and agents running too, and agents' events queue set, removing someone from a team that has a teamspace purges their side conversations in it, and deleting the teamspace, or its team, purges all of its. Realtime logs `realtime conversations purged` for each purge. With the Worker's `ORYXA_API_ORIGIN` at this process, each message and read in a conversation reaches realtime, which logs `realtime events accepted`.

A call can be signed by hand. The golden body's time is in the past, so it is taken whenever it is signed:

```sh
secret=…  # REALTIME_PURGE_SECRET, as the process has it
timestamp=$(date +%s)
body='{"events":[{"id":"evt_golden_posted_1","type":"realtime.message.posted.v1","detail":{"organization_id":"org_1","conversation":{"kind":"direct","id":"org_1~user-a~user-b"},"participant_user_ids":["user-a","user-b"],"message":{"id":"msg_1","seq":1},"author":{"kind":"member","user_id":"user-a"},"posted_at":"2026-09-24T10:00:00.000Z"}}]}'
signature=$(printf '%s' "events.$timestamp.$body" | openssl dgst -sha256 -hmac "$secret" | awk '{print $NF}')
curl -i http://localhost:8080/v1/realtime/events -H 'Content-Type: application/json' \
  -H "X-Oryxa-Timestamp: $timestamp" -H "X-Oryxa-Signature: v1=$signature" --data-binary "$body"
```

The storage's integration tests need a PostgreSQL they may create schemas in, each test in a schema of its own, dropped when it ends:

```sh
REALTIME_TEST_DATABASE_URL='postgres://oryxa:oryxa-dev-password@localhost:5435/oryxa?sslmode=disable' go test ./realtime/...
```

## Deploy

Realtime deploys in the single `oryxa` image, off. Its identity needs `sqs:ReceiveMessage` and `sqs:DeleteMessage` on its queue, and `events:PutEvents` on the bus, which the task role's `PublishEvents` statement already grants every service, whatever its source. It also needs outbound HTTPS to the web app's origin, and nothing in front of the Worker may challenge a request from AWS to `/internal/realtime/purge`: a Cloudflare bot or WAF rule answering `403` would send every purge to the dead-letter queue. The same goes the other way: nothing may challenge the Worker's `POST /v1/realtime/events` on its way to the tunnel.

For the Worker's events to arrive, web-next's Worker needs `ORYXA_API_ORIGIN`, the API's public origin, and the same `REALTIME_PURGE_SECRET`, and realtime must be on. Until it is, the route answers `404`, and the Worker keeps each event, retrying, for seven days, then drops it.

In production it stays off until web-next serves `app.oryxa.in`. The Worker's endpoint must be live, holding the same secret, before realtime is on, and today `app.oryxa.in` is served by the old `web/` Worker, which has no purge route and answers `404`, so every purge would end in the dead-letter queue. web-next has the route but no production environment yet, so there is no production Worker to give the secret to (`wrangler secret put REALTIME_PURGE_SECRET --env production` waits for one). `realtime_purge` in `deploy/production/realtime.auto.tfvars.json` is the switch, committed off, and `deploy/README.md` gives the order to turn it on.

## Limits

- `removed_at` comes from auth's clock, or for a teamspace's purge from Teams' or agents', and the Worker's messages are stamped by Cloudflare's. The clocks agree only to within a few milliseconds, so a message written in the same instant as the removal may fall on either side of it.
- Realtime's events have no consumer yet. They reach the bus, and its archive, and nothing else.
- The Worker's event ids are deduplicated alone, not with their conversation, so the Worker must make them unique across every conversation.
- A dropped purge is gone from the queue. Its log line carries it whole, as `event_id`, `scope`, `organization_id`, `user_id`, `teamspace_id` and `removed_at`, so it can be sent again by hand. In production the bus also archives every event for a week, so one can be replayed to realtime's rule once whatever stopped it is put right.
