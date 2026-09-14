---
name: oryxa-microsoft-onedrive
description: Use only with Oryxa Microsoft OneDrive tools for approved metadata listing, copy, upload, and download.
---

# Oryxa Microsoft OneDrive

This is a call-scoped skill. The Oryxa tool adapter or MCP host loads it only
for a bound Microsoft OneDrive tool. The user's current request takes
precedence over this guidance.

## Tool-call contract

- Use the Oryxa integration tool, not Graph APIs or Activepieces directly.
- Microsoft uses one provider-level member grant across Outlook, Calendar,
  OneDrive, Teams, and SharePoint. Use the `connection_id` from `connected` catalog state;
  `available` means authorization is still required.
- Never request or pass access tokens, refresh tokens, client secrets, or KMS
  values.
- The approved OneDrive surface is metadata listing, copy, upload, and
  download. Pass Oryxa `{file_id}` refs for bytes; never base64. File actions
  require `FILES_URL` or they fail with `files_unavailable`.
- Use one stable 8-200 character idempotency key per logical lookup or copy
  and reuse it only with identical input.

## Approved actions

### `list-files` / `list-folders`

Input `props` may contain `parentFolder`; omit it to use the piece's default
root behavior. When present it must be a bounded provider folder identifier.
The connection needs `Files.Read`, which is also satisfied by
`Files.ReadWrite`.

Expect at most 100 safe records in `files` plus `truncated`. A record may
contain ID, name, size, MIME type (files only), HTTPS web URL, creation time,
and modification time. File bytes, sharing permissions, preview bodies, and
download credentials are never returned. These lookups do not publish domain
events.

### `copy-file`

Input `props` requires `fileId`. Optional `destinationFolderId`, `newName`,
and `conflictBehavior` (`rename` default, `replace`, or `fail`). The
connection needs `Files.ReadWrite`. Success returns the copied item's safe
metadata and publishes `integrations.microsoft.onedrive-item-copied.v1`.

### `upload-onedrive-file`

Input `props` requires `fileName` and `file: {file_id}`. Optional `parentId`.
The connection needs `Files.ReadWrite`. Success returns the item's safe
metadata and publishes `integrations.microsoft.onedrive-item-created.v1`.

### `download-file`

Input `props` requires `lookupBy` (`id|name`) and `fileIdentifier`. The
connection needs `Files.Read`, which is also satisfied by `Files.ReadWrite`.
Success returns `{file_id, filename}` for the stored object. Web URLs and bytes
are not returned. This is a lookup: it does not publish a domain event.

## Triggers

No OneDrive trigger is approved. Do not emulate one with polling or a direct
Graph subscription.

## Failure handling

- `scope_required` or `reauthorization_required`: reconnect the shared
  Microsoft grant with `Files.Read` or `Files.ReadWrite` as appropriate.
  Existing connections created before file write scopes were requested must
  reconnect to copy.
- `operation_disabled`: move/share/delete remain outside the catalog;
  stop instead of switching to an unapproved provider path.
- `action_in_progress`: wait/report without issuing a second lookup or copy.
- Provider errors are opaque and must not be expanded with raw Graph response
  data.
