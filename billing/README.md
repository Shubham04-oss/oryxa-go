# Billing service

Billing sells Oryxa to organizations through [Dodo Payments](https://dodopayments.com), which is the merchant of record: it collects GST and other sales taxes, and issues invoices and receipts itself. Billing keeps each organization's subscription in step with Dodo, turns it into entitlements other services enforce, and announces what billing contacts should hear about.

```text
app ── checkout / portal ──▶ billing ── customers, checkouts, portal, cancellations ──▶ Dodo Payments
                               ▲  │
                Dodo webhooks ─┘  ├── billing.entitlements.changed.v1 ──▶ auth (member limits)
                                  └── billing.subscription.*          ──▶ email (billing contact)
auth ── organization created/updated/deleted ──▶ billing-events queue ──▶ billing
```

Billing never verifies a token itself. It forwards the caller's bearer token or session cookie to auth and acts on the membership auth returns, so auth stays the one service that decides identity and roles. Only owners can start checkout or open the customer portal; any member can read the organization's billing.

## Plans

What Oryxa sells lives in a plan catalog, not in code: a JSON file named by `BILLING_PLANS_FILE`. Each plan maps a Dodo product to what it entitles an organization to. Changing pricing means creating the products in Dodo and editing the catalog; billing reconciles every organization with the catalog when it starts.

```json
{
  "unpaid": {"member_limit": 3},
  "plans": [
    {"key": "team", "product_id": "pdt_...", "member_limit": 25},
    {"key": "seats", "product_id": "pdt_...", "member_limit_per_unit": 1, "min_quantity": 5}
  ]
}
```

| Field | Meaning |
| --- | --- |
| `unpaid.member_limit` | Members an organization without a paid subscription may have, pending invitations included |
| `key` | The plan's name in the API and in events |
| `product_id` | The Dodo subscription product |
| `member_limit` | A fixed member cap |
| `member_limit_per_unit` | A cap that scales with the subscription's quantity, for per-seat pricing; `min_quantity` and `max_quantity` bound what checkout may buy |

Leave a limit out for no limit. Without a catalog there are no plans and no limits. [`plans.example.json`](plans.example.json) shows both kinds of plan; its product IDs are placeholders.

A subscription to a product the catalog doesn't list keeps its paid status with no limits, and billing logs an error, so a paying customer is never cut off by a catalog that lags behind Dodo.

## Entitlements

Every organization has one current subscription, and its status decides the organization's entitlements:

| Dodo status | Entitlement status | Plan |
| --- | --- | --- |
| `active` | `active` | The subscribed plan |
| `past_due` | `past_due`, with `grace_ends_at` | The subscribed plan, until the grace period ends |
| `on_hold`, `paused`, `cancelled`, `expired`, `failed`, `pending`, or none | `unpaid` | The catalog's `unpaid` allowance |

`past_due` is Dodo's grace period: a renewal failed, but the customer keeps what they paid for until `past_due_ends_at`. Grace periods are off in Dodo by default; turn them on under Settings → Subscriptions → Subscription Grace Period, otherwise a failed renewal puts the subscription straight on hold.

Whenever an organization's entitlements change, billing publishes `billing.entitlements.changed.v1` with the complete entitlements and a revision that only increases. It also publishes the first entitlements of every new organization. Auth applies the member limit to invitations and acceptances.

## Events

Billing publishes source `oryxa.billing` to `EVENT_BUS_NAME`, in the envelope every Oryxa publisher uses. Contracts live in `contracts/events/billing/v1`.

| Detail type | When | Consumed by |
| --- | --- | --- |
| `billing.entitlements.changed.v1` | An organization's entitlements change, or it is created | auth |
| `billing.subscription.payment-failed.v1` | A renewal fails: into `past_due` (with `grace_ends_at`) or `on_hold` | email |
| `billing.subscription.ended.v1` | A subscription that granted a plan is cancelled or expires | email |
| `billing.subscription.renewal-upcoming.v1` | A monthly or yearly subscription renews within `BILLING_RENEWAL_REMINDER_DAYS` (7 by default; 0 turns reminders off), once per renewal | email |

Billing emails go to the Dodo customer's email: the billing contact, which the customer can change in the portal. They link to `BILLING_URL`.

Billing consumes `auth.organization.created.v1`, `auth.organization.updated.v1` and `auth.organization.deleted.v1`. Deleting an organization cancels its subscription in Dodo immediately, so a deleted organization is never charged again.

## API

| Method | Path | Who | Purpose |
| --- | --- | --- | --- |
| `GET` | `/healthz` | Anyone | Liveness |
| `GET` | `/v1/billing/plans` | Anyone | The plan catalog, without Dodo product IDs |
| `GET` | `/v1/billing/organizations/{organizationID}` | Members | Entitlements and the current subscription |
| `POST` | `/v1/billing/organizations/{organizationID}/checkout` | Owners | Start a Dodo checkout: `{"plan": "team", "quantity": 5}`; returns `checkout_url` |
| `POST` | `/v1/billing/organizations/{organizationID}/portal` | Owners | Open the Dodo customer portal; returns `portal_url` |
| `POST` | `/v1/billing/webhooks/dodo` | Dodo | Signed subscription webhooks |

Checkout is refused with 409 while the organization has a subscription it can still pay, resume or fix: `active`, `past_due`, `on_hold` or `paused`. Those are managed in the portal, where the customer updates payment methods, changes or cancels plans, and downloads invoices. Cookie-authenticated `POST`s must come from an origin in `BILLING_ALLOWED_ORIGINS`, which defaults to `BILLING_URL`'s.

The first checkout creates the organization's Dodo customer with the owner's email; every later checkout and portal session uses that customer.

## Webhooks

In the Dodo dashboard, add a webhook endpoint at `https://<billing host>/v1/billing/webhooks/dodo` subscribed to the `subscription.*` events, and put its signing secret in `DODO_PAYMENTS_WEBHOOK_KEY`.

Billing verifies each delivery's Standard Webhooks signature and rejects anything older than five minutes. It applies each delivery once by its `webhook-id`, ignores deliveries older than the state it already has, and answers 2xx only after the change is committed, so Dodo retries anything that failed.

## Provision

Billing uses `/internal/postgreskit`, the same PostgreSQL boundary as auth, connections, files and teams: one instrumented `pgxpool`, with sqlx on that pool. Tables are created by the embedded migration when `BILLING_AUTO_MIGRATE` is true, the default. Logs, HTTP access, AWS SDK and pgx spans come from `/internal/observability`; OTLP export is opt-in through `OTEL_EXPORTER_OTLP_ENDPOINT`. The queue auth's organization events arrive on is provisioned like the other services' AWS resources:

```sh
(set -a && . billing/.env && go run ./billing/infra/events/cmd/provision)
```

The command is repeatable. It creates or updates the `billing-events` queue with its dead-letter queue, and a `billing-auth-organizations` rule on `EVENT_BUS_NAME` whose target is the queue and which the queue policy admits alone. Put the printed `queue_url` in `BILLING_EVENTS_QUEUE_URL`.

Auth has its own queue for billing's entitlements; see `auth/README.md`.

## Run locally

Copy `.env.example` to an ignored `.env`. Dodo has a test mode but no emulator, so checkout and the portal need a test-mode API key; without `DODO_PAYMENTS_API_KEY` billing still starts, answering those endpoints with 503. Webhooks need Dodo to reach billing, through a tunnel such as `cloudflared tunnel --url http://localhost:8081`.

```sh
docker compose up -d
(set -a && . billing/.env && go run ./billing/infra/events/cmd/provision)
(set -a && . billing/.env && go run ./billing/cmd/billing)
```

Or in Docker on the shared network, like auth:

```sh
docker compose -f compose.billing.yaml up -d --build billing
```

The store has its own tests against PostgreSQL, which the unit test run skips:

```sh
BILLING_TEST_DATABASE_URL='postgres://oryxa:oryxa-dev-password@localhost:5435/oryxa?sslmode=disable' go test ./billing/internal/postgres
```

## Deploy

Billing is a long-running container, like auth, behind the same public edge: the webhook endpoint must be reachable by Dodo, and the other routes by the app. Its runtime identity needs `events:PutEvents` on the event bus and `sqs:ReceiveMessage` and `sqs:DeleteMessage` on `billing-events`.

Before taking payments: create the products in Dodo live mode, point the catalog's `product_id`s at them, set `DODO_PAYMENTS_ENVIRONMENT=live_mode` with a live API key and webhook secret, and turn on Dodo's subscription grace period if organizations should keep their plan while a renewal is retried.
