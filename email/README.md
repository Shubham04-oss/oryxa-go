# Email service

The email service sends Oryxa's system email. It has no API of its own: it subscribes to auth's and billing's events on Amazon EventBridge, renders a template for each, and sends it with Amazon SES (API v2). That is a welcome when a user registers; auth's delivery requests — email verification, password reset and organization invitations — each built around the link auth supplied; notices when a member's place in an organization changes; and billing notices to an organization's billing contact. It never verifies an account, resets a password, accepts an invitation or touches a subscription; auth and billing own those.

It sends each email at most once per event, keeps a log of every email and what became of it, and stops emailing addresses that bounce permanently or complain.

```text
auth, billing ── events ────▶ email-subscriptions rule ─┐
                                                        ├─▶ email-events queue ─▶ email ─▶ SES
SES ─── aws.ses events ─────▶ email-ses-feedback rule ──┘                          │
                                                                                   ▼
                                                             email-deliveries, email-suppressions
```

The service consumes the queue through one handler, run by Lambda in AWS and by a poller everywhere else. A message that keeps failing moves to `email-events-dlq` after five receives.

## Subscribed events

| Source | Detail type | Does |
| --- | --- | --- |
| `oryxa.auth` | `auth.user.registered.v1` | Sends `welcome` to `data.email` |
| `oryxa.auth` | `auth.user.email-verification.delivery-requested.v1` | Sends `verify-email` with `data.verification_url` to `data.email` |
| `oryxa.auth` | `auth.user.password-reset.delivery-requested.v1` | Sends `password-reset` with `data.reset_url` to `data.email` |
| `oryxa.auth` | `auth.member.invited.v1` | Sends `invitation` with `data.accept_url` to `data.email` |
| `oryxa.auth` | `auth.member.invitation-revoked.v1` | Sends `invitation-revoked` to `data.email` |
| `oryxa.auth` | `auth.member.role-changed.v1` | Sends `role-changed` to `data.email`, unless the member changed their own role |
| `oryxa.auth` | `auth.member.removed.v1` | Sends `member-removed`, or `organization-deleted` when `data.reason` is `organization_deleted`, to `data.email` — unless the member left or deleted the organization themselves |
| `oryxa.billing` | `billing.subscription.payment-failed.v1` | Sends `payment-failed` to `data.billing_email`, with the grace deadline when `data.grace_ends_at` is set |
| `oryxa.billing` | `billing.subscription.ended.v1` | Sends `subscription-ended` to `data.billing_email` |
| `oryxa.billing` | `billing.subscription.renewal-upcoming.v1` | Sends `renewal-upcoming` to `data.billing_email`, unless handled after `data.renews_at` |
| `aws.ses` | `Email Delivered`, `Email Delivery Delayed`, `Email Rejected` | Records the status |
| `aws.ses` | `Email Bounced` | Records the status; suppresses the address if the bounce is `Permanent` |
| `aws.ses` | `Email Complaint Received` | Records the status; suppresses the address unless the feedback type is `not-spam` |

Auth emits `auth.user.registered.v1` once per user, when the user first signs in: straight away for a Google sign-up, and for a password account at its first login after the email is verified — so a password user never gets a welcome before confirming their address.

Deleting an organization makes auth emit `auth.member.removed.v1` for every member, which is how each of them hears about it; `auth.organization.deleted.v1` itself sends nothing. Membership notices go only to the member affected, never to the people who acted.

Billing notices go to the billing contact Dodo Payments holds for the organization, and link to the app's billing page (`data.billing_url`), which must be HTTPS outside `localhost`. Dates are written in UTC. Receipts and invoices come from Dodo, not from this service.

A domain event's `detail` must be the publisher envelope (`event_id`, `version` 1, `occurred_at`, `data`), with `data` decoded as the matching type in `contracts/events`. The feedback rule only forwards SES events for mail sent through this service's configuration set.

To send mail for another event: add it to `Subscriptions` and `NewConsumer` in `internal/events`, give `internal/application` a method for it, add a template directory under `internal/mail/templates`, and re-run the provisioner so the rule matches it.

## Links

Verification, password reset and invitation emails each carry a single-use link that auth generated, and whoever holds the link can use it. Auth scrubs the link from its outbox once the event is published; this service carries it into the email and nowhere else:

- A link must be an absolute HTTPS URL (plain HTTP only on `localhost`). Rejections, like every error and log line, never include it.
- The delivery log records which email went to whom, never its content or link.
- An event handled after its `expires_at` is not sent and is recorded as `expired`. Reset links last 30 minutes, so an event held up by retries or left in the dead-letter queue can easily outlive its link.
- The dead-letter queue keeps failed events, links included, for 14 days under SQS encryption. Redriving them sends only the links that still work.

## Deliveries

Every email is a row in `email-deliveries`, keyed by a hash of the event's source and ID, the template, and the recipient. Before sending, an attempt claims that row with a conditional write; a redelivered event finds it already `sent` and sends nothing. A claim that is still held returns the message to the queue, and one abandoned by a crashed attempt expires after 30 seconds so the retry can take it over. The one duplicate this cannot prevent is an attempt that dies after SES accepts the email but before the row records it.

