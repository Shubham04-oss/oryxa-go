import { infiniteQueryOptions, queryOptions } from "@tanstack/react-query"
import { ApiError, apiRequest } from "@/lib/api/client"

// ---------------------------------------------------------------------------
// Catalog
// ---------------------------------------------------------------------------

export type CatalogCategory = {
  id: string
  name: string
}

/** An app in Composio's catalog, as a member browses it. */
export type Toolkit = {
  toolkit: string
  name: string
  description: string
  logo?: string
  categories: CatalogCategory[]
  auth_schemes: string[]
  /** A member can connect it with what this project holds. */
  connectable: boolean
  no_auth?: boolean
  tools_count: number
  triggers_count: number
}

/** The event a subscription watches for, as its own kind of catalog entry. */
export type TriggerType = {
  trigger: string
  name: string
  description: string
  instructions?: string
  toolkit: string
  /** "webhook" when the app pushes the event, "poll" when Composio polls. */
  kind: string
  /** A JSON Schema of what a subscription takes, when it takes anything. */
  config_schema?: unknown
  /** A webhook URL has to be registered with the app by hand first. */
  manual_setup?: boolean
}

export type CatalogQuery = {
  search?: string
  category?: string
  cursor?: string
  limit?: number
}

export type CatalogPage = {
  toolkits: Toolkit[]
  nextCursor?: string
}

export type TriggerTypePage = {
  triggers: TriggerType[]
  nextCursor?: string
}

function catalogPath(): string {
  return "/v1/apps/catalog"
}

function withQuery(
  path: string,
  params: Record<string, boolean | number | string | undefined>
): string {
  const search = new URLSearchParams()
  for (const [key, value] of Object.entries(params)) {
    if (value !== undefined && value !== "") {
      search.set(key, String(value))
    }
  }
  const query = search.toString()
  return query ? `${path}?${query}` : path
}

/**
 * Lists Composio's catalog. Unauthenticated: the catalog holds nothing of any
 * organization. `apps/internal/application/catalog.go`'s `Catalog` clamps
 * `limit` to 100 server-side (0 or over becomes 100) rather than erroring.
 */
export async function listCatalog(
  query: CatalogQuery = {}
): Promise<CatalogPage> {
  const { next_cursor, toolkits } = await apiRequest<{
    next_cursor?: string
    toolkits: Toolkit[]
  }>(
    withQuery(catalogPath(), {
      category: query.category,
      cursor: query.cursor,
      limit: query.limit,
      search: query.search,
    })
  )
  return { nextCursor: next_cursor, toolkits }
}

export function getCatalogToolkit(toolkit: string): Promise<Toolkit> {
  return apiRequest<Toolkit>(`${catalogPath()}/${encodeURIComponent(toolkit)}`)
}

/**
 * Lists one app's subscribable events. `CatalogTriggers` clamps `limit` to 50
 * server-side (0 or over 50 becomes 50) — unlike the main catalog's cap of
 * 100, so a caller wanting every trigger type must actually page with
 * `nextCursor` rather than asking for a large `limit` once.
 */
export async function listCatalogTriggerTypes(
  toolkit: string,
  query: { cursor?: string; limit?: number } = {}
): Promise<TriggerTypePage> {
  const { next_cursor, triggers } = await apiRequest<{
    next_cursor?: string
    triggers: TriggerType[]
  }>(
    withQuery(`${catalogPath()}/${encodeURIComponent(toolkit)}/triggers`, {
      cursor: query.cursor,
      limit: query.limit,
    })
  )
  return { nextCursor: next_cursor, triggers }
}

/** How many pages of trigger types one read follows before settling for what it has. */
export const triggerTypePagesLimit = 10

/**
 * Every subscribable event an app has, following `nextCursor` (at most
 * `triggerTypePagesLimit` pages of 50), since the catalog pages them.
 */
export async function listAllCatalogTriggerTypes(
  toolkit: string
): Promise<TriggerType[]> {
  const types: TriggerType[] = []
  let cursor: string | undefined
  for (let page = 0; page < triggerTypePagesLimit; page += 1) {
    const result = await listCatalogTriggerTypes(toolkit, { cursor, limit: 50 })
    types.push(...result.triggers)
    if (!result.nextCursor) break
    cursor = result.nextCursor
  }
  return types
}

