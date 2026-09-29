# Workspace lifecycle and retention

Workspace consumes signed `auth.member.removed.v1`,
`auth.organization.deleted.v1`, and `teams.team.deleted.v1` events through
`WORKSPACE_EVENTS_QUEUE_URL`. Its subscription list also generates the queue's
EventBridge rule. The host's event signature policy applies; production should
use `ORYXA_EVENT_SIGNATURES=enforce` with the publishing services' signing keys.
A production host requires the queue URL before starting workspace.

Member departure removes current external bindings by stable user ID and drops
that member's current directory entries, people entry, team membership/lead
references, ownership facts, chart relationships and executive reference. Team
deletion removes its binding, current team declaration and directory group facts.
Cleanup shares the per-organization apply lock and commits its receipt, mapping
removal, new current version and approval invalidation atomically. Busy applies
leave the event on the queue for retry. Duplicate delivery cannot undo a later
rebind. A reference-only event older than the latest administrator-applied file
does not delete newer same-email or same-name facts; stable-ID matches still
remove the old binding. Replanning also validates retained facts against the
live directory, including when an event is delayed.

## Retention decision

Previously applied versions remain immutable audit records, including historical
identity facts. Only the organization's owners/admins may read workspace history
or use it to prepare a reviewed rollback. Keeping those records makes earlier
privileged configuration decisions reviewable; they are not the source of current
membership or current policy. A rollback is replanned against live resource IDs
and cannot silently restore a departed binding.

Organization deletion purges current configuration, all historical versions,
external bindings, reviewed plan payloads, apply progress and event receipts.
Only the opaque organization ID and deletion timestamp remain as a tombstone;
local database guards reject delayed writes that would recreate the deleted
organization's workspace data. A periodic sweep repeats deletion for any writes
already in flight when the tombstone was committed.

Reviewed plan payloads are physically purged after expiration at startup and
every fifteen minutes, even if no lifecycle event arrives. Opaque event-ID
receipts have a thirty-day retention period, longer than accepted event signature
age; they contain no event payload. The sweeper logs failures for operator action.

## Provision before releasing this version

For an existing production installation:

1. Apply the updated `deploy/production` configuration with the existing running
   image. Adding `workspace` to `local.queue_services` supplies the new queue
   name/URL, task environment and task-role consume permission through the shared
   queue maps.
2. Run `deploy/scripts/provision-app.sh` to create/update the workspace queue,
   dead-letter queue and exact-source EventBridge rule before releasing the new
   application image. No Cognito configuration change is needed for this queue.
3. Release the new image and run workspace migrations as usual. Confirm the
   lifecycle consumer starts with its queue and event signature enforcement.

The standalone provisioner is
`go run ./internal/workspace/infra/events/cmd/provision`. Supply `AWS_REGION`,
`EVENT_BUS_NAME`, and `WORKSPACE_EVENTS_QUEUE_NAME`; use its returned `queue_url`
as `WORKSPACE_EVENTS_QUEUE_URL`. The queue policy admits only the designated rule
and account. Local emulator examples are in `.env.example`.
