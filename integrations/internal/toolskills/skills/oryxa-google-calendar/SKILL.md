---
name: oryxa-google-calendar
description: Use only with Oryxa Google Calendar tools for approved event, free/busy, and inbound calendar-event-changed operations.
---

# Oryxa Google Calendar

This is a call-scoped skill. The Oryxa tool adapter or MCP host loads it only
for a bound Google Calendar tool. The user's current request takes precedence
over this guidance.

## Tool-call contract

- Use the Oryxa integration tool, not Google APIs or Activepieces directly.
- Google uses one provider-level connection for Gmail, Calendar, and Drive.
  Obtain the member-owned `connection_id` from a `connected` catalog record;
  `available` means authorization is still required.
- Never request or pass OAuth credentials. The tool refreshes and decrypts the
  grant internally and enforces the exact Calendar scope.
- Confirm the calendar, timezone-bearing RFC3339 times, title, attendees, and
  notification intent before creating or updating an event. Do not infer missing
  business details when that could invite people or put an event on the wrong
  calendar.
- Use one stable 8-200 character idempotency key per logical request and reuse
  it only with identical input.
- Custom API call and piece polling/webhook triggers remain disabled. Native
  `calendar-event-changed` stays the inbound path. `calendar.events` satisfies
  `calendar.readonly`.

## Approved actions

Public names are kebab-case. Lists return at most 100 safe records plus
`truncated`. Lookups do not publish domain events. Writes publish identity-only
events without descriptions, attendee lists, or conferencing payloads. Safe
event records may include ID, status, summary, URL, start/end, timezone, and
update time.

### Events

`create-calendar-event`, `update-calendar-event`, `delete-calendar-event`,
`get-calendar-event`, `list-calendar-events`, `add-calendar-attendees`,
`remove-calendar-attendee`, `create-quick-event`, `move-calendar-event`,
`import-calendar-event`, `list-recurring-event-instances`,
`search-calendar-events`.

Create requires `calendar_id`, non-empty `title`, RFC3339 `start_date_time`,
and `send_notifications` (`all|externalOnly|none`). List requires `calendar_id`
and a non-empty `event_types` list from `default`, `focusTime`, `outOfOffice`,
and `workingLocation`. Delete/get/update/move take `event_id`. Search requires
`query` plus a start/end window. Create, quick-add, and import publish
`integrations.google.calendar-event-created.v1`; other writes publish
`integrations.google.calendar-event-updated.v1` or
`integrations.google.calendar-event-deleted.v1`.

### Free/busy and calendars

`find-busy-periods`, `find-free-slots`, `list-calendars`, `get-calendar`,
`get-calendar-colors`, `list-calendar-settings`.

Busy/free tools require `calendar_ids` and an RFC3339 window. Results are times
only. Calendar list/get omit descriptions.

## Approved trigger: `calendar-event-changed`

Input `props` must be an empty object. Oryxa watches the connected primary
calendar through a per-connection HTTPS channel. `calendar.events` satisfies
the required `calendar.readonly` grant.

Expect at-least-once `integrations.google.calendar-event-changed.v1` with
trigger, connection, and delivery identity only. Titles, attendees, location,
and body are never published. Do not create polling schedules or provider
webhooks through another tool.

## Failure handling

- `scope_required` or `reauthorization_required`: reconnect the shared Google
  grant with the required Calendar access.
- `idempotency_conflict`: keep the original call unchanged or create a new key
  only for a separately authorized action.
- `action_in_progress`: do not create a duplicate event.
- `action_failed`: retrying creation with a new key may duplicate an event;
  obtain explicit user intent.
- Provider errors are opaque by design.
