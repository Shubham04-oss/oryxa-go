---
name: oryxa-google-docs
description: Use only with Oryxa Google Docs tools for approved document create, read, mutate, PDF export, and host-restricted custom API calls.
---

# Oryxa Google Docs

This is a call-scoped skill. The Oryxa tool adapter or MCP host loads it only
for a bound Google Docs tool. The user's current request takes precedence over
this guidance.

## Tool-call contract

- Use the Oryxa integration tool, not Docs APIs or Activepieces directly.
- Google uses one provider-level member connection for Gmail, Calendar, Drive,
  Docs, and Sheets. Use the `connection_id` from a `connected` catalog record.
- Never request or pass OAuth tokens. Writes need `documents`; reads need
  `documents.readonly` (`documents` satisfies it). Reconnect existing grants
  before Docs tools.
- Image `uri` values must be public `https` URLs. Do not pass localhost, IP, or
  metadata hosts. `export-as-pdf` returns an Oryxa `{file_id}` through `/files`.
- Host-restricted `docs-custom-api-call` may use `/path` or
  `https://docs.googleapis.com/...` only. Do not send `Authorization`.
- Piece polling (`new-document`) and `google-docs-find-document` stay disabled.
- Use one stable 8-200 character idempotency key per logical request.

## Approved actions

Public names are kebab-case. Lookups do not publish domain events. Writes
publish identity-only events without document body, markdown, or image URIs.

### Create and copy

`create-document`, `create-text-document`, `create-document-from-markdown`,
`create-document-from-template`, `copy-document`.

Create requires `title` plus `body` or `markdown`. Template requires `template`,
`values`, and `images` (HTTPS URIs). Copy requires `fileId` and `title`.
Creates publish `integrations.google.docs-document-created.v1`; copy publishes
`integrations.google.docs-document-copied.v1`.

### Read

`search-documents`, `get-document`, `get-document-plaintext`, `read-document`,
`get-document-end-index`.

Get/read return `document_id` plus title/revision only — never Docs JSON body.
Plaintext is capped at 32KiB with `truncated`. Search returns at most 100
`{id, name}` records. No events.

### Mutate

`append-text`, `insert-text`, `replace-all-text`, `delete-content-range`,
`replace-body-with-markdown`, `replace-section-with-markdown`,
`insert-page-break`, `create-header`, `delete-header`, `create-footer`,
`delete-footer`, `create-footnote`, `create-named-range`, `delete-named-range`,
`create-paragraph-bullets`, `delete-paragraph-bullets`, `insert-table`,
`insert-table-row`, `insert-table-column`, `delete-table-row`,
`delete-table-column`, `insert-text-in-table-cell`,
`insert-image-in-table-cell`, `unmerge-table-cells`, `update-table-row-style`,
`create-and-populate-table`, `insert-inline-image`, `replace-image`,
`update-document-style`, `batch-update`.

All require `documentId`. Publish `integrations.google.docs-document-mutated.v1`.

### Export and custom API

`export-as-pdf` requires `documentId` and returns `{file_id, filename}`. No
event. `docs-custom-api-call` returns status plus body only. No event.

## Failure handling

- `scope_required` or `reauthorization_required`: reconnect the shared Google
  grant with `documents` or `documents.readonly`.
- `files_unavailable`: `export-as-pdf` needs `FILES_URL`.
- Provider errors are opaque.
