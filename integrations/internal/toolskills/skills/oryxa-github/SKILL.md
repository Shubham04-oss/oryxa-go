---
name: oryxa-github
description: Use only with Oryxa GitHub tools for member connection status and the approved issue or pull-request actions.
---

# Oryxa GitHub

This is a call-scoped skill. The Oryxa tool adapter or MCP host loads it only
for a bound GitHub tool. The user's current request takes precedence over this
guidance.

## Tool-call contract

- Use the Oryxa integration tool, not GitHub APIs or an Activepieces action
  directly. The tool owns organization/member isolation, credentials,
  idempotency, and domain events.
- Treat the tool schema and member catalog as authoritative. `available` means
  this member still needs GitHub authorization; `connected` supplies the
  member-owned `connection_id`.
- Resolve the exact repository owner and name. Never infer a repository from a
  similarly named organization or from prior conversation without confirmation.
- Never request or pass OAuth tokens or client secrets. Registering/rotating a
  BYOC OAuth app is an admin task, not an action prerequisite the agent may
  perform silently.
- All approved actions currently require the exact `repo` scope. A grant with
  only `public_repo` does not satisfy that action contract.
- Use one stable 8-200 character idempotency key per logical action, including
  reads. Reuse it only for identical input.

## Approved actions

### `create-issue`

Input `props` requires `owner`, `repo`, and non-empty `title`. Optional `body`,
`labels`, and `assignees` are bounded lists. This creates an external issue;
do not add labels, assignees, or content the user did not request. Expect safe
issue metadata (`number`, `title`, `state`, `url`, author, labels) and
`integrations.github.issue-created.v1`.

### `comment-on-issue`

Input `props` requires `owner`, `repo`, positive `issue_number`, and non-empty
`body`. This creates an external comment. Expect `comment_id` and `url`, plus
`integrations.github.issue-comment-created.v1`.

### `get-issue`

Input `props` requires `owner`, `repo`, and positive `issue_number`. Expect
safe issue metadata only; comments and arbitrary provider response fields are
not returned.

### `list-pull-requests`

Input `props` requires `owner` and `repo`. Optional filters are `state`
(`open|closed|all`), `head`, `base`, `sort`
(`created|updated|popularity|long-running`), and `direction` (`asc|desc`).
Expect at most 100 safe PR records with number, title, state, URL, draft flag,
and author.

## Triggers

No GitHub trigger is approved. Do not emulate one with polling, broad API
queries, or a direct webhook. Return that the requested operation is not in the
catalog.

## Failure handling

- `scope_required` or `reauthorization_required`: request reconnection with the
  required scope.
- `operation_disabled`: stop; installed package code does not make an action
  available.
- `idempotency_conflict`: the key was used with different input; do not alter
  the call silently.
- `action_in_progress`: wait or report progress, without creating a new call.
- `action_failed`: a new key is an explicit retry and needs user intent.
- Provider errors are opaque by design; do not disclose or reconstruct raw
  GitHub failure payloads.
