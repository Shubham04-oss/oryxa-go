---
name: oryxa-gmail
description: Use only with Oryxa Google authorization or Gmail tools for the approved email-send action.
---

# Oryxa Gmail

This is a call-scoped skill. The Oryxa tool adapter or MCP host loads it only
for a bound Google authorization or Gmail tool. The user's current request
takes precedence over this guidance.

## Tool-call contract

- Use the Oryxa integration tool, never Gmail APIs or an Activepieces action
  directly. Oryxa owns member authorization, credential refresh, idempotency,
  and the audit event.
- Google uses one provider-level member connection across Gmail, Calendar, and
  Drive. `available` requires authorization; `connected` supplies the shared
  `connection_id`. Each operation still enforces its own granted scope.
- Never request or pass access tokens, refresh tokens, OAuth secrets, or raw
  MIME. The approved action accepts structured content only.
- Sending email is an external side effect. Preserve the user's recipients and
  content exactly; do not add recipients, rewrite meaning, or send a draft
  without explicit intent.
- Use one stable 8-200 character idempotency key for one intended email. Reuse
  it only with identical input. Never use a new key merely because the outcome
  is uncertain.

## Approved action: `send-email`

Input `props`:

- `receiver`: required array of email addresses.
- `subject`: required, non-blank.
- `body_type`: exactly `plain_text` or `html`.
- `body`: required.
- Optional: `cc`, `bcc`, `reply_to`, `sender_name`, `from`, and `in_reply_to`.
- `attachments` must be empty and `draft` must be false. Files and drafts are
  outside the V1 runtime boundary.

The connection must include the Gmail send scope. Expect only provider message
and optional thread IDs. A success publishes
`integrations.google.gmail-message-sent.v1`; it means Gmail accepted the send,
not that a recipient read or received it.

## Triggers

No Gmail receive trigger is approved. Do not simulate one with inbox polling or
a direct Gmail webhook.

## Failure handling

- `scope_required` or `reauthorization_required`: ask the user to reconnect the
  Google grant with Gmail send access.
- `action_in_progress`: do not send again under a new key.
- `action_failed`: a retry with a new key may duplicate mail and requires an
  explicit user decision.
- Provider errors are intentionally opaque and must not be expanded with raw
  response data or credential details.