| Status | Meaning |
| --- | --- |
| `sending` | Claimed by an attempt in progress |
| `failed` | SES refused the send; the next retry claims it again (`last_error` says why) |
| `suppressed` | Not sent: the recipient is on the suppression list |
| `expired` | Not sent: its link had expired by the time it was handled |
| `sent` | Accepted by SES |
| `delayed` | SES is still retrying delivery |
| `delivered` | Accepted by the recipient's mail server |
| `rejected`, `bounced` | Not delivered (`feedback_detail` says why) |
| `complained` | Delivered, then marked as spam |

SES events can arrive out of order, so a delivery only moves forward through that table: a late `delayed` never replaces `delivered`. Rows expire a year after they are created.

To see everything sent to an address, query the `recipient` index:

```sh
(set -a && . email/.env && aws dynamodb query --table-name email-deliveries --index-name recipient \
  --key-condition-expression 'recipient_key = :address' \
  --expression-attribute-values '{":address":{"S":"asha@example.com"}}')
```

## Suppression

`email-suppressions` holds one row per lowercased address, with the first reason it was suppressed. Sends to it are recorded as `suppressed` and never reach SES. The configuration set also enables SES's own suppression for bounces and complaints, so SES keeps refusing an address even if this list is bypassed.

Lifting a suppression in AWS takes both commands. Against the emulators only the first applies: Moto keeps no SES suppression list.

```sh
aws dynamodb delete-item --table-name email-suppressions --key '{"address":{"S":"asha@example.com"}}'
aws sesv2 delete-suppressed-destination --email-address asha@example.com
```

## Provision

Copy `.env.example` to an ignored `.env`. Its last block points everything at the emulators in the root `docker-compose.yml`; delete that block to provision AWS through the default credential chain. Then run:

```sh
(set -a && . email/.env && go run ./email/infra/delivery/cmd/provision)
```

The command is repeatable. It creates or updates:

- the `email-deliveries` table (with its `recipient` index and TTL) and the `email-suppressions` table, both on-demand and protected against deletion;
- the `email-events` queue and its dead-letter queue;
- the `email-subscriptions` rule on `EVENT_BUS_NAME` and the `email-ses-feedback` rule on the default bus (SES publishes nowhere else), with a queue policy that admits only those two;
- an SES identity for the domain of `EMAIL_FROM_ADDRESS`;
- unless `SES_EVENT_PUBLISHING=false`, the `oryxa-email` configuration set with bounce and complaint suppression, publishing delivery, delay, bounce, complaint and reject events to EventBridge.

For a new domain in AWS, publish the printed `dkim_records` in DNS; `ses_verified` turns true once SES finds them. A new SES account also starts in the sandbox, where it only delivers to verified addresses.

## Run locally

```sh
docker compose up -d
(set -a && . email/.env && go run ./email/infra/delivery/cmd/provision)
(set -a && . email/.env && go run ./email/cmd/email)
```

The emulators forget everything when they restart, so re-run the provisioner after one. Auth running against the same emulators delivers its requests on its own; to publish one by hand, as auth's outbox would:

```sh
(set -a && . email/.env && aws events put-events --entries "$(jq -n '[{
  Source: "oryxa.auth", DetailType: "auth.user.email-verification.delivery-requested.v1", EventBusName: "default",
  Detail: ({
    event_id: "evt_local_1", version: 1, occurred_at: (now | todate),
    data: {request_id: "act_local_1", user_id: "user-1", email: "asha@example.com",
           verification_url: "http://localhost:3000/auth/verify-email?token=local", expires_at: (now + 86400 | todate)}
  } | tojson)
}]')")
```

Sent mail is listed under `ses.Message` at http://localhost:4567/moto-api/data.json.

Moto never delivers anything, so nothing bounces locally. To simulate a bounce, take the `delivery_id` from the delivery log (see [Deliveries](#deliveries)) and publish the event SES would:

```sh
DELIVERY_ID=paste-the-delivery-id
(set -a && . email/.env && aws events put-events --entries "$(jq -n --arg id "$DELIVERY_ID" '[{
  Source: "aws.ses", DetailType: "Email Bounced", EventBusName: "default",
  Detail: ({
    eventType: "Bounce",
    mail: {messageId: "simulated", destination: ["asha@example.com"],
           tags: {"ses:configuration-set": ["oryxa-email"], delivery_id: [$id]}},
    bounce: {bounceType: "Permanent", bounceSubType: "General", timestamp: "2026-09-13T12:00:05Z",
             bouncedRecipients: [{emailAddress: "asha@example.com"}]}
  } | tojson)
}]')")
```

The store's conditional writes have their own tests against a real DynamoDB, which the unit test run skips:

```sh
EMAIL_DYNAMODB_TEST_ENDPOINT=http://localhost:4566 go test ./email/internal/store
```

## Deploy

Build for Lambda's `provided.al2023` runtime on arm64:

```sh
GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -tags lambda.norpc -trimpath -ldflags="-s -w" -o bootstrap ./email/cmd/email
```

The function itself is not provisioned yet. It needs:

- `EMAIL_FROM_ADDRESS` (and optionally `EMAIL_FROM_NAME`); the table, configuration set and region settings default to what the provisioner creates;
- a timeout of 10 seconds or less, because the queue's visibility timeout is 60;
- an SQS event source mapping on `email-events` with `ReportBatchItemFailures`;
- permission to receive and delete messages from `email-events`, to `GetItem`, `PutItem` and `UpdateItem` on both tables, and to `ses:SendEmail` for the identity and the `oryxa-email` configuration set.
