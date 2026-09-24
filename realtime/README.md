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
X-Oryxa-Signature: v1=a6f400c72318e7a142e5fcc51f04f98fad8ffae7864549a902cadfa96b584067

{"event_id":"evt_Zx8rP0kQ3mB7tY2wN5cV1A","organization_id":"org_2fQx9LmR7vTk4WcA","user_id":"6c1d5f0e-2b7a-4c8e-9f3d-1a2b3c4d5e6f"}
```

- **Body:** `event_id` is the auth event's own id (the envelope's `event_id`). It stays the same however many times the event is delivered or published. `user_id` is the member whose conversations go. It is `""` when the organization was deleted, and then everything the organization kept goes. All three keys are always present, in that order.
- **`X-Oryxa-Timestamp`:** when this attempt was signed, in Unix seconds. Every retry is signed afresh, so the Worker can refuse one signed too long ago. The Worker's side allows five minutes.
- **`X-Oryxa-Signature`:** `v1=` followed by the lowercase hex HMAC-SHA256 of the timestamp in decimal, a `.`, and the body exactly as sent. The key is `REALTIME_PURGE_SECRET`'s characters as written. It is not decoded from hex or base64, even when it looks like hex.
- **User-Agent:** `oryxa-realtime/1`. Redirects are not followed.

| The Worker answers | Realtime |
| --- | --- |
| `204` | Done. The message is deleted. |
| `401` | A bad signature, which comes from the secret or the clock, not from this purge. It is logged, and the message is kept for the queue to deliver again, so it reaches the dead-letter queue and can be redriven once the secret is fixed. |
| `408`, `429` | The Worker asking for later. Kept and retried. |
| Any other `4xx` | A refusal for good. It is logged as `realtime purge dropped`, with the status and the start of the Worker's answer, and the message is deleted. |
| `5xx`, anything else, no answer within 30 seconds, or no connection | Kept and retried. |

A message that keeps failing is delivered again every five minutes. After five receives, about 25 minutes, it moves to `realtime-events-dlq`, which keeps it for 14 days. The purge is idempotent, so a repeat is always safe.

### Golden vectors

The Worker's verification can be tested against these. `internal/purge/purge_test.go` pins them, and `openssl dgst -sha256 -hmac "$secret"` over `"$timestamp.$body"` reproduces them.

| | |
| --- | --- |
| Secret | `5f2b8e1c9a7d4036b1e8c2f94a6d0e3b7c1f5a9d2e8b4c6f0a3d7e1b9c5f2a84` (64 characters, used as they are) |
| Timestamp | `1790208000` (2026-09-24T00:00:00Z) |
| Member body | `{"event_id":"evt_Zx8rP0kQ3mB7tY2wN5cV1A","organization_id":"org_2fQx9LmR7vTk4WcA","user_id":"6c1d5f0e-2b7a-4c8e-9f3d-1a2b3c4d5e6f"}` |
| Its signature | `v1=a6f400c72318e7a142e5fcc51f04f98fad8ffae7864549a902cadfa96b584067` |
| Organization body | `{"event_id":"evt_Qm4sT8vY1bN6cX3zL0pK7g","organization_id":"org_2fQx9LmR7vTk4WcA","user_id":""}` |
| Its signature | `v1=c35da0ee0a92d26cf07903fcfd3118d42b0517920abe31cf13a0f818b9004314` |

## Events

| Detail type | Purge |
| --- | --- |
| `auth.member.removed.v1`, `reason` `left` or `removed` | That member's conversations in that organization: `user_id` set |
| `auth.member.removed.v1`, `reason` `organization_deleted` | None. Deleting an organization removes every member, each with an event of their own, and the organization's purge already takes everything, so these make no calls. |
| `auth.organization.deleted.v1` | Everything the organization kept: `user_id` empty |

The envelope's signature is checked before anything in an event is used, as in every consumer (`internal/eventauth`). By default a signature that does not check out is logged and the event handled anyway (`ORYXA_EVENT_SIGNATURES=report`). With `enforce` it is refused. An event realtime cannot read, meaning one that is not JSON, not a subscribed type from `oryxa.auth`, not envelope version 1, missing its organization or member, or refused for its signature, stays on the queue and ends in the dead-letter queue, as in every consumer.

Realtime remembers the last 1,024 events it purged. A message delivered again after its purge succeeded is then deleted without a second call. This only saves calls. It does not guarantee anything, because it forgets on restart. The Worker's idempotence is the guarantee.

To consume another event: add it to `AuthSubscriptions` and `requestFor` in `internal/events`, and re-run the provisioner so the rule matches it.

## Configuration

| Variable | What it is |
| --- | --- |
| `REALTIME_WORKER_URL` | The web app's origin, where the Worker serves `/internal/realtime/purge`. `https://app.oryxa.in` in production. Locally, web-next's `http://localhost:3001`. |
| `REALTIME_PURGE_SECRET` | The key every call is signed with. The Worker holds the same value. At least 32 characters: `openssl rand -hex 32`. Surrounding whitespace is trimmed. |
| `REALTIME_EVENTS_QUEUE_URL` | The queue the provisioner prints |
| `REALTIME_EVENTS_QUEUE_NAME` | The provisioner's name for that queue: `realtime-events` unless set |