// ---------------------------------------------------------------------------
// Connections
// ---------------------------------------------------------------------------

export type ConnectionStatus =
  "active" | "expired" | "failed" | "pending" | "removed"

/**
 * One member's connection to one app. See `README.md` alongside this file for
 * what each status means, how long a pending connection lasts, and what a
 * page should offer for disconnect, reconnect and cancel-pending.
 */
export type Connection = {
  id: string
  toolkit: string
  status: ConnectionStatus
  status_reason?: string
  label?: string
  member_id: string
  composio_account_id?: string
  /**
   * Whether this connection is meant to subscribe to everything its app can
   * deliver once active. `auto_subscribed_at` is unset both before that has
   * happened and after auto-subscribe has given up (see
   * `auto_subscribe_attempts`); neither case is an error a member need act
   * on.
   */
  auto_subscribe: boolean
  auto_subscribed_at?: string
  auto_subscribe_attempts?: number
  created_at: string
  updated_at: string
  connected_at?: string
  removed_at?: string
}

export type ConnectInput = {
  toolkit: string
  /** Where the member's browser goes once they finish connecting. */
  returnTo: string
  /** An optional provider-facing label for the account. */
  alias?: string
  /** Left out asks for the deployment's own default (subscribe to everything). */
  autoSubscribe?: boolean
}

export type ConnectResult = {
  connection: Connection
  redirect_url: string
}

function connectionsPath(organizationId: string): string {
  return `/v1/organizations/${encodeURIComponent(organizationId)}/apps/connections`
}

function connectionPath(organizationId: string, connectionId: string): string {
  return `${connectionsPath(organizationId)}/${encodeURIComponent(connectionId)}`
}

/**
 * Starts connecting `input.toolkit`; the member finishes at the returned
 * `redirect_url`. 409s (`conflict`) when the member already has a pending or
 * active connection to this toolkit — see `README.md` for why that is the
 * one code both cases share.
 */
export function connect(
  organizationId: string,
  input: ConnectInput
): Promise<ConnectResult> {
  return apiRequest<ConnectResult>(connectionsPath(organizationId), {
    body: {
      alias: input.alias,
      auto_subscribe: input.autoSubscribe,
      return_to: input.returnTo,
      toolkit: input.toolkit,
    },
  })
}

export type ConnectionFilter = {
  /** "organization" reads every member's connections; an admin-only rollup. */
  scope?: "organization"
  toolkit?: string
}

/**
 * Lists the caller's own connections, or with `scope: "organization"` (org
 * admins only), everyone's. The server never includes a `removed`
 * connection in this list — see `README.md`.
 */
export async function listConnections(
  organizationId: string,
  filter: ConnectionFilter = {}
): Promise<Connection[]> {
  const { connections } = await apiRequest<{ connections: Connection[] }>(
    withQuery(connectionsPath(organizationId), {
      scope: filter.scope,
      toolkit: filter.toolkit,
    })
  )
  return connections
}

/** Reads one connection by id, whatever its status (including `removed`). */
export function getConnection(
  organizationId: string,
  connectionId: string
): Promise<Connection> {
  return apiRequest<Connection>(connectionPath(organizationId, connectionId))
}

/**
 * Disconnects `connectionId`: Composio revokes the credential and its
 * triggers are removed. This is also how a page should offer "cancel" on a
 * pending connection — there is no separate cancel endpoint, and disconnect
 * is idempotent on an already-removed connection.
 */
export async function disconnectConnection(
  organizationId: string,
  connectionId: string
): Promise<void> {
  await apiRequest(connectionPath(organizationId, connectionId), {
    method: "DELETE",
  })
}

// ---------------------------------------------------------------------------
// Triggers
// ---------------------------------------------------------------------------

export type TriggerStatus = "active" | "disabled" | "removed"

export type Trigger = {
  id: string
  connection_id: string
  toolkit: string
  trigger: string
  config: Record<string, unknown>
  status: TriggerStatus
  status_reason?: string
  member_id: string
  /** "webhook" when the app pushes the event, "poll" when Composio polls. */
  kind?: string
  /** The app's own webhook still needs registering by hand before this delivers. */
  manual_setup?: boolean
  /** What, if anything, is left to do or expect — advice, not a refusal. */
  setup?: string
  delivery_count: number
  last_delivery_at?: string
  created_at: string
  updated_at: string
}

