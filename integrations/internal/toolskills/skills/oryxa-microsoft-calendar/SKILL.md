---
name: oryxa-microsoft-calendar
description: Use only with Oryxa Microsoft Calendar tools for approved event creation, listing, deletion, and change subscriptions.
---

# Oryxa Microsoft Calendar

This is a call-scoped skill. The Oryxa tool adapter or MCP host loads it only
for a bound Microsoft Calendar tool. The user's current request takes
precedence over this guidance.

## Tool-call contract

- Use the Oryxa integration tool, not Graph APIs or Activepieces directly.
- Microsoft uses one provider-level member grant for Outlook, Calendar,
  OneDrive, Teams, and SharePoint. Obtain the member-owned `connection_id` from `connected` catalog
  state; `available` means authorization is required.
- Never request or pass Microsoft credentials, Graph `clientState`, or
  subscription identifiers from untrusted input.
- Confirm the target calendar, RFC3339 start/end, timezone, and title before a
  create action. Do not infer missing scheduling details that materially
  affect the user's calendar.
- Use one stable 8-200 character idempotency key per logical action and reuse
  it only with identical input.

## Approved actions

### `create-calendar-event`

Input `props` requires `calendarId`, non-blank `title`, RFC3339 `start`, and a
non-empty `timezone`. Optional `end` must be after start; `location` is also
optional. The connection needs `Calendars.ReadWrite`. Expect safe event ID,
HTTPS URL, start/end, and timezone. Success publishes
`integrations.microsoft.calendar-event-created.v1`.

### `list-calendar-events`

Input `props` requires `calendarId`; optional `filter` is passed only through
the reviewed bounded filter field. The connection needs `Calendars.Read`,
which is also satisfied by `Calendars.ReadWrite`. Expect at most 100 safe event
records and `truncated`; subjects, attendees, bodies, and transport fields are
not persisted in the result.

### `delete-calendar-event`

Input `props` requires `calendarId` and `eventId`. The connection needs
`Calendars.ReadWrite`. Success returns those identities and publishes
`integrations.microsoft.calendar-event-deleted.v1`. Titles, attendees, and
bodies are not returned.

## Approved trigger: `calendar-event-changed`

Input `props` must be `{}`. The connection needs `Calendars.Read` (or the
write scope that implies it). Oryxa creates and renews a per-connection Graph
subscription for created, updated, and deleted `/me/events` changes.

Expect at-least-once `integrations.microsoft.calendar-event-changed.v1`
events with stable resource/change metadata only. Consumers must deduplicate
by event ID. Disabling the trigger closes local ingress before durable remote
subscription cleanup.

## Failure handling

- `scope_required` or `reauthorization_required`: reconnect with the required
  Calendar permission.
- `action_in_progress`: do not create a duplicate event.
- `action_failed`: retrying create under a new key requires explicit user
  intent because the provider may have accepted the first request.
- Provider and subscription errors are opaque. Never ask for raw tokens,
  `clientState`, or webhook payloads to work around them.
