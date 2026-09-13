---
name: oryxa-google-calendar
description: Use only with Oryxa Google Calendar tools for approved event creation and listing.
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
  notification intent before creating an event. Do not infer missing business
  details when that could invite people or put an event on the wrong calendar.
- Use one stable 8-200 character idempotency key per logical create or list
  request and reuse it only with identical input.

## Approved actions

### `create-calendar-event`

Input `props` requires `calendar_id`, non-empty `title`, RFC3339
`start_date_time`, and `send_notifications` (`all|externalOnly|none`). Optional
fields include `end_date_time`, `location`, `description`, `colorId`,
`attendees`, guest-permission booleans, and `create_meet_link`. When supplied,
the end must be after the start. Expect safe event metadata: ID, status,
summary, URL, start/end, timezone, and update time. A success publishes
`integrations.google.calendar-event-created.v1`.

### `list-calendar-events`

Input `props` requires `calendar_id` and a non-empty `event_types` list chosen
from `default`, `focusTime`, `outOfOffice`, and `workingLocation`. Optional
`search`, RFC3339 `start_date`/`end_date`, and `singleEvents` narrow the result.
Expect at most 100 safe event records plus `truncated`; descriptions,
attendees, conferencing payloads, and provider transport fields are omitted.

## Triggers

No Google Calendar trigger is approved. Do not create polling schedules or
provider webhooks through another tool.

## Failure handling

- `scope_required` or `reauthorization_required`: reconnect the shared Google
  grant with the required Calendar access.
- `idempotency_conflict`: keep the original call unchanged or create a new key
  only for a separately authorized action.
- `action_in_progress`: do not create a duplicate event.
- `action_failed`: retrying creation with a new key may duplicate an event;
  obtain explicit user intent.
- Provider errors are opaque by design.
