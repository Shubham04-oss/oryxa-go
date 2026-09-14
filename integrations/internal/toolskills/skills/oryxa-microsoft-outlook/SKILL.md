---
name: oryxa-microsoft-outlook
description: Use only with Oryxa Microsoft authorization or Outlook tools for approved mail operations and new-email subscriptions.
---

# Oryxa Microsoft Outlook

This is a call-scoped skill. The Oryxa tool adapter or MCP host loads it only
for a bound Microsoft authorization or Outlook tool. The user's current
request takes precedence over this guidance.

## Tool-call contract

- Use the Oryxa integration tool, not Microsoft Graph or Activepieces
  directly. Oryxa owns tenant validation, refresh rotation, Graph subscription
  state, idempotency, and credential-free events.
- Microsoft uses one provider-level member grant across Outlook, Calendar,
  OneDrive, Teams, and SharePoint. `available` means the member must authorize; `connected` supplies
  the shared `connection_id` and safe tenant/account identity.
- Never request or pass access tokens, refresh tokens, Entra client secrets,
  Graph `clientState`, subscription secrets, or raw webhook bodies.
- Mail writes are external side effects. Preserve the user's recipients and
  content, and use one stable 8-200 character idempotency key. Never change the
  key merely because the provider outcome is uncertain.
- `attachments` are Oryxa `{file_id}` refs, max 5. File upload/download use the
  same refs. Host-restricted `custom-api-call` may use `/me/...` or
  `https://graph.microsoft.com/...` only. Approval waitpoints are not approved.
  File actions require `FILES_URL` or they fail with `files_unavailable`.

## Approved actions

### `send-email`

Input `props` requires non-empty `recipients`, non-blank `subject`,
`bodyFormat` (`text|html`), and `body`. Optional `ccRecipients` and
`bccRecipients` are address arrays.

The connection needs `Mail.Send`. Optional `attachments` are
`[{"file_id":"file_..."}]`. Success returns only
`{"accepted":true}` and publishes
`integrations.microsoft.outlook-email-accepted.v1`. Microsoft Graph's sendMail
operation does not provide a message ID here; report acceptance, not delivery
or receipt.

### `reply-email`

Input `props` requires `messageId`, `replyBody`, and `bodyFormat`
(`text|html`). Optional `draft` defaults to false; `ccRecipients` and
`bccRecipients` are address arrays.

The connection needs `Mail.ReadWrite` and `Mail.Send`. A sent reply publishes
`integrations.microsoft.outlook-email-accepted.v1`. A draft (`draft: true`)
publishes `integrations.microsoft.outlook-draft-created.v1` and returns the
draft id.

### `create-draft-email`

Same compose fields as `send-email`. The connection needs `Mail.ReadWrite`.
Success returns the draft id and publishes
`integrations.microsoft.outlook-draft-created.v1`.

### `send-draft-email`

Input `props` requires `messageId`. The connection needs `Mail.Send`. Success
returns `{accepted:true,id}` and publishes
`integrations.microsoft.outlook-email-accepted.v1`.

### `forward-email`

Input `props` requires `messageId` and non-empty `recipients`. Optional
`comment` is prepended by the piece. The connection needs `Mail.ReadWrite` and
`Mail.Send`. Success returns the message id and publishes
`integrations.microsoft.outlook-email-forwarded.v1`.

### `add-label-to-email` / `remove-label-from-email`

Input `props` requires `messageId` and a non-empty `categories` array. The
connection needs `Mail.ReadWrite`. Success returns the message id and publishes
`integrations.microsoft.outlook-email-mutated.v1`. Category names are not
persisted in the result or event.

### `move-email-to-folder`

Input `props` requires `messageId` and `destinationFolderId`. The connection
needs `Mail.ReadWrite`. Success returns the message id and folder id and
publishes `integrations.microsoft.outlook-email-mutated.v1`.

### `find-email`

Input `props` requires `searchQuery` (no embedded quotes). Optional `folderId`
and `top` (1-100; omit for the piece default). The connection needs
`Mail.Read`, which is also satisfied by `Mail.ReadWrite`. Expect at most 100
items with id, subject, and received time, plus `found` and `truncated`.
Bodies, senders, recipients, attachments, and next-page URLs are not returned.
This is a lookup: it does not publish a domain event.

### `custom-api-call`

Input `props` requires `url` (`/me` or `https://graph.microsoft.com/...`) and
`method`. Optional `headers`, `query`, `body_type` (`none|json|form_data|raw`),
and `body`. Do not send `Authorization`. The connection needs `User.Read`.
Returns HTTP `status` plus `body` only; headers and tokens are stripped. This
is a lookup: it does not publish a domain event.

### `download-attachment`

Input `props` requires `messageId`. The connection needs `Mail.Read`. Success
returns up to 5 `{name, file_id}` records for stored attachments. Bytes and
Graph `contentBytes` are not returned. This is a lookup: it does not publish a
domain event.

## Approved trigger: `new-email`

Input `props` must be `{}`. The connection needs `Mail.Read`. Oryxa creates a
per-connection Microsoft Graph subscription for newly created Inbox messages,
validates the opaque callback and encrypted `clientState`, renews the lease,
and queues durable cleanup when disabled or disconnected.

Expect at-least-once `integrations.microsoft.outlook-email-received.v1`
events containing only stable resource/change metadata. Message subject, body,
sender, recipients, attachments, tokens, and raw Graph payloads are not
published. Consumers must deduplicate by event ID.

## Failure handling

- `scope_required` or `reauthorization_required`: reconnect the Microsoft grant
  with `Mail.Send`, `Mail.Read`, or `Mail.ReadWrite` as appropriate. Existing
  connections created before mail write scopes were requested must reconnect.
- `action_in_progress`: do not send another message.
- `action_failed`: a new key may duplicate mail and requires explicit user
  retry intent.
- Trigger disable is local-first; remote Graph deletion may continue through a
  durable cleanup job until the maximum lease boundary.
- Provider errors are deliberately opaque; never expose or reconstruct Graph
  response content.
