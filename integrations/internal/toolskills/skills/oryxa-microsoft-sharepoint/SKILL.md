---
name: oryxa-microsoft-sharepoint
description: Use only with Oryxa Microsoft SharePoint tools for approved list, file, site, and host-restricted custom API operations.
---

# Oryxa Microsoft SharePoint

This is a call-scoped skill. The Oryxa tool adapter or MCP host loads it only
for a bound Microsoft SharePoint tool. The user's current request takes
precedence over this guidance.

## Tool-call contract

- Use the Oryxa integration tool, not Graph APIs or Activepieces directly.
- Microsoft uses one provider-level member grant across Outlook, Calendar,
  OneDrive, Teams, and SharePoint. Use the `connection_id` from `connected`
  catalog state.
- Never request or pass tokens. Reconnect existing grants for `Sites.Read.All`,
  `Sites.ReadWrite.All`, `Sites.Manage.All`, and `Files.ReadWrite.All`.
  `Sites.Manage.All` satisfies read/write site scopes. `Files.ReadWrite.All`
  satisfies `Files.ReadWrite`.
- Pass site, drive, item, and list IDs as strings. `listColumns` is a JSON
  object (not a dynamic dropdown). `upload-sharepoint-file` uses
  `file: {file_id}`. `includeCustomFields` must stay false.
- Host-restricted `sharepoint-custom-api-call` may use `/path` or
  `https://graph.microsoft.com/...` only. Piece polling triggers stay
  disabled.
- Use one stable 8-200 character idempotency key per logical request.

## Approved actions

Lists return at most 100 metadata records plus `truncated`. File bytes, list
field values, and `@odata.nextLink` never appear in results or events.

### Lists and pages

`create-list`, `create-list-item`, `update-list-item`, `delete-list-item`,
`find-list-item`, `publish-page`.

Create/upload publish `integrations.microsoft.sharepoint-item-created.v1`.
Updates, deletes, and publish-page publish
`integrations.microsoft.sharepoint-item-mutated.v1`. Find is a lookup.

### Files and folders

`create-sharepoint-folder`, `upload-sharepoint-file`, `copy-sharepoint-item`,
`copy-item-within-site`, `move-sharepoint-file`, `find-sharepoint-file`,
`get-folder-contents`.

Copy `conflict_behavior` / `conflictBehavior` is `fail`, `replace`, or
`rename`. Find `findMethod` is `path` or `search`. Copies publish
`integrations.microsoft.sharepoint-item-copied.v1`.

### Sites and custom API

`get-site-information`, `find-site`, `sharepoint-custom-api-call`. Lookups and
custom API publish no event.

## Failure handling

- `scope_required` or `reauthorization_required`: reconnect with Sites or
  Files.ReadWrite.All as needed.
- `files_unavailable`: upload needs `FILES_URL`.
- Provider errors are opaque.
