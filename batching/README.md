# Batching

Batching is the durable boundary between high-volume application webhooks and
agent wakeups. It consumes `apps.trigger.received.v1`, groups ordinary triggers
for one organization member, and publishes `batching.trigger-batch.ready.v1`.
One inbound webhook therefore does not have to become one agent turn.

This slice combines pluggable urgency classification with an adaptive,
deterministic scheduler:

- ordinary app triggers enter the `digest` lane while urgent triggers can
  bypass it through the `immediate` lane;
- five events is the cold-start target, not a permanent threshold;
- fast and slow time-decayed traffic estimates adapt the target per member;
- every trigger receives persisted soft and hard deadlines on arrival;
- the hard deadline is never extended by later traffic or policy changes;
- cadence offsets are stable per member, avoiding one global hourly stampede;
- one published batch carries at most twenty triggers;
- each original EventBridge event ID is stored as the idempotency key;
- trigger payloads are preserved; an oversized group becomes multiple events
  rather than a truncated event, and a single trigger too large for any batch
  (encoding can grow a payload apps accepted past the budget) goes without its
  data, flagged `data_omitted`, the way apps sends one it cannot carry, rather
  than holding every trigger queued behind it;
- member and organization deletion events erase pending trigger data,
  including batches completed but not yet published, and are remembered for
  a month, so a trigger apps received before the removal that the bus
  delivers after it is refused instead of starting a new group; a member
  added back later is not held up by what was forgotten;
- a member disconnecting an app (`apps.connection.removed.v1`) or removing a
  trigger (`apps.trigger.unsubscribed.v1`) drops what it reported that is
  still waiting, and is remembered the same way, so it no longer wakes the
  agent; batches already completed are left alone;
- a published batch's payloads are blanked from the outbox once it is on the
  bus, as apps does with its own trigger events.

The scheduler is not a leaky or token bucket. It estimates arrivals with a
15-minute fast half-life and a 90-minute slow half-life, predicts how many events
fit in one cadence, and fixes the resulting target and deadlines onto each
event. The oldest persisted promise wins for a group. A burst, a higher
priority, or reaching the target can only release the group earlier. Silence
requires no new webhook: the database worker claims the partial group when its
deadline arrives. Policy decisions are stamped `adaptive-ewma.v2`.
The consumer, deadline sweeper, and outbox dispatcher run continuously for the
full 24-hour day; member presence or working hours never pause delivery. The
consumer handles each receive's messages (at most ten) side by side, so one
waiting on the classifier holds up none of the others, removals included.

With `TYPESAFE_API_KEY` set, the classifier calls TypeSafe's System One API once
per trigger. A trigger already recorded, or from something removed since, is
screened out before it is classified, so the redelivery of a recorded trigger
never reaches TypeSafe again. It asks two parallel questions: a Choice between `immediate` and
`digest`, and a Noul for the probability that prompt attention is required.
An event takes the immediate lane only when both its immediate probability and
Choice confidence clear their configured gates. Otherwise the Noul probability
and the Choice's immediate probability are combined by taking the larger value
as its digest priority. The adapter defaults to the moving `jev-latest`
alias and records the versioned model returned by TypeSafe in every decision.

Jev receives toolkit, trigger name, omission state, and at most a 48 KiB
projection of the app payload. This projection never replaces or truncates the
payload stored for the agent. Provider failures become a fallback digest at
`FallbackPriority` (0.4): the event stays durable and its soft deadline comes
forward, but its pressure stays under the smallest target, so an outage cannot
create one agent turn per webhook. After five failures in a row batching stops
asking TypeSafe for a minute and takes the fallback at once, so an outage does
not cost every trigger its timeout; then one trigger asks again, and a success
resumes classification. Opening and closing are each logged once. With no API
key, classification remains the deterministic digest policy.

The service publishes through the shared transactional outbox. Claiming a due
group and writing its output event are separate transactions connected by a
lease: a crashed replica releases nothing permanently, and another replica can
claim the group after the lease expires. Completing a claim and writing the
outbox row happen in one transaction.

## Event contract

Source: `oryxa.batching`

Detail type: `batching.trigger-batch.ready.v1`

The detail contains a batch ID, organization and member IDs, lane, event count,
adaptive target, pressure, deadlines, flush reason, policy version,
first/last receipt times, ready time, and the original triggers. Each trigger
also records classifier name, priority, schedule, and decision reason so both
routing and timing remain auditable.

Consumers must deduplicate with the outer event envelope's `event_id`, as with
the other Oryxa events.

## Provision and run

Provision the service-owned SQS queue, dead-letter queue, EventBridge rule, and
resource policies:

```sh
go run ./batching/infra/events/cmd/provision
```

Set `BATCHING_EVENTS_QUEUE_URL` to the printed queue URL. The provisioner
subscribes to app triggers, app connection and trigger removals, and auth
member/organization deletion events, and configures a target DLQ for
EventBridge delivery failures. Re-run it after upgrading so the rule carries
the app removals.

Useful controls are:

```text
BATCHING_BATCH_SIZE=5
BATCHING_MAX_WAIT=1h
BATCHING_LARGEST_BATCH=20
BATCHING_SWEEP_INTERVAL=1s
BATCHING_CLAIM_LEASE=1m
BATCHING_GROUPS_PER_SWEEP=8
TYPESAFE_API_KEY=
TYPESAFE_BASE_URL=https://api.typesafe.ai
BATCHING_JEV_MODEL=jev-latest
BATCHING_JEV_TIMEOUT=3s
BATCHING_JEV_IMMEDIATE_PROBABILITY=0.85
BATCHING_JEV_MIN_CONFIDENCE=0.60
```

`BATCHING_BATCH_SIZE` is the cold-start prior. `BATCHING_MAX_WAIT` is the
periodic hard cadence: ordinary traffic may release earlier, but never later.
The service rejects values over `90m`; the default is `1h`, leaving operational
headroom beneath the product's absolute delivery ceiling.
`BATCHING_LARGEST_BATCH` must be at least the cold-start target, and the claim
lease must exceed the sweep interval. `BATCHING_JEV_TIMEOUT` may be at most
`10s`, so a message is always handled well within the queue's 60-second
visibility timeout and never handled twice for being slow. At least two groups are handled per sweep
so immediate traffic cannot consume every worker slot and starve due digests.

The current agents service still consumes app triggers directly. Do not route
the ready event to agents until that consumer is switched off, or the same app
activity can wake an agent twice.

## Known gaps

Found in review before the first merge, and not yet addressed:

- **Setting `TYPESAFE_API_KEY` is a data decision.** It sends up to 48 KiB of
  every app payload — mail bodies, direct messages — to TypeSafe, for every
  organization, with no per-organization opt-in. Leave it unset until that
  sharing is approved. `TYPESAFE_BASE_URL` also accepts `http://`.
- **Throughput is bounded per replica**: at most `BATCHING_GROUPS_PER_SWEEP`
  groups per sweep interval, so the hard cadence holds only below that rate.