function triggersPath(organizationId: string): string {
  return `/v1/organizations/${encodeURIComponent(organizationId)}/apps/triggers`
}

/** Lists the caller's live triggers, optionally narrowed to one connection. */
export async function listTriggers(
  organizationId: string,
  connectionId?: string
): Promise<Trigger[]> {
  const { triggers } = await apiRequest<{ triggers: Trigger[] }>(
    withQuery(triggersPath(organizationId), { connection_id: connectionId })
  )
  return triggers
}

export type CreateTriggerInput = {
  connectionId: string
  trigger: string
  config?: Record<string, unknown>
}

/**
 * Subscribes one of the caller's own active connections to an event.
 * Requires the connection to be `active` (409 `not_connected` otherwise, or
 * 409 `needs_reconnect` when it is specifically `expired`).
 */
export function createTrigger(
  organizationId: string,
  input: CreateTriggerInput
): Promise<Trigger> {
  return apiRequest<Trigger>(triggersPath(organizationId), {
    body: {
      config: input.config ?? {},
      connection_id: input.connectionId,
      trigger: input.trigger,
    },
  })
}

export async function removeTrigger(
  organizationId: string,
  triggerId: string
): Promise<void> {
  await apiRequest(
    `${triggersPath(organizationId)}/${encodeURIComponent(triggerId)}`,
    {
      method: "DELETE",
    }
  )
}

// ---------------------------------------------------------------------------
// Connection targets (what a connection can index)
// ---------------------------------------------------------------------------

/** One thing inside a connection that can be indexed: a repository, a channel, a database. */
export type ConnectionTarget = {
  /** "repository", "channel" or "database" — what the app calls it. */
  kind: string
  id: string
  /** What a member reads: "#engineering" rather than C0123456789. */
  name: string
  description?: string
  /**
   * The payload `knowledge`'s `sources.register` takes as `target`. Go types
   * this as opaque JSON (`json.RawMessage`); every provider emits a flat
   * string map today, so it is typed that way here, but treat it as
   * pass-through rather than a value to build by hand.
   */
  target: Record<string, string>
}

/**
 * What a member could index through their own connection, read live from the
 * app itself. Requires the connection to be `active` (same 409s as
 * `createTrigger`). An app with nothing indexable, or one `targetsOf` in
 * `apps/internal/application/targets.go` has no provider for, answers an
 * empty `[]` — a fact, not a failure, and every branch there builds the
 * slice with `make`/a literal rather than leaving it nil, so this is never a
 * JSON `null` to guard against.
 */
export async function listConnectionTargets(
  organizationId: string,
  connectionId: string
): Promise<ConnectionTarget[]> {
  const { targets } = await apiRequest<{ targets: ConnectionTarget[] }>(
    `${connectionPath(organizationId, connectionId)}/targets`
  )
  return targets
}

// ---------------------------------------------------------------------------
// Query keys and TanStack query options
// ---------------------------------------------------------------------------

/** The prefix of everything cached from apps about one organization. */
export function appsQueryKey(organizationId: string) {
  return ["apps", organizationId] as const
}

export function catalogQueryOptions(query: CatalogQuery = {}) {
  return queryOptions({
    queryKey: [
      "apps",
      "catalog",
      query.search ?? "",
      query.category ?? "",
      query.cursor ?? "",
      query.limit ?? 0,
    ] as const,
    queryFn: () => listCatalog(query),
  })
}

/** A single app's catalog entry, cached: the catalog changes rarely. */
export function catalogToolkitQueryOptions(toolkit: string) {
  return queryOptions({
    queryKey: ["apps", "catalog", "toolkit", toolkit] as const,
    queryFn: () => getCatalogToolkit(toolkit),
    staleTime: 5 * 60_000,
  })
}

/** Every event an app can deliver, cached: the catalog changes rarely. */
export function catalogTriggerTypesQueryOptions(toolkit: string) {
  return queryOptions({
    queryKey: ["apps", "catalog", "triggers", toolkit] as const,
    queryFn: () => listAllCatalogTriggerTypes(toolkit),
    staleTime: 5 * 60_000,
  })
}

