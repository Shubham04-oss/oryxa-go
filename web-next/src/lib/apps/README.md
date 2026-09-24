# `lib/apps` — connection and trigger semantics

Read against `apps/internal/domain/domain.go`, `apps/internal/application/connections.go`,
`lifecycle.go`, `triggers.go` and `webhooks.go` (not from memory). This is the
detail a page needs that isn't obvious from the wire shapes alone.

## Connection status lifecycle

`pending → active | failed`, and separately `active → expired`:

- **`pending`** — set the moment `connect()` succeeds. Means: the member has
  not finished at Composio's hosted page yet, or the callback couldn't
  confirm it finished within the few seconds it waits (see below).
- **`active`** — Composio confirmed the account; the connection is usable.
- **`failed`** — never completed. Reached from `pending` only, by one of:
  the member cancelling/denying at Composio, Composio reporting the account
  failed/expired/revoked/inactive during the callback poll, the callback's
  `state` not matching what apps recorded (a replayed or forged callback), or
  the 15-minute maintenance sweep abandoning a `pending` connection once it
  is over 24 hours old.
- **`expired`** — reached from `active` only, and **only** by an asynchronous
  Composio webhook (`connection.expired`) telling apps the credential can no
  longer be renewed (e.g. an OAuth refresh token was revoked at the
  provider). Nothing the frontend does can cause or detect this early; it is
  purely reactive. A `GET` on the connection is the only way to notice it.
- **`removed`** — set by `disconnectConnection` (`DELETE`). Terminal; a
  `DELETE` on an already-`removed` connection is a no-op, not an error.

## How long a pending connection lasts

Two different timers apply:

1. **The callback itself** polls Composio's connected-account status up to 5
   times, one second apart (≈5 seconds), while the member's browser is on
   its way back from Composio. If the account is active or has clearly
   failed by then, the connection resolves immediately. If not — a slow
   OAuth provider, a browser that hasn't returned yet — the callback leaves
   it `pending` and redirects back with `status=pending`; nothing is broken,
   the member can just come back to it.
2. **The maintenance sweep** runs every 15 minutes and fails any connection
   still `pending` more than 24 hours after it was created
   (`pendingLinkLifetime`). Until then, a `GET` on a stuck `pending`
   connection keeps reporting `pending` — there is no server-side "check
   again now" a client can trigger.

So: a `pending` connection can realistically sit for up to ~24 hours before
the server gives up on it. A page should not assume a few seconds of polling
will resolve it and should let the member leave and come back.

## Cancelling or retrying a pending connection

There is **no dedicated cancel or retry endpoint**. In practice:

- **Cancel** a pending connection the same way you disconnect any other:
  `DELETE .../apps/connections/{id}` (`disconnectConnection`). This revokes
  whatever Composio account was created and marks the row `removed`.
- **Retry**: calling `connect()` again for the same toolkit while a
  `pending` (or `active`) connection still exists for it fails with 409
  `conflict` (see below) — you must cancel first, then connect again.

## What `connect()`'s 409 means, precisely

`Service.Connect` checks the caller's existing connections to the toolkit
filtered to `pending` and `active` only. If either exists, it refuses with
**`409 conflict`** (not `choose_connection` — that code is reserved for a
different, legacy multi-connection situation the current one-connection
model no longer produces). The message text differs by which case it is
("... is already connected for this member" vs "... is already being
connected for this member"), but **`ApiError.code` is `"conflict"` in both
cases** — a client cannot distinguish "already active" from "already
pending" from the code alone, only by parsing the message (not
recommended). The practical response is the same either way: stop, refetch
the connections list, and show the member the connection that already
exists instead of blindly retrying.

Important corollary: **`failed` and `expired` connections do not block a new
`connect()` call.** Only `pending`/`active` do. So "reconnect" on an expired
or failed connection is just calling `connect()` again — no need to
disconnect first. `canReconnect()` in `client.ts` reflects this (it returns
true for both `expired` and `failed`, not only `expired` as `web/`'s
frontend did).

**Contract surprise:** because `connect()` always creates a brand-new
connection row (new id, new Composio account) rather than reusing the old
one, reconnecting an expired/failed connection leaves the old row in place
— now stale, but not `removed`, so it still shows up in `listConnections`.
A page that offers "Reconnect" has to decide what becomes of the old row;
how depends on its status, because disconnecting deletes data.

`replacedConnections()` finds those rows: a `failed` or `expired` connection
whose toolkit has an `active` connection created after it (compared with
`Date.parse`, since Go writes microseconds and sometimes a local offset).

- A **failed** one is safe to disconnect without asking: it never connected,
  so nothing hangs off it. Tools removes it once the new connection is
  active, and shows it again only if that fails.
