---
name: oryxa-slack
description: Use only with Oryxa Slack tools for member connection status, approved message and reaction actions, channel lookup, or the new-message trigger.
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

## Approved actions

### `post-message`

Input `props`:

- `channel` is required and must be a channel ID, not a guessed display name.
- Supply non-empty `text` or a non-empty `blocks` array. `text` is limited to
  40,000 characters and `blocks` to 50 items/100 KiB.
- Optional: `threadTs`, `replyBroadcast`, and `unfurlLinks`.
- `replyBroadcast=true` requires `threadTs`. `sendAsUser` is unsupported.

Expect `channel_id`, `message_timestamp`, and optionally `thread_timestamp`.
A success publishes `integrations.slack.message-sent.v1`. Do not claim Slack
delivered or displayed the message beyond the provider-accepted result.

### `add-reaction`

Input `props` requires `channel`, message `ts`, and an emoji name in `reaction`
without colons or whitespace. `reactAsUser` is unsupported. Expect the same
channel/message identifiers and reaction in the safe result, plus
`integrations.slack.reaction-added.v1`.

### `find-channel`

Input `props` requires `name`; a leading `#` is accepted. Optional
`excludeArchived` controls archived results. Expect safe channel metadata such
as ID, name, privacy, archive, membership, and member count. This is a lookup
and publishes no domain side-effect event.

## Approved trigger

`new-message` is an organization app-level webhook trigger. Input `props` may
contain `ignoreBots`; prefer `true` unless the user explicitly needs bot
messages. The organization admin must configure the Slack webhook route and
signing secret first. The connected grant needs history access for the relevant
public/private channel type. Expect authenticated, deduplicated, at-least-once
events; downstream consumers must deduplicate by event ID.

## Failure handling

- `scope_required` or `reauthorization_required`: tell the user Slack must be
  reconnected with the missing scopes; do not seek a bypass.
- `webhook_route_required`: an admin must configure the app-level route before
  enabling `new-message`.
- `action_in_progress`: do not submit another action with a different key.
- `action_failed`: use a new idempotency key only after the user asks to retry.
- Provider failures are intentionally opaque. Never infer success from an
  error and never expose raw provider payloads.