/**
 * A catalog search, a page at a time: each page's `nextCursor` is the next
 * page's cursor, until there is none.
 */
export function catalogSearchQueryOptions(query: {
  search: string
  category?: string
  limit?: number
}) {
  return infiniteQueryOptions({
    queryKey: [
      "apps",
      "catalog",
      "search",
      query.search,
      query.category ?? "",
      query.limit ?? 0,
    ] as const,
    queryFn: ({ pageParam }) => listCatalog({ ...query, cursor: pageParam }),
    initialPageParam: undefined as string | undefined,
    getNextPageParam: (page: CatalogPage) => page.nextCursor,
  })
}

export function connectionsQueryOptions(
  organizationId: string,
  filter: ConnectionFilter = {}
) {
  return queryOptions({
    queryKey: [
      ...appsQueryKey(organizationId),
      "connections",
      filter.scope ?? "mine",
      filter.toolkit ?? "",
    ] as const,
    queryFn: () => listConnections(organizationId, filter),
  })
}

export function connectionQueryOptions(
  organizationId: string,
  connectionId: string
) {
  return queryOptions({
    queryKey: [
      ...appsQueryKey(organizationId),
      "connections",
      connectionId,
    ] as const,
    queryFn: () => getConnection(organizationId, connectionId),
  })
}

/**
 * A connection's targets, cached briefly: a member is asked once per visit,
 * since the repositories and channels an app holds do not come and go while
 * they are choosing one.
 */
export function connectionTargetsQueryOptions(
  organizationId: string,
  connectionId: string
) {
  return queryOptions({
    queryKey: [
      ...appsQueryKey(organizationId),
      "connections",
      connectionId,
      "targets",
    ] as const,
    queryFn: () => listConnectionTargets(organizationId, connectionId),
    staleTime: 60_000,
  })
}

export function triggersQueryOptions(
  organizationId: string,
  connectionId: string
) {
  return queryOptions({
    queryKey: [
      ...appsQueryKey(organizationId),
      "triggers",
      connectionId,
    ] as const,
    queryFn: () => listTriggers(organizationId, connectionId),
  })
}

// ---------------------------------------------------------------------------
// Composio's return trip
// ---------------------------------------------------------------------------

export type ConnectReturn = {
  connectionId: string
  status: string
}

/**
 * The connection Composio's callback sent the member's browser back with, if
 * this load is that return trip. `search` is the route's own search params.
 *
 * `apps/internal/httpserver/server.go`'s `completeConnection` sets exactly
 * two query parameters on the redirect: `app_connection` (the connection id)
 * and `status` (the connection's status at that moment — typically `active`,
 * `pending` or `failed`; see this package's README). It does **not** add a
 * `reason` or `status_reason` parameter — a failure's detail is only on the
 * connection itself, read with `getConnection`.
 */
export function parseConnectReturn(search: {
  app_connection?: string
  status?: string
}): ConnectReturn | undefined {
  if (!search.app_connection || !search.status) {
    return undefined
  }
  return { connectionId: search.app_connection, status: search.status }
}

export type ConnectNotice = {
  text: string
  tone: "danger" | "info" | "success"
}

/**
 * What to tell the member once their browser is back from connecting.
 * `detail` is what `getConnection` says about it: the app's name, and for a
 * failure its `status_reason`, which the redirect itself never carries.
 *
 * A connection still `pending` after the callback stays pending until the
 * sweep fails it a day later; nothing checks again sooner, so the copy
 * points at cancelling rather than waiting.
 */
export function connectReturnNotice(
  value: ConnectReturn,
  detail: { name?: string; reason?: string } = {}
): ConnectNotice {
  const name = detail.name || "The app"
  switch (value.status) {
    case "active":
      return { text: `${name} is connected and ready to use.`, tone: "success" }
    case "pending":
      return {
        text: `${name} is still connecting. If it doesn't finish, cancel it and connect again.`,
        tone: "info",
      }
    default:
      return {
        text: detail.reason
          ? `${name} didn't connect: ${connectionReasonText(detail.reason)}. Try again.`
          : `${name} didn't connect. Try again.`,
        tone: "danger",
      }
  }
}

