---
name: oryxa-gmail
description: Use only with Oryxa Google authorization or Gmail tools for approved mail actions and inbound new-email.
---

# Oryxa Gmail

This is a call-scoped skill. The Oryxa tool adapter or MCP host loads it only
for a bound Google authorization or Gmail tool. The user's current request
takes precedence over this guidance.

## Tool-call contract

- Use the Oryxa integration tool, never Gmail APIs or an Activepieces action
  directly. Oryxa owns member authorization, credential refresh, idempotency,
  and the audit event.
- Google uses one provider-level member connection across Gmail, Calendar,
  Drive, Docs, and Sheets. `available` requires authorization; `connected` supplies the shared
  `connection_id`. Each operation still enforces its own granted scope.
- Never request or pass access tokens, refresh tokens, OAuth secrets, or raw
  MIME. The approved actions accept structured content only.
- Sending or forwarding email is an external side effect. Preserve the user's
  recipients and content exactly; do not add recipients or rewrite meaning
  without explicit intent.
- Use one stable 8-200 character idempotency key per logical action, including
  reads. Reuse it only with identical input. Never use a new key merely because
  the outcome is uncertain.
- Attachments on send/reply/draft use Oryxa `{file_id}` refs, max 5. `get-email`
  and `find-email` also store attachments as `{file_id}` and never return MIME.
  Host-restricted `gmail-custom-api-call` may use `/path` or
  `https://gmail.googleapis.com/...` only. Approval waitpoints and
  `users.watch` stop remain disabled. Reconnect existing grants to pick up
  `gmail.readonly`, `gmail.compose`, and `gmail.modify` before using the new
  read, draft, and label tools. `gmail.modify` satisfies those read/compose
  scopes and `gmail.send`. File actions require `FILES_URL` or they fail with
  `files_unavailable`.

## Approved actions

Public names are kebab-case. Lists return at most 100 safe records plus
`truncated`. Lookups do not publish domain events. Writes publish identity-only
events without subjects, bodies, or recipients.

### Send, reply, and forward

`send-email`, `reply-email`, `forward-email`, `send-draft-email`.

`send-email` requires `receiver`, non-blank `subject`, `body_type`
(`plain_text|html`), and `body`. Optional `cc`, `bcc`, `reply_to`,
`sender_name`, `from`, `in_reply_to`, and `attachments` as
`[{"file_id":"file_..."}]` (max 5). `draft` must be false. `reply-email`
requires `message_id`, `reply_type` (`reply|reply_all`), `body_type`, and
`body`; optional `attachment` is `{file_id}`. `forward-email` requires
`message_id` and `receiver`. Send and reply publish
`integrations.google.gmail-message-sent.v1`; forward publishes
`integrations.google.gmail-email-forwarded.v1`. Success means Gmail accepted
the write, not that a recipient read it.

`get-email-attachment` requires `message_id` and `attachment_id`. Optional
`file_name`. Returns `{file_id, filename}` for the stored object; no event.

`get-email` requires `message_id`. Returns id, from/to/subject/date, and
attachment `{file_id, filename}` only — never `text`, `html`, or raw MIME. No
event.

`find-email` accepts optional `from`, `to`, `subject`, `content`,
`has_attachment`, `attachment_name`, string `label`, `category`
(`primary|social|promotions|updates|forums|reservations|purchases`),
`after_date`/`before_date` (RFC3339), `include_spam_trash`, and `max_results`
(cap 100). String labels are wrapped for the piece. Same safe message shape as
`get-email`. No event.

`gmail-custom-api-call` requires `url` (`/users/me/...` or
`https://gmail.googleapis.com/...`) and `method`. Optional `headers`, `query`,
`body_type`, `body`. Do not send `Authorization`. Returns status plus body
only. No event. Needs `gmail.readonly` (`gmail.modify` satisfies it).

### Drafts

`create-draft-email`, `update-draft-email`, `get-draft-email`, `list-drafts`,
`delete-draft-email`. Create/update use the send field shapes plus optional
`thread_id`; `draft_id` is required on update/get/delete/send-draft.
Optional `attachments` are `{file_id}` refs. Create publishes
`integrations.google.gmail-draft-created.v1`; update and delete publish
`integrations.google.gmail-email-mutated.v1`. Get/list omit payload and snippet.

### Threads, labels, and mailbox

`get-thread`, `list-threads`, `list-labels`, `get-label`, `create-label`,
`add-label-to-email`, `remove-label-from-email`, `archive-email`, `get-profile`,
`list-history`.

`get-thread` requires `thread_id` and `format` of `metadata` or `minimal` (never
`full` or `raw`). `list-threads` accepts optional Gmail `query` and `label_ids`.
Label mutations take string `label` IDs, not dropdown objects. Label and
archive writes publish `integrations.google.gmail-email-mutated.v1`.

## Approved trigger: `new-email`

Input `props` must be an empty object. Oryxa creates a Gmail Inbox `users.watch`
against the operator Pub/Sub topic. Existing connections without
`gmail.metadata` (or a grant that implies it) must reconnect before this
trigger can be enabled.

Expect at-least-once `integrations.google.gmail-message-received.v1` with
trigger, connection, and delivery identity only. Subjects, bodies, and headers
are never published. Do not emulate a receive trigger with inbox polling or a
direct Gmail API read.

## Failure handling

- `scope_required` or `reauthorization_required`: ask the user to reconnect the
  Google grant with the required Gmail scopes.
- `action_in_progress`: do not send again under a new key.
- `action_failed`: a retry with a new key may duplicate mail and requires an
  explicit user decision.
- Provider errors are intentionally opaque and must not be expanded with raw
  response data or credential details.