Realtime runs only once the first three are set. A process running every service says once which are missing and runs without realtime (`realtime is off`). A process given `ORYXA_SERVICES=realtime`, alone or in a list, refuses to start instead. A value that is set but malformed, such as a relative URL or a short secret, always refuses. As for every consumer, a production start (`DEPLOYMENT_ENVIRONMENT=production`) refuses while `REALTIME_EVENTS_QUEUE_URL` is unset, even with realtime off.

## Provision

Copy the root `.env.example` to the ignored `.env`. Its emulator block points SQS and EventBridge at LocalStack in the root `docker-compose.yml` (`docker compose up -d eventbridge`, on `:4566`). Remove that block to provision AWS through the default credential chain. Then run:

```sh
(set -a && . ./.env && go run ./realtime/infra/events/cmd/provision)
```

The command is repeatable. It creates or updates the `realtime-events` queue and `realtime-events-dlq`, with a five-minute visibility timeout, a redrive after five receives, 14 days' retention and SQS-managed encryption. It also creates a `realtime-auth-memberships` rule on `EVENT_BUS_NAME`, matching the two detail types above from `oryxa.auth`, whose target is the queue and which the queue policy admits alone. Put the printed `queue_url` in `REALTIME_EVENTS_QUEUE_URL`. LocalStack forgets it all when it restarts, so re-run the command after a restart.

## Run locally

```sh
docker compose up -d eventbridge
(set -a && . ./.env && go run ./realtime/infra/events/cmd/provision)
(set -a && . ./.env && ORYXA_SERVICES=realtime go run ./cmd/oryxa)
```

With web-next running its Worker on `:3001`, and `REALTIME_WORKER_URL` and the same `REALTIME_PURGE_SECRET` on both sides, removing a member from an organization purges their conversations. Realtime logs `realtime conversations purged` for each purge.

## Deploy

Realtime deploys in the single `oryxa` image. Its identity needs `sqs:ReceiveMessage` and `sqs:DeleteMessage` on its queue. It also needs outbound HTTPS to the web app's origin, and nothing in front of the Worker may challenge a request from AWS to `/internal/realtime/purge`: a Cloudflare bot or WAF rule answering `403` would drop every purge. `deploy/README.md` gives the order to turn it on in production. The Worker's endpoint must be live, holding the same secret, before realtime is on. Until then the Worker answers `404`, and a `404` drops the purge.

## Limits

- A purge carries no time. If a member is removed, rejoins, and writes something before a delayed purge runs (the Worker down, the message waiting on retries), that purge removes what they wrote since too.
- A dropped purge is gone. Its log line names the event. In production the bus archives every event for a week, so one can be replayed to realtime's rule once whatever the Worker refused is fixed.
