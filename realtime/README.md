# Realtime service

The web app keeps direct messages and teamspace side conversations only in its Worker's Durable Objects. No Go service holds a copy, so no Go service can delete them. When a member leaves an organization, or the organization is deleted, those conversations must go. Realtime hears both from auth, on its own queue, and turns each into one signed call that has the Worker purge them.

Realtime has no API and keeps nothing. It is off until it is configured.

```text
auth ── auth.member.removed.v1, auth.organization.deleted.v1 ──▶ realtime-auth-memberships rule
                                                                            │
                                                         realtime-events queue (dead letters: realtime-events-dlq)
                                                                            │
                                                                        realtime ── POST /internal/realtime/purge ──▶ Worker
```

## The call

Realtime makes the call. The Worker serves it. Both sides implement exactly this:

```http
POST {REALTIME_WORKER_URL}/internal/realtime/purge
Content-Type: application/json
X-Oryxa-Timestamp: 1790208000
X-Oryxa-Signature: v1=ee12ee93bb00fb5e9901c4a0ac8ed193a85539bfb2c76241f9182f2fcc71b90e

{"event_id":"evt_Zx8rP0kQ3mB7tY2wN5cV1A","organization_id":"org_2fQx9LmR7vTk4WcA","scope":"member","user_id":"6c1d5f0e-2b7a-4c8e-9f3d-1a2b3c4d5e6f","removed_at":"2026-09-24T10:00:00Z"}
```

