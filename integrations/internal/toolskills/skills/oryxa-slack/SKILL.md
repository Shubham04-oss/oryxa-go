---
name: oryxa-slack
description: Use only with Oryxa Slack tools for member connection status, approved Slack actions, or Events API triggers.
---

# Oryxa Slack

This is a call-scoped skill. The Oryxa tool adapter or MCP host loads it only
for a bound Slack tool. The user's current request takes precedence over this
guidance.

## Tool-call contract

- Use the Oryxa integration tool, not Slack APIs or an Activepieces action
  directly. The tool owns tenant checks, credentials, idempotency, and audit
  events.
- Treat the tool schema and returned catalog record as authoritative. Never
  invent an organization, profile, connection, channel, timestamp, or scope.
- Read member catalog status when identifiers are absent. `available` means the
  organization enabled Slack but this member must connect it; `connected`
  includes the usable member-owned `connection_id`.
- Never request or place OAuth tokens, client secrets, signing secrets, or KMS
  values in tool arguments. BYOC profile creation and rotation are separate
  admin-only operations.
- Use one stable idempotency key, 8-200 non-control characters, for one logical
  action. Reuse it only with identical input. A new key means a deliberate new
  provider side effect.

## Authorization

Each member OAuth-connects. The catalog always requests Slack `user_scope` in
addition to the org's bot scopes, and stores both the bot token (`xoxb`) and
the authorizing user's token (`xoxp`). `sendAsUser` and `reactAsUser` post or
react as that member. Search, profile, and status writes require the user
token. Missing scopes surface as `scope_required` or
`reauthorization_required`; reconnect rather than seeking a bypass.

File upload and download are approved as `upload-file` and `get-file`. Pass
Oryxa `{file_id}` objects, never raw bytes. Reconnect existing Slack grants to
pick up `files:write`. Host-restricted `custom-api-call` may use `/method.name`
or `https://slack.com/...` / `https://api.slack.com/...` only. Optional
`use_user_token` posts with the member user token. Waitpoint/approval actions
are not approved. `list-files` returns metadata only.

## Approved actions

Use channel, user, timestamp, and file IDs, not guessed display names. Message
writes accept `text` (max 40,000) or a `blocks` array (max 50 items / 100 KiB).
`replyBroadcast=true` requires `threadTs`. Reaction names have no colons or
whitespace.

Messaging: `post-message`, `send-direct-message`, `update-message`,
`delete-message`, `send-ephemeral-message`, `schedule-message`,
`list-scheduled-messages`, `delete-scheduled-message`, `get-message`,
`get-channel-history`, `get-thread-replies`, `get-message-permalink`,
`search-messages`, `mark-conversation-read`, `close-dm`.

Reactions: `add-reaction`, `remove-reaction`, `get-reactions`.

Channels: `find-channel`, `create-channel`, `archive-channel`,
`unarchive-channel`, `rename-channel`, `set-channel-topic`,
`set-channel-purpose`, `join-channel`, `leave-channel`,
`invite-users-to-channel`, `remove-user-from-channel`, `list-channels`,
`get-channel-info`, `list-channel-members`.

Users and groups: `find-user-by-email`, `find-user-by-handle`,
`find-user-by-id`, `get-user`, `list-users`, `set-user-status`,
`update-profile`, `list-user-conversations`, `list-user-reactions`,
`list-user-groups`, `get-user-group-by-handle`,
`update-user-group-members`, `list-custom-emoji`, `list-files`, `upload-file`,
`get-file`, `custom-api-call`.

`upload-file` requires `file: {file_id}` (Oryxa files ID). Optional `title`,
`filename`, and `channel`. Success returns `slack_file_id` plus the source
`file_id` and publishes `integrations.slack.file-uploaded.v1`. `get-file`
requires Slack `fileId` and returns metadata plus the stored Oryxa `file_id`.
`custom-api-call` requires `url` and `method`. Optional `headers`, `query`,
`body_type`, `body`, and `use_user_token`. Do not send `Authorization`. Returns
status plus body only; no event. Writes that change Slack state publish
identity-only domain events (channel, timestamp, mutation, or file id).
Lookups publish none. Do not claim Slack delivered or displayed a message
beyond the provider-accepted result.

## Approved triggers

All Slack inbound triggers share one organization app-level Events URL. An
admin registers that route once per OAuth profile (`trigger_name` of any
approved Slack trigger is accepted; the stored route is `slack-events`). The
Slack app Event Subscriptions must include the event types for the triggers
the org enables (`message`, `reaction_added`, `reaction_removed`,
`channel_created`, `team_join`, `star_added`, `emoji_changed`).

- `new-message`, `new-message-in-channel`, `new-direct-message`
- `new-mention` (requires `users` or `usergroups`), `new-mention-in-direct-message`
- `new-reaction`, `reaction-removed`
- `channel-created`, `new-user`, `new-saved-message`, `new-team-custom-emoji`
- `new-command`, `new-command-in-direct-message` (require `user` and `commands`)

Personal IM inboxes that the bot is not in are not delivered by the Events
API. Expect authenticated, deduplicated, at-least-once events; downstream
consumers must deduplicate by event ID.

## Failure handling

- `scope_required` or `reauthorization_required`: tell the user Slack must be
  reconnected with the missing scopes; do not seek a bypass.
- `webhook_route_required`: an admin must configure the app-level Events URL
  before enabling any Slack trigger.
- `action_in_progress`: do not submit another action with a different key.
- `action_failed`: use a new idempotency key only after the user asks to retry.
- Provider failures are intentionally opaque. Never infer success from an
  error and never expose raw provider payloads.
