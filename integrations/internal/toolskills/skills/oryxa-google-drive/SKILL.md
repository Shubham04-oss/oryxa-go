---
name: oryxa-google-drive
description: Use only with Oryxa Google Drive tools for approved metadata, share, comment, upload, download, leftover unique Drive writes, and inbound drive-file-changed.
---

# Oryxa Google Drive

This is a call-scoped skill. The Oryxa tool adapter or MCP host loads it only
for a bound Google Drive tool. The user's current request takes precedence over
this guidance.

## Tool-call contract

- Use the Oryxa integration tool, not Drive APIs or Activepieces directly.
- Google uses one provider-level member connection for Gmail, Calendar, Drive,
  Docs, and Sheets. Use the `connection_id` from a `connected` catalog record;
  `available` means the member must authorize first.
- Never request or pass OAuth tokens or client secrets. List/get/search require
  Drive metadata read access. Copies, moves, trash, share, comments, and shared
  drives require `drive`. Download and export require `drive.readonly` (implied
  by `drive`). Reconnect existing grants to pick up `drive` before
  those writes. A `drive` grant satisfies `drive.metadata.readonly` and
  `drive.readonly`.
- File bytes enter and leave through Oryxa `{file_id}` refs. Do not pass
  base64. `upload-from-url` fetches a public `https` URL (no IP, localhost, or
  metadata hosts) and does not use Oryxa files. Search may use `fullText`. Copy
  `convert_to` is Docs or Sheets MIME only. Public-access results never include
  download URLs. Host-restricted `drive-custom-api-call` may use `/path` or
  `https://www.googleapis.com/...` only.
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
`delete-drive-file`, `upload-drive-file`, `replace-drive-file`,
`download-drive-file`, `export-drive-file`, `create-file-from-text`,
`save-file-as-pdf`, `upload-from-url`.

`upload-drive-file` requires `file_name` and `file: {file_id}`. Optional
`parent_folder_id`. Publishes `integrations.google.drive-item-created.v1`.
`replace-drive-file` requires Drive `file_id` plus Oryxa `file: {file_id}` and
publishes `integrations.google.drive-item-mutated.v1`. Download and export
return `{file_id, filename}` for the stored object and publish no event.
`export-drive-file` `mime_type` must be one of the piece's listed export
formats. List requires `folder_id`; `download_files` must remain false. Search
requires `query_term` of `name`, `mimeType`, or `fullText`, `operator`
(`contains|=`), and `value`. Copy requires `file_id`, `name`, and `folder_id`.
Optional `convert_to` is Docs or Sheets MIME only. `create-file-from-text`
writes text into Drive (`content_type` must be `plain/text`, `text/csv`, or
`text/xml` — not `text/plain`) and does not store an Oryxa file. `save-file-as-pdf`
requires `document_id`, `folder_id`, and `name`. `upload-from-url` requires
public `https` `source_url` and `file_name`; optional `mime_type` and
`parent_folder_id`. Permanent delete is
irreversible; prefer trash. Create publishes
`integrations.google.drive-item-created.v1`; copy publishes
`integrations.google.drive-item-copied.v1`; other writes publish
`integrations.google.drive-item-mutated.v1`.

`drive-custom-api-call` requires `url` (`/files/...` or
`https://www.googleapis.com/...`) and `method`. Optional `headers`, `query`,
`body_type`, `body`. Do not send `Authorization`. Returns status plus body
only. No event. Needs `drive.readonly` (`drive` satisfies it).

### Sharing and shared drives

`share-drive-file`, `list-drive-permissions`, `update-drive-permission`,
`remove-drive-permission`, `set-drive-public-access`, `list-shared-drives`,
`get-shared-drive`, `create-shared-drive`, `update-shared-drive`,
`empty-trash`, `delete-shared-drive`.

Share/update/remove take a user email and role
(`organizer|fileOrganizer|writer|commenter|reader`). `set-drive-public-access`
takes Drive `file_id` and role `reader|commenter|writer` and returns permission
identity plus `webViewLink` only — never a download URL. `empty-trash` is
optional `drive_id` (defaults to the user corpus). `delete-shared-drive`
requires `drive_id` and is irreversible. Lookups may include emails; domain
events do not. Public access, empty-trash, and shared-drive delete publish
`integrations.google.drive-item-mutated.v1`.

### Comments and quota

`create-drive-comment`, `list-drive-comments`, `create-drive-reply`,
`get-drive-reply`, `list-drive-replies`, `update-drive-reply`,
`delete-drive-reply`, `get-drive-about`.

Comment/reply writes publish `integrations.google.drive-item-mutated.v1`.
Lookups may include comment text. About returns quota numbers and the account
email only.

## Triggers

### `drive-file-changed`

Oryxa creates a member-owned Drive `changes.watch` channel. Inbox sync
notifications are acknowledged and not enqueued. Expect at-least-once
`integrations.google.drive-file-changed.v1` with trigger, connection, and
delivery identity only. File names and bytes never publish. `drive.readonly`
is required (`drive` implies it). Channel TTL is one day.

Do not emulate a trigger with polling or a direct webhook.

## Failure handling

- `scope_required` or `reauthorization_required`: reconnect the shared Google
  grant with Drive metadata and, for writes, `drive`.
- `operation_disabled`: waitpoints, piece polling, and unlisted Drive packages
  remain outside the catalog; do not route around the restriction.
- `files_unavailable`: `/files` is not configured; do not fall back to base64.
- `action_in_progress`: wait/report without issuing a second lookup.
- Provider failures are intentionally opaque and never justify requesting raw
  credentials or response bodies.