/**
 * A connection's `status_reason` in a member's words. apps writes it either
 * in its own words (`CompleteConnection`, the maintenance sweep's "link
 * expired") or as Composio's own lowercased account status; an expired
 * connection carries Composio's free text, which is passed through.
 */
export function connectionReasonText(reason: string): string {
  switch (reason.trim()) {
    case "the member did not finish connecting":
      return "it wasn't finished at the app"
    case "link expired":
      return "the link expired before it was finished"
    case "failed":
      return "the app reported a failure"
    case "expired":
      return "the sign-in expired"
    case "revoked":
      return "access was revoked"
    case "inactive":
      return "the account is inactive"
    default:
      // Composio's own text arrives as a sentence; the caller adds the stop.
      return reason.trim().replace(/[.!?]+$/, "")
  }
}

// ---------------------------------------------------------------------------
// Status/label helpers
// ---------------------------------------------------------------------------

/**
 * A status read as a UI tone, independent of any particular component (there
 * is no shared status-orb/badge tone type in `web-next` yet to align with).
 */
export type StatusTone = "active" | "danger" | "muted" | "neutral" | "warning"

export function connectionStatusLabel(status: ConnectionStatus): string {
  switch (status) {
    case "active":
      return "Active"
    case "pending":
      return "Connecting…"
    case "expired":
      return "Expired"
    case "failed":
      return "Failed"
    case "removed":
      return "Removed"
    default:
      return status
  }
}

export function connectionStatusTone(status: ConnectionStatus): StatusTone {
  switch (status) {
    case "active":
      return "active"
    case "expired":
      return "warning"
    case "failed":
      return "danger"
    case "removed":
      return "muted"
    default:
      return "neutral"
  }
}

/**
 * What the member can do about a status, in a few words, for a line under a
 * connection with no `status_reason` of its own. A working connection says
 * nothing: its own tone has already said it.
 */
export function connectionStatusHint(status: ConnectionStatus): string {
  switch (status) {
    case "active":
      return ""
    case "pending":
      return "Waiting for the app to confirm the connection."
    case "expired":
      return "Reconnect to let your agents use it again."
    case "failed":
      return "Connecting did not finish. Try again."
    case "removed":
      return "Disconnected. Connect it again to bring it back."
    default:
      return ""
  }
}

/**
 * A readable name for an app the catalog has not told us about yet, built
 * from its slug: `google_calendar` reads as "Google Calendar".
 */
export function toolkitName(toolkit: string): string {
  return toolkit
    .split(/[_-]/)
    .filter(Boolean)
    .map((word) => word.charAt(0).toUpperCase() + word.slice(1))
    .join(" ")
}

/** Whether a connection has triggers and sources worth opening: only a live one does. */
export function hasConnectionDetails(status: ConnectionStatus): boolean {
  return status === "active"
}

/**
 * Whether a page should offer a plain reconnect (calling `connect` again)
 * for a connection in this status.
 *
 * `Service.Connect` in `apps/internal/application/connections.go` only
 * refuses (409 `conflict`) when the member already has a `pending` or
 * `active` connection to the toolkit — an `expired` or a `failed` one does
 * not block a fresh `connect()` call. Both are offered reconnect here; see
 * `README.md` for the UI implication (the old, expired/failed row is not
 * replaced automatically, so a page offering reconnect should also let the
 * member disconnect the stale row).
 */
export function canReconnect(status: ConnectionStatus): boolean {
  return status === "expired" || status === "failed"
}

/**
 * The stale rows a newer connection has replaced: a `failed` or `expired`
 * connection whose app now has an `active` connection created after it.
 * `connect()` always makes a new row and leaves the old one listed (see
 * README), and apps allows one pending-or-active connection per app and
 * member, so the newer active one is the member's current connection to that
 * app, though not necessarily the same account.
 *
 * Only the `failed` ones are safe to disconnect without asking: nothing
 * hangs off a connection that never connected. An `expired` one still holds
 * what its paused sources indexed, and disconnecting it retires them and
 * retracts those documents, so a page shows it as replaced and removes it
 * only when the member says so (behind a confirm step).
 */
