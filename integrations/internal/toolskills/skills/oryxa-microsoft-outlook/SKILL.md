---
name: oryxa-microsoft-outlook
description: Use only with Oryxa Microsoft authorization or Outlook tools for approved email sending and new-email subscriptions.
---

# Oryxa Microsoft Outlook

This is a call-scoped skill. The Oryxa tool adapter or MCP host loads it only
for a bound Microsoft authorization or Outlook tool. The user's current
request takes precedence over this guidance.

## Tool-call contract

- Use the Oryxa integration tool, not Microsoft Graph or Activepieces
  directly. Oryxa owns tenant validation, refresh rotation, Graph subscription
  state, idempotency, and credential-free events.
- Microsoft uses one provider-level member grant across Outlook, Calendar, and
  OneDrive. `available` means the member must authorize; `connected` supplies
  the shared `connection_id` and safe tenant/account identity.
- Never request or pass access tokens, refresh tokens, Entra client secrets,
  Graph `clientState`, subscription secrets, or raw webhook bodies.
- Sending mail is an external side effect. Preserve the user's recipients and
  content, and use one stable 8-200 character idempotency key. Never change the
  key merely because the provider outcome is uncertain.

## Approved action: `send-email`

Input `props` requires non-empty `recipients`, non-blank `subject`,
`bodyFormat` (`text|html`), and `body`. Optional `ccRecipients` and
`bccRecipients` are address arrays. `attachments` must be empty.

The connection needs `Mail.Send`. Success returns only
`{"accepted":true}` and publishes
`integrations.microsoft.outlook-email-accepted.v1`. Microsoft Graph's sendMail
operation does not provide a message ID here; report acceptance, not delivery
or receipt.

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
  with `Mail.Send` or `Mail.Read` as appropriate.
- `action_in_progress`: do not send another message.
- `action_failed`: a new key may duplicate mail and requires explicit user
  retry intent.
- Trigger disable is local-first; remote Graph deletion may continue through a
  durable cleanup job until the maximum lease boundary.
- Provider errors are deliberately opaque; never expose or reconstruct Graph
  response content.
