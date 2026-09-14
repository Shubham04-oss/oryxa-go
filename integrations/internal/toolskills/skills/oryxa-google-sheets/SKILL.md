---
name: oryxa-google-sheets
description: Use only with Oryxa Google Sheets AI tools for approved spreadsheet, worksheet, row, and host-restricted custom API operations.
---

# Oryxa Google Sheets

This is a call-scoped skill. The Oryxa tool adapter or MCP host loads it only
for a bound Google Sheets tool. The user's current request takes precedence
over this guidance.

## Tool-call contract

- Use the Oryxa integration tool, not Sheets APIs or Activepieces directly.
- Google uses one provider-level member connection for Gmail, Calendar, Drive,
  Docs, and Sheets. Use the `connection_id` from a `connected` catalog record.
- Never request or pass OAuth tokens. Writes need `spreadsheets`; reads need
  `spreadsheets.readonly` (`spreadsheets` satisfies it). Reconnect existing
  grants before Sheets tools.
- Pass spreadsheet and sheet identifiers as strings/numbers. Do not use
  dropdown-only human duplicates (`insert_row`, `create-spreadsheet`, and the
  rest). `sheets_get_next_rows` stays disabled because it uses piece store
  state.
- `sheets-export-worksheet` returns an Oryxa `{file_id}` (`return_as_text` must
  stay false). Host-restricted `sheets-custom-api-call` may use `/path` or
  `https://sheets.googleapis.com/...` only.
- Piece polling triggers stay disabled.
- Use one stable 8-200 character idempotency key per logical request.

## Approved actions

Public names are kebab-case with a `sheets-` prefix. Row reads return at most
100 rows plus `truncated`. Lookups do not publish domain events. Writes publish
identity-only events without cell values.

### Spreadsheet and worksheets

`sheets-create-spreadsheet`, `sheets-get-spreadsheet`,
`sheets-search-spreadsheets`, `sheets-add-worksheet`, `sheets-find-worksheet`,
`sheets-find-or-create-worksheet`, `sheets-copy-worksheet`,
`sheets-rename-worksheet`, `sheets-delete-worksheet`.

Create requires `spreadsheet_title`. Get rejects `include_grid_data`. Create
publishes `integrations.google.sheets-spreadsheet-created.v1`; other writes
publish `integrations.google.sheets-spreadsheet-mutated.v1`.

### Rows and values

`sheets-add-row`, `sheets-add-row-at-top`, `sheets-add-multiple-rows`,
`sheets-update-row`, `sheets-update-multiple-rows`, `sheets-delete-row`,
`sheets-delete-multiple-rows`, `sheets-find-rows`, `sheets-find-or-create-row`,
`sheets-get-row`, `sheets-get-all-rows`, `sheets-get-values`,
`sheets-append-values`, `sheets-update-values`, `sheets-clear-values`.

Values JSON is capped at 64KiB. Find rows `number_of_rows` is at most 100.

### Structure, format, export

`sheets-add-column`, `sheets-insert-dimension`, `sheets-append-dimension`,
`sheets-delete-dimension`, `sheets-auto-resize-dimensions`,
`sheets-update-dimension-properties`, `sheets-update-sheet-properties`,
`sheets-format-cells`, `sheets-export-worksheet`, `sheets-custom-api-call`.

Export `format` is `csv` or `tsv`. Custom API returns status plus body only.

## Failure handling

- `scope_required` or `reauthorization_required`: reconnect with
  `spreadsheets` or `spreadsheets.readonly`.
- `files_unavailable`: export needs `FILES_URL`.
- Provider errors are opaque.
