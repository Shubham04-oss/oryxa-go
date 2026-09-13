---
name: oryxa-google-drive
description: Use only with Oryxa Google Drive tools for approved metadata-only file listing and lookup.
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
- Never request or pass OAuth tokens or client secrets. The connection must
  grant Drive metadata read access.
- This tool family is metadata-only. Do not claim to download, upload, copy,
  edit, share, delete, or read file contents.
- Use one stable 8-200 character idempotency key per logical lookup and reuse
  it only with identical input.

## Approved actions

### `list-drive-files`

Input `props` requires `folder_id`. Optional `include_trashed`, `depth_level`
(0-3), and `include_team_drives` narrow or expand metadata discovery.
`download_files` must remain false. Expect at most 100 metadata records plus a
`truncated` flag. Returned fields may include ID, name, MIME type, trash state,
parents, web-view link, and modified time; there is no file content.

### `get-drive-file-metadata`

Input `props` requires `file_id`. Expect one metadata record with no content or
download URL guarantee.

## Triggers

No Google Drive trigger is approved. Do not emulate one with polling.

## Failure handling

- `scope_required` or `reauthorization_required`: reconnect the shared Google
  grant with Drive metadata access.
- `operation_disabled`: the requested file mutation or download is outside the
  catalog; do not route around the restriction.
- `action_in_progress`: wait/report without issuing a second lookup.
- Provider failures are intentionally opaque and never justify requesting raw
  credentials or response bodies.