- An **expired** one is never disconnected for the member. Its sources were
  paused (`needs_reconnect`) with their documents kept and searchable, and
  knowledge never re-links a paused source to a new connection, so
  disconnecting the old row retires them and retracts what they indexed. The
  new connection may also be a different account for the same app (an old
  work mailbox, a new personal one), which would make an automatic removal
  wipe data the member still wants. Tools shows it as "Replaced by your new
  connection" and offers only Remove, behind the same confirm step as
  Disconnect; it offers no Reconnect, which apps would refuse with 409 while
  the new one is active.

With a standing agent grant, knowledge registers up to ten sources for a
newly connected app by itself (`ConnectionConnected`); without one, the
member registers them again.

## What a page should offer, by status

| Status                                    | Disconnect                                             | Reconnect                                 | Cancel                    |
| ----------------------------------------- | ------------------------------------------------------ | ----------------------------------------- | ------------------------- |
| `pending`                                 | Yes — this **is** cancel                               | No (would 409)                            | Same action as Disconnect |
| `active`                                  | Yes                                                    | No (already connected)                    | —                         |
| `failed`                                  | Yes (clears the stale row)                             | Yes (`connect()` again)                   | —                         |
| `expired`                                 | Yes, after a confirm step                              | Yes (`connect()` again)                   | —                         |
| `expired`, replaced by a newer active one | Remove only, after a confirm step; never automatically | No (would 409)                            | —                         |
| `removed`                                 | No-op if attempted                                     | Yes (`connect()` again; nothing to clear) | —                         |

## Composio's return trip

`GET /v1/apps/connect/callback` is Composio calling the browser back, not
something this client ever calls directly. `completeConnection` in
`apps/internal/httpserver/server.go` redirects to the connection's
`return_to` (the value `connect()` was given) with **exactly two** query
parameters appended:

- `app_connection` — the connection id
- `status` — the connection's status at that moment (`active`, `pending` or
  `failed` in practice)

**No `reason` or `status_reason` parameter is ever added to the redirect**,
despite that being a reasonable guess. A failure's detail lives only on the
connection itself (`Connection.status_reason`), fetched separately with
`getConnection`. `parseConnectReturn()` reads exactly these two parameters
and nothing else.

`web-next` has no `/app/connections`-equivalent route (Tools lives inside
the Home workspace shell), so this package does not hardcode a `return_to`
path the way `web/`'s `connectReturnTo()` did. Tools builds it with
`connectReturnTo()` in `components/workspace-tool-helpers.ts`: the current
page with `?org=<the organization it connected in>&view=tools`. On the way
back it reads the two parameters once with `parseConnectReturn()`, takes
them off the address with `history.replaceState` (TanStack Router's history
wraps it, so the router follows), opens that connection, and shows
`connectReturnNotice(value, { name, reason })`, with the reason read from
`getConnection` and put in a member's words by `connectionReasonText()`.

## Error codes (`ApiError.code`)

Every code `ErrorCode` in `apps/internal/httpserver/server.go` can produce,
and how `appsErrorMessage` treats it:

| Code                | HTTP | Overridden with friendlier copy?                                                                |
| ------------------- | ---- | ----------------------------------------------------------------------------------------------- |
| `invalid_request`   | 400  | No — Go's own message is already specific (e.g. "a toolkit is a lowercase slug such as github") |
| `unauthorized`      | 401  | No — "authentication failed" is fine as-is                                                      |
| `not_read_only`     | 403  | Yes                                                                                             |
| `forbidden`         | 403  | No — "operation is not allowed" / a specific reason is fine as-is                               |
| `not_found`         | 404  | No — "resource not found" is fine as-is                                                         |
| `not_connected`     | 409  | Yes                                                                                             |
| `choose_connection` | 409  | Yes (kept for legacy data; the current model shouldn't produce it)                              |
| `needs_reconnect`   | 409  | Yes                                                                                             |
| `conflict`          | 409  | Yes                                                                                             |
| `not_connectable`   | 422  | Yes                                                                                             |
| `rate_limited`      | 429  | Yes                                                                                             |
| `unavailable`       | 503  | Yes                                                                                             |
| `internal`          | 500  | N/A — any 5xx falls back to the caller's own `fallback` text, code or not                       |

## Connections list scope

`GET .../apps/connections` defaults to the caller's own connections. With
`?scope=organization` (only meaningful exact value; anything else is
ignored) an org admin sees every member's. **The server excludes `removed`
connections from this list by default** — `ConnectionQuery.Statuses` empty
means "every status but removed" — so `groupConnectionsByToolkit`'s own
`removed` filter in `client.ts` is mostly defensive, not load-bearing.

**Contract surprise:** `web-next/src/lib/settings/client.ts` already calls
this exact endpoint (`listOrganizationConnections`) with its own smaller
`Connection` type (`id`, `toolkit`, `member_id`, `status` only — missing
`auto_subscribe`/timestamps/etc.) for the read-only tool-access settings
page. That file is outside this task's owned paths, so it hasn't been
touched, but the duplication is worth resolving when Tools/Apps migrates
onto this package: either have that settings page switch to
`listConnections(id, { scope: "organization" })` from here, or accept the
two independent, overlapping clients.
