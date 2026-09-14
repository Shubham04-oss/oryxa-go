---
name: oryxa-github
description: Use only with Oryxa GitHub tools for member connection status, approved repository actions, file content through Oryxa file refs, and inbound issue, pull-request, star, push, release, and review triggers.
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
- Repo-scoped actions require `repo`. Profile and public user lookups require
  `read:user`. Organization member listing requires `read:org`; reconnect if
  that grant is missing. `public_repo` does not satisfy `repo`. Creating a gist
  requires `gist`; reconnect existing grants before `create-gist`.
- Repository inbound triggers require `admin:repo_hook` (or a `repo` grant that
  already includes hook administration). Organization-wide triggers require
  `admin:org_hook`.
- Use one stable 8-200 character idempotency key per logical action, including
  reads. Reuse it only for identical input.
- File bytes enter and leave through Oryxa `{file_id}` refs. Do not pass
  base64. Gist content is plain text, not a file ref. Host-restricted
  `custom-api-call` may use `/path` or `https://api.github.com/...` only.
  Dropdown-only duplicates of these tools are not approved. Workflow dispatch
  returns immediately; waiting for completion is not approved.

## Approved actions

Public names are kebab-case. Inputs use `owner`/`repo` strings (or `username` /
`org` where listed). Lists return at most 100 safe records plus `truncated`.
Lookups do not publish domain events. Writes publish identity-only events
without bodies, patches, or emails.

### Issues and labels

`create-issue`, `get-issue`, `list-issues`, `update-issue`, `comment-on-issue`,
`list-issue-comments`, `lock-issue`, `unlock-issue`, `add-labels-to-issue`,
`remove-label-from-issue`, `set-issue-labels`, `list-issue-labels`,
`add-assignees-to-issue`, `remove-assignees-from-issue`, `list-assignees`,
`list-repository-labels`, `create-label`, `update-label`.

Create/comment require title or body as documented on the tool schema. List
filters are optional (`state`, `labels`, `since`, sort). Lock reason is
`off-topic|too heated|resolved|spam`. Create/update issue and comment publish
`integrations.github.issue-created.v1` /
`integrations.github.issue-comment-created.v1`; other issue writes publish
`integrations.github.issue-mutated.v1`. Label writes publish
`integrations.github.label-mutated.v1`.

### Pull requests

`create-pull-request`, `get-pull-request`, `list-pull-requests`,
`update-pull-request`, `merge-pull-request`, `list-pull-request-files`,
`list-pull-request-commits`, `create-pull-request-review`,
`request-pull-request-reviewers`, `create-review-comment`,
`list-pull-request-reviews`, `list-review-comments`.

Create requires `title`, `head`, and `base`. Merge method is
`merge|squash|rebase`. Reviews require `event` (`APPROVE|REQUEST_CHANGES|COMMENT`);
request-changes and comment reviews need `body`. File lists return path and
line stats only (no patches). Create publishes
`integrations.github.pull-request-created.v1`; other PR writes publish
`integrations.github.pull-request-mutated.v1`.

### Repositories, git, and search

`get-repository`, `list-my-repositories`, `list-user-repositories`,
`list-organization-repositories`, `create-repository`,
`create-organization-repository`, `update-repository`, `list-branches`,
`get-branch`, `create-branch`, `delete-branch`, `get-reference`, `list-commits`,
`get-commit`, `compare-commits`, `list-contributors`, `list-languages`,
`list-tags`, `get-repository-tree`, `search-repositories`, `search-issues`,
`search-users`, `search-code`.

Create/update repository publish repository created/mutated events. Branch
create/delete publish `integrations.github.ref-mutated.v1`. Commit and compare
results omit patches. `search-code` takes `q` plus optional `per_page`/`page`
and returns path, sha, url, and repository only (no text fragments).

### Files

`get-file-content`, `create-or-update-file`, `delete-file`.

`create-or-update-file` requires `owner`, `repo`, `path`, `message`, and
`file: {file_id}`. Optional `sha` (required when replacing an existing blob)
and `branch`. Path must be relative (no leading `/`, no `..`). Publishes
`integrations.github.file-mutated.v1` without bytes or download URLs.
`delete-file` requires `path`, blob `sha`, and `message`; it does not move
bytes. `get-file-content` returns directory metadata or
`{path, sha, size, url, type, file_id, filename}` after Oryxa stores the
decoded blob. Do not expect or pass base64.

