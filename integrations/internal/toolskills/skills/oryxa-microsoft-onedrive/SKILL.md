---
name: oryxa-microsoft-onedrive
description: Use only with Oryxa Microsoft OneDrive tools for the approved metadata-only file listing action.
---

# Oryxa Microsoft OneDrive

This is a call-scoped skill. The Oryxa tool adapter or MCP host loads it only
for a bound Microsoft OneDrive tool. The user's current request takes
precedence over this guidance.

## Tool-call contract

- Use the Oryxa integration tool, not Graph APIs or Activepieces directly.
- Microsoft uses one provider-level member grant across Outlook, Calendar, and
  OneDrive. Use the `connection_id` from `connected` catalog state;
  `available` means authorization is still required.
- Never request or pass access tokens, refresh tokens, client secrets, or KMS
  values. The connection must grant `Files.Read`.
- The approved OneDrive surface is metadata-only. Do not claim to download,
  upload, move, share, edit, delete, or read file contents.
- Use one stable 8-200 character idempotency key per logical lookup and reuse
  it only with identical input.

## Approved action: `list-files`

Input `props` may contain `parentFolder`; omit it to use the piece's default
root behavior. When present it must be a bounded provider folder identifier.

Expect at most 100 safe records plus `truncated`. A record may contain ID,
name, size, MIME type, HTTPS web URL, creation time, and modification time.
File bytes, sharing permissions, preview bodies, and download credentials are
never returned.

## Triggers

No OneDrive trigger is approved. Do not emulate one with polling or a direct
Graph subscription.

## Failure handling

- `scope_required` or `reauthorization_required`: reconnect the shared
  Microsoft grant with `Files.Read`.
- `operation_disabled`: file content or mutation is outside the catalog; stop
  instead of switching to an unapproved provider path.
- `action_in_progress`: wait/report without issuing a second lookup.
- Provider errors are opaque and must not be expanded with raw Graph response
  data.
