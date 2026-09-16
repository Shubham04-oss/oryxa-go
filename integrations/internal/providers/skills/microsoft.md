# Microsoft

Microsoft Graph delivers changes to a member's mail, calendar or OneDrive to a
subscription integrations creates and renews. A subscription watches one
resource: `me/messages`, one mail folder's messages, `me/events`, or
`me/drive/root`.

## Events

Events are named for what changed and how: `microsoft.message.created`,
`microsoft.event.updated`, `microsoft.drive_item.updated`. Mail and calendar
subscriptions last a week and the drive's a month; integrations renews them
before they lapse, and a subscription Graph removed fails with
`provider_removed` rather than going quiet.

## Working with what arrives

A Graph notification is not the thing that changed: it carries the resource's
ID and little else, so fetch the message or event with the connection before
acting on it. A notification for a message in Drafts is a draft the member is
still writing — not everything created is sent.

Graph delivers at least once and reorders freely. Treat an event as "this
resource changed, look at it now" rather than as a record of what it was.

## Tools

A Graph notification is only a pointer, as above: fetch the actual message or
event — with `microsoft_get_message`, say — before acting on it, never the
notification's payload alone.

Sending mail cannot be taken back once Graph accepts it. Confirm the
recipients, subject and body with the member before calling
`microsoft_send_mail`.

`microsoft_list_messages` and `microsoft_get_message` need the connection's
Mail.Read permission; `microsoft_send_mail` needs Mail.Send;
`microsoft_list_events` and `microsoft_create_event` need
Calendars.ReadWrite. When a tool's permission is missing it fails with
`scope_required` or `forbidden` — say which permission is missing rather than
trying again.

Every timestamp a tool here takes or answers, such as an event's `start` or a
message's `received_at`, is UTC in ISO 8601.