### Users, releases, collaborators, milestones, and stars

`get-authenticated-user`, `get-user`, `list-organization-members`,
`create-release`, `list-releases`, `get-release`, `get-latest-release`,
`list-collaborators`, `add-collaborator`, `remove-collaborator`,
`create-milestone`, `list-milestones`, `get-milestone`, `update-milestone`,
`star-repository`, `unstar-repository`, `list-starred-repositories`,
`check-repository-starred`.

Emails are never returned. Release create publishes
`integrations.github.release-created.v1`. Collaborator and star writes publish
`integrations.github.repository-mutated.v1`. Milestone writes publish
`integrations.github.milestone-mutated.v1`.

### Gists, GraphQL, workflows, and custom API

`create-gist`, `raw-graphql-query`, `dispatch-workflow`, `custom-api-call`.

`create-gist` requires `public`, `filename`, and text `content` (not a file
ref). Optional `description`. Needs `gist`. Publishes
`integrations.github.gist-created.v1` with gist id and URL only; file bytes
and raw URLs are stripped.

`raw-graphql-query` requires `query` (max 16 KiB). Optional `variables` object.
Returns HTTP `status` plus `body` only (no headers, no event).

`dispatch-workflow` requires `owner`, `repo`, `workflow_id` (numeric id or
workflow file name such as `ci.yml`), and `ref`. Optional `inputs` map.
`wait_for_completion` is rejected; the run is accepted and returns immediately.
Publishes `integrations.github.workflow-dispatched.v1` with run id and URL;
logs and job output are stripped.

`custom-api-call` requires `url` (`/path` or `https://api.github.com/...`) and
`method`. Optional `headers`, `query`, `body_type` (`none|json|form_data|raw`),
and `body`. Do not send `Authorization`; Oryxa injects the member token.
Returns status plus body only. No event.

## Approved triggers

Oryxa creates a member-owned GitHub webhook on an opaque per-connection route.
Inbound deliveries are HMAC-verified in Go. Issue, pull-request, review, and
commit bodies never publish. `new-issue` and `new-pull-request` stay
opened-only.

### `new-issue`

Input `props` requires `scope` (`repository|organization`) and `owner`.
`repository` also requires `repo`. Organization-wide hooks omit `repo` so one
trigger covers every repository in that org. Expect at-least-once
`integrations.github.issue-opened.v1` with `repository`, `issue_number`, and
`url` only.

### `new-pull-request`

Input `props` uses the same `scope`/`owner`/`repo` contract as `new-issue`.
Expect at-least-once `integrations.github.pull-request-opened.v1` with
`repository`, `pull_request_number`, and `url` only.

### `new-star`

Same `scope`/`owner`/`repo` contract. GitHub `watch`/`started` only. Expect
at-least-once `integrations.github.star-received.v1` with `repository` and
`url` only.

### `new-push`

Same `scope`/`owner`/`repo` contract. Expect at-least-once
`integrations.github.push-received.v1` with `repository`, `ref`, optional
`sha`, and compare `url`. Commit lists never publish.

### `new-release`

Same `scope`/`owner`/`repo` contract. `published` and `released` only. Expect
at-least-once `integrations.github.release-published.v1` with `repository`,
`release_id`, and `url` only.

### `new-review`

Same `scope`/`owner`/`repo` contract. Submitted pull-request reviews only.
Expect at-least-once `integrations.github.review-submitted.v1` with
`repository`, `pull_request_number`, optional `review_id`, and `url` only.

Do not emulate a trigger with polling, a broad API query, or a direct webhook.

## Failure handling

- `scope_required` or `reauthorization_required`: request reconnection with the
  required scope (`read:org` for org members, `gist` for create-gist,
  `admin:org_hook` for org-wide triggers).
- `operation_disabled`: stop; installed package code does not make an action
  available.
- `idempotency_conflict`: the key was used with different input; do not alter
  the call silently.
- `action_in_progress`: wait or report progress, without creating a new call.
- `action_failed`: a new key is an explicit retry and needs user intent.
- `files_unavailable`: `/files` is not configured; do not fall back to base64.
- Trigger disable is local-first; remote GitHub hook deletion may continue
  through a durable cleanup job with a bounded retry.
- Provider errors are opaque by design; do not disclose or reconstruct raw
  GitHub failure payloads.
