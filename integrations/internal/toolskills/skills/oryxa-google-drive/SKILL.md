---
name: oryxa-google-drive
description: Use only with Oryxa Google Drive tools for approved metadata, share, and comment operations.
---

# Oryxa Google Drive

This is a call-scoped skill. The Oryxa tool adapter or MCP host loads it only
for a bound Google Drive tool. The user's current request takes precedence over
this guidance.

## Tool-call contract

- Use the Oryxa integration tool, not Drive APIs or Activepieces directly.
- Google uses one provider-level member connection for Gmail, Calendar, and
  Drive. Use the `connection_id` from a `connected` catalog record;
  `available` means the member must authorize first.
- Never request or pass OAuth tokens or client secrets. List/get/search require
  Drive metadata read access. Copies, moves, trash, share, comments, and shared
  drives require `drive`. Reconnect existing grants to pick up `drive` before
  those writes. A `drive` grant satisfies `drive.metadata.readonly`.
- Do not claim to download, upload, export, or replace file bytes. `fullText`
  search, Docs/Sheets `convert_to`, public-access download URLs, empty-trash,
  and deleting a shared drive are not approved.
- Use one stable 8-200 character idempotency key per logical request and reuse
  it only with identical input.

## Approved actions

Public names are kebab-case. Lists return at most 100 metadata records plus
`truncated`. Lookups do not publish domain events. Writes publish identity-only
events without file bytes, download URLs, or emails.

### Files and folders

`list-drive-files`, `get-drive-file-metadata`, `create-drive-folder`,
`search-drive-files`, `copy-drive-file`, `move-drive-file`,
`update-drive-file-metadata`, `trash-drive-file`, `untrash-drive-file`,
`delete-drive-file`.

List requires `folder_id`; `download_files` must remain false. Search requires
`query_term` of `name` or `mimeType` (not `fullText`), `operator`
(`contains|=`), and `value`. Copy requires `file_id`, `name`, and `folder_id`
and must omit `convert_to`. Permanent delete is irreversible; prefer trash.
Create publishes `integrations.google.drive-item-created.v1`; copy publishes
`integrations.google.drive-item-copied.v1`; other writes publish
`integrations.google.drive-item-mutated.v1`.

### Sharing and shared drives

`share-drive-file`, `list-drive-permissions`, `update-drive-permission`,
`remove-drive-permission`, `list-shared-drives`, `get-shared-drive`,
`create-shared-drive`, `update-shared-drive`.

Share/update/remove take a user email and role
(`organizer|fileOrganizer|writer|commenter|reader`). Lookups may include
emails; domain events do not.

### Comments and quota

`create-drive-comment`, `list-drive-comments`, `create-drive-reply`,
`get-drive-reply`, `list-drive-replies`, `update-drive-reply`,
`delete-drive-reply`, `get-drive-about`.

Comment/reply writes publish `integrations.google.drive-item-mutated.v1`.
Lookups may include comment text. About returns quota numbers and the account
email only.

## Triggers

No Google Drive trigger is approved. Do not emulate one with polling.

## Failure handling

- `scope_required` or `reauthorization_required`: reconnect the shared Google
  grant with Drive metadata and, for writes, `drive`.
- `operation_disabled`: the requested file mutation or download is outside the
  catalog; do not route around the restriction.
- `action_in_progress`: wait/report without issuing a second lookup.
- Provider failures are intentionally opaque and never justify requesting raw
  credentials or response bodies.