export function replacedConnections(
  connections: readonly Connection[]
): Connection[] {
  const time = (value: string) => Date.parse(value) || 0
  return connections.filter(
    (stale) =>
      (stale.status === "failed" || stale.status === "expired") &&
      connections.some(
        (current) =>
          current.toolkit === stale.toolkit &&
          current.status === "active" &&
          time(current.created_at) > time(stale.created_at)
      )
  )
}

/** Why `toolkit` cannot be connected here, for a disabled catalog row. */
export function nonConnectableReason(toolkit: Toolkit): string | undefined {
  if (toolkit.connectable) {
    return undefined
  }
  return toolkit.no_auth
    ? "This app needs no connection."
    : "Connecting this app isn't set up yet."
}

export type ConnectionGroup = {
  toolkit: string
  connections: Connection[]
  activeCount: number
  attentionCount: number
}

/**
 * The organization's connections to each app, for an admin's summary.
 * `removed` connections are dropped: the view is who is connected now, not
 * everyone who ever was. (The list endpoint already excludes `removed` by
 * default — see README — so this is mostly defensive.)
 */
export function groupConnectionsByToolkit(
  connections: Connection[]
): ConnectionGroup[] {
  const groups = new Map<string, Connection[]>()
  for (const connection of connections) {
    if (connection.status === "removed") {
      continue
    }
    const list = groups.get(connection.toolkit) ?? []
    list.push(connection)
    groups.set(connection.toolkit, list)
  }
  return [...groups.entries()]
    .map(([toolkit, list]) => ({
      activeCount: list.filter((connection) => connection.status === "active")
        .length,
      attentionCount: list.filter(
        (connection) =>
          connection.status === "expired" || connection.status === "failed"
      ).length,
      connections: list,
      toolkit,
    }))
    .sort((a, b) => a.toolkit.localeCompare(b.toolkit))
}

/**
 * How a whole app reads in the admin summary. A group with nothing active and
 * nothing broken is still settling — saying "All connected" over a
 * connection that has not connected yet is the one thing this must not do.
 */
export function connectionGroupState(group: ConnectionGroup): {
  label: string
  tone: StatusTone
} {
  if (group.attentionCount > 0) {
    return { label: "Needs attention", tone: "warning" }
  }
  if (group.activeCount === 0) {
    return { label: "Connecting…", tone: "neutral" }
  }
  return { label: "All connected", tone: "active" }
}

// ---------------------------------------------------------------------------
// Errors
// ---------------------------------------------------------------------------

/**
 * apps' own error codes (`ErrorCode` in `apps/internal/httpserver/server.go`)
 * in words a member can act on. Every code that function can produce is
 * listed here or is left to fall through deliberately: `invalid_request`,
 * `unauthorized`, `forbidden` and `not_found` already get a reasonable,
 * specific message from Go's own `Message(err)` (used as `ApiError.message`),
 * so they are not overridden.
 */
const appsErrorCopy: Partial<Record<string, string>> = {
  choose_connection: "More than one connection could answer this. Choose one.",
  conflict: "That didn't go through because something changed. Try again.",
  needs_reconnect: "Reconnect this app to keep using it.",
  not_connectable: "This app can't be connected here yet.",
  not_connected: "Connect this app first.",
  not_read_only: "That can only be done through a read-only request.",
  rate_limited: "Too many requests. Try again in a minute.",
  unavailable: "The app catalog or Composio did not answer. Try again shortly.",
}

/**
 * apps' own error codes in words a member can act on; a generic fallback
 * otherwise. The code lookup runs first and regardless of status, because a
 * friendly code can ride a 5xx — `unavailable` is a 503 — and must not be
 * shadowed by the generic "any 5xx gets the fallback" rule below it, which
 * exists for codeless or genuinely opaque failures (apps' own `internal`
 * code, which always accompanies a 500, has no table entry and so still
 * falls through to it). `lib/api/client.ts` has no shared generic humanizer
 * to build on (unlike `web/`'s `apiErrorMessage`), so that fallback mirrors
 * `lib/auth/client.ts`'s `authErrorMessage` instead.
 */
export function appsErrorMessage(error: unknown, fallback: string): string {
  if (error instanceof ApiError && error.code) {
    const copy = appsErrorCopy[error.code]
    if (copy) {
      return copy
    }
  }
  if (!(error instanceof ApiError) || error.status >= 500) {
    return fallback
  }
  return error.message || fallback
}