- **Body:** compact JSON, with all five keys always present, in this order:
  - `event_id` is the auth event's own id (the envelope's `event_id`). It stays the same however many times the event is delivered or published.
  - `organization_id` is the organization.
  - `scope` is `member` or `organization`. The scope is always stated, so an empty `user_id` can never mean the whole organization by accident.
  - `user_id` is the member whose conversations go. It is `""` when `scope` is `organization`.
  - `removed_at` is when the membership ended (the event's `removed_at`), or when the organization was deleted (its `deleted_at`). It is written in UTC, in RFC 3339, with as many digits of a fraction of a second as the event had, up to nine. Auth's timestamps have them, so most real bodies carry a fraction. It has none when the time is a whole second. The Worker deletes only what was written at or before `removed_at`, so a purge that runs late, after a member was invited back, leaves what they wrote since.
- **`X-Oryxa-Timestamp`:** when this attempt was signed, in Unix seconds. Every retry is signed afresh, so the Worker can refuse one signed too long ago. The Worker's side allows five minutes.
- **`X-Oryxa-Signature`:** `v1=` followed by the lowercase hex HMAC-SHA256 of the timestamp in decimal, a `.`, and the body exactly as sent. The key is `REALTIME_PURGE_SECRET`'s characters as written. It is not decoded from hex or base64, even when it looks like hex.
- **User-Agent:** `oryxa-realtime/1`. Redirects are not followed.

| The Worker answers | Realtime |
| --- | --- |
| `204` | Done. The message is deleted. |
| `400` or `413` carrying the Worker's own refusal, `{"error":{"code":…}}` | The Worker refusing the body for good: it cannot be read as a purge, or it is over 4 KB. It is logged as `realtime purge dropped`, with the status, the start of the Worker's answer and the purge's five fields, and the message is deleted. |
| `401` | A bad signature, which comes from the secret or the clock, not from this purge. It is logged, and the message is kept for the queue to deliver again, so it reaches the dead-letter queue and can be redriven once the secret is fixed. |
| `408`, `429` | The Worker asking for later. Kept and retried. |
| Any other `4xx`, including a `400` or `413` without the Worker's refusal | Not the Worker refusing this purge. A `403` is usually Cloudflare challenging the call, and a `404` or `405` a URL that does not reach the Worker's route, or a Worker without it. Kept and retried, so it reaches the dead-letter queue, whose alarm mails, and can be redriven once that is put right. |
| `5xx`, anything else, no answer in time, or no connection | Kept and retried. A member's purge has 30 seconds, and an organization's 120, since the Worker answers that only once it has purged every member. |

A message that keeps failing is delivered again every fifteen minutes, which outlasts a batch of five organizations' purges the Worker never answers. After five receives, about 75 minutes, it moves to `realtime-events-dlq`, which keeps it for 14 days. The purge is idempotent, so a repeat is always safe.

### Golden vectors

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

The Worker's side agreed on the first two vectors. The third is here so that the Worker's parsing of `removed_at` can be tested on what auth actually sends. JavaScript's `Date.parse` reads it, to the millisecond.

## Events

| Detail type | Purge |
| --- | --- |
| `auth.member.removed.v1`, `reason` `left`, `removed` or `organization_deleted` | That member's conversations in that organization: `scope` `member`, their `user_id`, and `removed_at` from the event's `removed_at` |
| `auth.organization.deleted.v1` | Everything the organization kept: `scope` `organization`, `user_id` `""`, and `removed_at` from the event's `deleted_at` |

Deleting an organization removes every member, each with an event of their own, and each of those is purged too, although the organization's purge takes everything it kept. The organization's purge reaches only the conversations the Worker's registry knows of, and nothing guarantees that is all of them. A member's purge that finds nothing left is still answered `204`.

The envelope's signature is checked before anything in an event is used, as in every consumer (`internal/eventauth`). By default a signature that does not check out is logged and the event handled anyway (`ORYXA_EVENT_SIGNATURES=report`). With `enforce` it is refused. So with report-only signatures, the platform's default, an event forged onto the bus would still be acted on, and its purge sent. That is the platform's current stance on signatures, not realtime's alone: `enforce` waits for every service to sign with a KMS key.

Realtime accepts a signature up to 21 days old, where other consumers accept one a day old. A purge can wait on its queue and dead-letter queue for the 14 days they keep it, and then be replayed from the bus's archive, which keeps a week, and under `enforce` it must still verify. A purge acted on late is only a repeat.

An event realtime cannot read, meaning one that is not JSON, not a subscribed type from `oryxa.auth`, not envelope version 1, missing its organization, member or time, or refused for its signature, stays on the queue and ends in the dead-letter queue, as in every consumer.

A `removed_at`, or `deleted_at`, more than five minutes after realtime received the event is a removal that has not happened. Purged up to it, a member would lose what they wrote after being invited back, so its purge is never sent. It is dropped, and logged as `realtime purge dropped` with the purge's five fields. Five minutes allows for auth's clock and realtime's disagreeing.

Realtime remembers the last 1,024 events it purged. A message delivered again after its purge succeeded is then deleted without a second call. This only saves calls. It does not guarantee anything, because it forgets on restart. The Worker's idempotence is the guarantee.

To consume another event: add it to `AuthSubscriptions` and `requestFor` in `internal/events`, and re-run the provisioner so the rule matches it.

## Configuration

| Variable | What it is |
| --- | --- |
| `REALTIME_WORKER_URL` | The web app's origin, where the Worker serves `/internal/realtime/purge`. `https://app.oryxa.in` in production. Locally, web-next's `http://localhost:3001`. It has no path: realtime appends the route, which the Worker matches exactly, so a URL with a path is refused. |
| `REALTIME_PURGE_SECRET` | The key every call is signed with. The Worker holds the same value. Realtime takes any 32 characters or more, but make it with `openssl rand -hex 32`: in production, `deploy/scripts/put-secrets.sh` stores only exactly 64 hex characters, and the Worker must hold the same 64 hex characters, since each side uses them as written. Realtime trims surrounding whitespace, but neither side strips quotes or a comment copied from `.env`. |
| `REALTIME_EVENTS_QUEUE_URL` | The queue the provisioner prints |
| `REALTIME_EVENTS_QUEUE_NAME` | The provisioner's name for that queue: `realtime-events` unless set |

Realtime runs only once the first three are set. A process running every service says once which are missing and runs without realtime (`realtime is off`). A process given `ORYXA_SERVICES=realtime`, alone or in a list, refuses to start instead. A value that is set but malformed, such as a relative URL or a short secret, always refuses. A production start (`DEPLOYMENT_ENVIRONMENT=production`) refuses while `REALTIME_EVENTS_QUEUE_URL` is unset, as for every consumer, but only once `REALTIME_WORKER_URL` or `REALTIME_PURGE_SECRET` is set. With neither, realtime is off and hears nothing, so a release can carry it before its queue exists.

## Provision

Copy the root `.env.example` to the ignored `.env`. Its emulator block points SQS and EventBridge at LocalStack in the root `docker-compose.yml` (`docker compose up -d eventbridge`, on `:4566`). Remove that block to provision AWS through the default credential chain. Then run:

```sh
(set -a && . ./.env && go run ./realtime/infra/events/cmd/provision)
```

The command is repeatable. It creates or updates the `realtime-events` queue and `realtime-events-dlq`, with a fifteen-minute visibility timeout, a redrive after five receives, 14 days' retention and SQS-managed encryption. It also creates a `realtime-auth-memberships` rule on `EVENT_BUS_NAME`, matching the two detail types above from `oryxa.auth`. The rule's target is the queue, with the dead-letter queue for an event EventBridge cannot deliver to it, and each queue's policy admits that rule alone. Put the printed `queue_url` in `REALTIME_EVENTS_QUEUE_URL`. LocalStack forgets it all when it restarts, so re-run the command after a restart.

## Run locally

```sh
docker compose up -d eventbridge
(set -a && . ./.env && go run ./realtime/infra/events/cmd/provision)
(set -a && . ./.env && ORYXA_SERVICES=realtime go run ./cmd/oryxa)
```

With web-next running its Worker on `:3001`, and `REALTIME_WORKER_URL` and the same `REALTIME_PURGE_SECRET` on both sides, removing a member from an organization purges their conversations. Realtime logs `realtime conversations purged` for each purge.

## Deploy

Realtime deploys in the single `oryxa` image, off. Its identity needs `sqs:ReceiveMessage` and `sqs:DeleteMessage` on its queue. It also needs outbound HTTPS to the web app's origin, and nothing in front of the Worker may challenge a request from AWS to `/internal/realtime/purge`: a Cloudflare bot or WAF rule answering `403` would send every purge to the dead-letter queue.

In production it stays off until web-next serves `app.oryxa.in`. The Worker's endpoint must be live, holding the same secret, before realtime is on, and today `app.oryxa.in` is served by the old `web/` Worker, which has no purge route and answers `404`, so every purge would end in the dead-letter queue. web-next has the route but no production environment yet, so there is no production Worker to give the secret to (`wrangler secret put REALTIME_PURGE_SECRET --env production` waits for one). `realtime_purge` in `deploy/production/realtime.auto.tfvars.json` is the switch, committed off, and `deploy/README.md` gives the order to turn it on.

## Limits

- `removed_at` comes from auth's clock, and the Worker's messages are stamped by Cloudflare's. The two clocks agree only to within a few milliseconds, so a message written in the same instant as the removal may fall on either side of it.
- A dropped purge is gone from the queue. Its log line carries it whole, as `event_id`, `scope`, `organization_id`, `user_id` and `removed_at`, so it can be sent again by hand. In production the bus also archives every event for a week, so one can be replayed to realtime's rule once whatever stopped it is put right.
