---
name: oryxa-microsoft-teams
description: Use only with Oryxa Microsoft Teams tools for approved channel, chat, meeting lookup, and host-restricted custom API operations.
---

# Oryxa Microsoft Teams

This is a call-scoped skill. The Oryxa tool adapter or MCP host loads it only
for a bound Microsoft Teams tool. The user's current request takes precedence
over this guidance.

## Tool-call contract

- Use the Oryxa integration tool, not Graph APIs or Activepieces directly.
- Microsoft uses one provider-level member grant across Outlook, Calendar,
  OneDrive, Teams, and SharePoint. Use the `connection_id` from `connected`
  catalog state.
- Never request or pass tokens. Reconnect existing grants for Teams scopes
  (`Channel.Create`, `ChannelMessage.Send`, `Chat.ReadWrite`,
  `ChannelMessage.Read.All`, `TeamMember.Read.All`, transcript/recording
  read). `Channel.Create` satisfies `Channel.ReadBasic.All`.
- Pass team, channel, chat, and user IDs as strings. Approval waitpoints
  (`request_approval_in_channel`, `request_approval_direct_message`) stay
  disabled. Piece polling triggers stay disabled.
- Host-restricted `teams-custom-api-call` may use `/path` or
  `https://graph.microsoft.com/...` only. Do not send `Authorization`.
- Meeting `joinWebUrl` must be public `https`. Transcript text is capped at
  32KiB. Recording results never include content URLs.
- Use one stable 8-200 character idempotency key per logical request.

## Approved actions

Lookups do not publish domain events. Message bodies never appear in results
or events.

### Channels and chat

`create-channel`, `create-private-channel`, `send-channel-message`,
`send-chat-message`, `reply-to-channel-message`,
`create-chat-and-send-message`, `get-chat-message`, `delete-chat-message`,
`get-channel-message`, `find-channel`, `find-team-member`.

`contentType` is `text` or `html`. Find member `searchBy` is `email` or
`name`. Channel creates publish
`integrations.microsoft.teams-channel-created.v1`. Sends publish
`integrations.microsoft.teams-message-sent.v1`. Delete publishes
`integrations.microsoft.teams-message-deleted.v1`.

### Meetings and custom API

`get-meeting-transcript`, `get-meeting-recording`, `teams-custom-api-call`.

`meetingIdentifierType` is `meetingId`, `joinWebUrl`, or `joinMeetingId`.
Lookups and custom API publish no event.

## Failure handling

- `scope_required` or `reauthorization_required`: reconnect the shared
  Microsoft grant with the matching Teams permission.
- Provider errors are opaque.
