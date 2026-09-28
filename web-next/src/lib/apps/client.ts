import { infiniteQueryOptions, queryOptions } from "@tanstack/react-query"
import { ApiError, apiFetch, apiRequest, requestError } from "@/lib/api/client"

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
 * Who holds a connection's credential: Composio, the default, or Oryxa's own
 * sign-in (`apps/README.md`, "Oryxa's own sign-in").
 */
export type Broker = "composio" | "oryxa"

/**
 * One member's connection to one app. See `README.md` alongside this file for
 * what each status means, how long a pending connection lasts, and what a
 * page should offer for disconnect, reconnect and cancel-pending.
 */
export type Connection = {
  id: string
  toolkit: string
  /** Always sent by apps since Oryxa's own sign-in; missing means Composio. */
  broker?: Broker
  /**
   * An Oryxa connection's credential kind once it holds one (`oauth2_code`,
   * `api_token`, `basic`, `oauth2_client_credentials`). Everything but
   * `oauth2_code` can be replaced with `replaceCredentials`.
   */
  credential_kind?: string
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
 *
 * A toolkit `GET /v1/apps/integrations` lists connects through Oryxa's own
 * sign-in; every other one through Composio, with the same request as
 * before. Through Oryxa:
 *
 * - one OAuth way in that needs nothing typed answers the app's own sign-in
 *   page as `redirect_url`, exactly as Composio answers its link;
 * - anything else (a key, or a choice of ways in) opens the credential
 *   dialog first. A key connects at once, so `redirect_url` is then the
 *   return trip itself (`return_to` with `app_connection` and `status`), and
 *   the page reads it with `parseConnectReturn` as it reads a callback's.
 *   Closing the dialog rejects with `ConnectCancelledError`.
 */
export async function connect(
  organizationId: string,
  input: ConnectInput
): Promise<ConnectResult> {
  const integration = await oryxaIntegration(input.toolkit)
  if (!integration) {
    return apiRequest<ConnectResult>(connectionsPath(organizationId), {
      body: {
        alias: input.alias,
        auto_subscribe: input.autoSubscribe,
        return_to: input.returnTo,
        toolkit: input.toolkit,
      },
    })
  }
  const manifest = await getSigninManifest(integration.toolkit)
  const direct = directSignin(manifest)
  if (direct) {
    const started = await connectOryxa(organizationId, {
      autoSubscribe: input.autoSubscribe,
      kind: direct.kind,
      returnTo: input.returnTo,
      toolkit: integration.toolkit,
    })
    return nextStep(started, input.returnTo)
  }
  // The page that calls this (Tools) does not render the dialog itself, so
  // it is mounted on its own; see `connect-prompt.tsx`. That needs a
  // browser, and the server build leaves it out.
  if (import.meta.env.SSR) {
    throw new Error("Connecting an app needs a browser.")
  }
  const { promptConnect } = await import("./connect-prompt")
  const started = await promptConnect({
    autoSubscribe: input.autoSubscribe,
    manifest,
    organizationId,
    returnTo: input.returnTo,
  })
  if (!started) throw new ConnectCancelledError(manifest.name)
  return nextStep(started, input.returnTo)
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
  if (error instanceof ConnectCancelledError) {
    return `You closed ${error.appName}'s sign-in, so it isn't connected.`
  }
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

// ---------------------------------------------------------------------------
// Oryxa's own sign-in
// ---------------------------------------------------------------------------
//
// The second broker (`apps/README.md`, "Oryxa's own sign-in"). It ships
// switched off: `GET /v1/apps/integrations` lists nothing until
// APPS_ORYXA_TOOLKITS names a toolkit, and until then `connect()` sends every
// app through Composio exactly as before.
//
// Secrets a member or an admin types are write-only. The functions below take
// them, send them once and keep nothing; a caller passes them straight
// through, never through React state, a query cache or a mutation's
// variables, which TanStack Query keeps after it settles.

/** A toolkit Oryxa's own sign-in is switched on for. */
export type OryxaIntegration = {
  toolkit: string
  name: string
  /** The ways a member signs in to it, the default first. */
  kinds: string[]
}

/** What a field's value must be (`ValidationView` in `oryxa_manifest.go`). */
export type FieldValidation = {
  required: boolean
  /** `hostname`, `https_url`, `uuid` or `email`. */
  format?: string
  /** RE2, matched against the whole value. */
  pattern?: string
  enum?: string[]
  max_length?: number
}

/** One field of a way in, as a form asks for it. */
export type SigninField = {
  name: string
  label: string
  help?: string
  /** Write-only: masked while typed, never answered back. */
  secret: boolean
  /**
   * `connection` (the member types it), `client` (an admin, with the
   * organization's own OAuth app) or `discovered` (read from the app).
   */
  level: string
  /** The credential kind's own field rather than one the manifest declares. */
  standard?: boolean
  validation: FieldValidation
}

/** One way to sign in to an app. */
export type SigninMethod = {
  /** `oauth2_code`, `api_token`, `basic`, `oauth2_client_credentials`, or one the core added. */
  kind: string
  /** The OAuth client registration it signs in through, shared across toolkits (`google`). */
  client?: string
  scopes?: string[]
  fields: SigninField[]
}

/** A toolkit's sign-in, as a form: `GET /v1/apps/integrations/{toolkit}/manifest`. */
export type SigninManifest = {
  toolkit: string
  name: string
  version: number
  /** The ways in, the default first. */
  methods: SigninMethod[]
  /** The one callback to register with an OAuth app. */
  redirect_uri?: string
  webhook?: {
    scope: string
    organization_endpoint: boolean
    organization_secret: boolean
    verification_token: boolean
  }
}

/**
 * A failure from apps that may blame one field: `invalid_credentials` (400)
 * carries `field`, the field's name, and never its value.
 */
export class AppsFieldError extends ApiError {
  constructor(
    message: string,
    status: number,
    code: string | undefined,
    readonly field?: string
  ) {
    super(message, status, code)
    this.name = "AppsFieldError"
  }
}

/** The member closed the credential dialog before connecting. */
export class ConnectCancelledError extends Error {
  constructor(readonly appName: string) {
    super(`${appName}'s sign-in was closed before it connected`)
    this.name = "ConnectCancelledError"
  }
}

/** `apiRequest`, but a failure keeps the `field` an `invalid_credentials` answer names. */
async function appsRequest<T>(
  path: string,
  options: Parameters<typeof apiFetch>[1] = {}
): Promise<T> {
  const response = await apiFetch(path, options)
  if (!response.ok) {
    const field = await blamedField(response.clone())
    const error = await requestError(response)
    throw new AppsFieldError(error.message, error.status, error.code, field)
  }
  if (!response.headers.get("Content-Type")?.includes("application/json")) {
    return undefined as T
  }
  const data: T = await response.json()
  return data
}

async function blamedField(response: Response): Promise<string | undefined> {
  try {
    const body: { error?: { code?: unknown; field?: unknown } } =
      await response.json()
    const { code, field } = body.error ?? {}
    return code === "invalid_credentials" && typeof field === "string" && field
      ? field
      : undefined
  } catch {
    return undefined
  }
}

const integrationsPath = "/v1/apps/integrations"

/**
 * The toolkits Oryxa's own sign-in is switched on for, with the kinds each
 * signs in with. Public, like the catalog; empty while it is off.
 */
export async function listOryxaIntegrations(): Promise<OryxaIntegration[]> {
  const body = await apiRequest<
    { integrations?: OryxaIntegration[] } | undefined
  >(integrationsPath)
  return Array.isArray(body?.integrations) ? body.integrations : []
}

/** One toolkit's sign-in, as a form. Holds nothing secret. */
export function getSigninManifest(toolkit: string): Promise<SigninManifest> {
  return appsRequest<SigninManifest>(
    `${integrationsPath}/${encodeURIComponent(toolkit)}/manifest`
  )
}

/**
 * The Oryxa integration a toolkit connects through, or undefined for
 * Composio. An apps that has no such route (404) predates Oryxa's own
 * sign-in, so it is Composio's; any other failure is thrown rather than
 * guessed, since guessing would connect an app through the wrong broker.
 */
async function oryxaIntegration(
  toolkit: string
): Promise<OryxaIntegration | undefined> {
  let integrations: OryxaIntegration[]
  try {
    integrations = await listOryxaIntegrations()
  } catch (error) {
    if (error instanceof ApiError && error.status === 404) return undefined
    throw error
  }
  return integrations.find((integration) => integration.toolkit === toolkit)
}

/** The fields a member types for a way in: its standard ones and its connection-level ones. */
export function memberFields(method: SigninMethod): SigninField[] {
  return method.fields.filter((field) => field.level === "connection")
}

/** The fields an admin types with the organization's own OAuth app. */
export function clientFields(method: SigninMethod): SigninField[] {
  return method.fields.filter((field) => field.level === "client")
}

/**
 * The way in a toolkit signs in with straight away: its only one, OAuth,
 * with nothing for the member to type first. Anything else asks in the
 * credential dialog.
 */
export function directSignin(
  manifest: SigninManifest
): SigninMethod | undefined {
  if (manifest.methods.length !== 1) return undefined
  const [method] = manifest.methods
  return method.kind === "oauth2_code" && memberFields(method).length === 0
    ? method
    : undefined
}

export type OryxaConnectInput = {
  toolkit: string
  kind: string
  /** What the member typed: sent once, kept nowhere. */
  fields?: Record<string, string>
  returnTo: string
  autoSubscribe?: boolean
}

/** apps' answer: `redirect_url` for OAuth; a key's connection is already active. */
export type OryxaConnectResult = {
  connection: Connection
  redirect_url?: string
}

/**
 * Connects through Oryxa's own sign-in (`broker: "oryxa"`). For OAuth, apps
 * sets the sign-in's state cookie on this answer, so it must be a
 * credentialed request to the web app's own origin (`apiFetch`, with
 * `VITE_API_URL` unset as in production), and the browser then goes to
 * `redirect_url`. A key is tried with the app first and answers the
 * connection active, or `invalid_credentials`.
 */
export function connectOryxa(
  organizationId: string,
  input: OryxaConnectInput
): Promise<OryxaConnectResult> {
  const fields =
    input.fields && Object.keys(input.fields).length ? input.fields : undefined
  return appsRequest<OryxaConnectResult>(connectionsPath(organizationId), {
    body: {
      auto_subscribe: input.autoSubscribe,
      broker: "oryxa",
      fields,
      kind: input.kind,
      return_to: input.returnTo,
      toolkit: input.toolkit,
    },
  })
}

/**
 * Where the member's browser goes after an Oryxa connect: the app's own
 * sign-in page, or, for a key that connected at once, back to `returnTo`
 * with the parameters a callback would add.
 */
export function nextStep(
  started: OryxaConnectResult,
  returnTo: string
): ConnectResult {
  return {
    connection: started.connection,
    redirect_url:
      started.redirect_url || connectReturnUrl(returnTo, started.connection),
  }
}

/** `returnTo` with exactly the `app_connection` and `status` apps' callbacks add. */
export function connectReturnUrl(
  returnTo: string,
  connection: Pick<Connection, "id" | "status">
): string {
  const url = new URL(returnTo)
  url.searchParams.set("app_connection", connection.id)
  url.searchParams.set("status", connection.status)
  return url.toString()
}

/**
 * Gives an Oryxa connection a new key, password or client credentials. apps
 * keeps it only once the app accepts it in its test call; until then the
 * old one stays. The connection's own member only, and never OAuth.
 */
export async function replaceCredentials(
  organizationId: string,
  connectionId: string,
  input: { kind?: string; fields: Record<string, string> }
): Promise<Connection> {
  const { connection } = await appsRequest<{ connection: Connection }>(
    `${connectionPath(organizationId, connectionId)}/credentials`,
    { body: { fields: input.fields, kind: input.kind }, method: "PUT" }
  )
  return connection
}

export function isOryxaConnection(connection: Pick<Connection, "broker">) {
  return connection.broker === "oryxa"
}

/** Whether a connection has a key, a password or client credentials to replace. */
export function canReplaceCredential(
  connection: Pick<Connection, "broker" | "credential_kind" | "status">
): boolean {
  const kind = connection.credential_kind ?? ""
  return (
    isOryxaConnection(connection) &&
    connection.status === "active" &&
    kind !== "" &&
    kind !== "oauth2_code"
  )
}

/** A way in, named for a member choosing one. */
export function signinMethodLabel(kind: string, appName: string): string {
  switch (kind) {
    case "oauth2_code":
      return `Sign in with ${appName}`
    case "api_token":
      return "API token"
    case "basic":
      return "Username and password"
    case "oauth2_client_credentials":
      return "Client ID and secret"
    default:
      return toolkitName(kind)
  }
}

/** How an Oryxa connection signs in, in a member's words. */
export function signinDescription(
  connection: Pick<Connection, "credential_kind">,
  appName: string
): string {
  switch (connection.credential_kind) {
    case "oauth2_code":
      return `Signed in with ${appName} through Oryxa's own sign-in.`
    case "api_token":
      return "Connected with an API token through Oryxa's own sign-in."
    case "basic":
      return "Connected with a username and password through Oryxa's own sign-in."
    case "oauth2_client_credentials":
      return "Connected with a client ID and secret through Oryxa's own sign-in."
    default:
      return "Connected through Oryxa's own sign-in."
  }
}

// Organizations' own OAuth apps, for their admins -------------------------

export type VerificationTokenState = "awaited" | "ready" | "revealed"

/** An endpoint an organization's own app delivers to (Slack, Notion). */
export type OrganizationWebhook = {
  toolkit: string
  /** Where the app delivers, to copy into it; absent until the endpoint exists. */
  url?: string
  /** The admin enters the app's signing secret (`webhook_secrets`). */
  secret_required: boolean
  secret_set: boolean
  /** For an app that sends its own token once (Notion). */
  verification_token?: VerificationTokenState
  validated_at?: string
}

/** An organization's own OAuth app for one registration, as its admins see it: never a secret. */
export type OAuthClient = {
  client: string
  /** The organization saved its own app, or only a refusal of Oryxa's. */
  saved: boolean
  client_id?: string
  secret_set: boolean
  /** The secret's last four characters, when it is 16 or longer. */
  secret_hint?: string
  /** Client-level fields that are not secret. */
  fields: Record<string, string>
  /** Secret client-level fields that are set, each with its hint. */
  secret_fields: Record<string, string>
  /** The "don't use Oryxa's app" switch. */
  refuse_default: boolean
  /** Oryxa has an app of its own members sign in through otherwise. */
  oryxa_default: boolean
  redirect_uri?: string
  /** The apps switched on here that sign in through it. */
  toolkits: string[]
  webhooks: OrganizationWebhook[]
  created_by?: string
  created_at?: string
  updated_at?: string
}

export type OAuthClientInput = {
  clientId: string
  /** Write-only. Left out keeps the saved one; a new client ID needs one. */
  clientSecret?: string
  /** Client-level fields: one left out keeps its value, an empty one clears it. */
  fields?: Record<string, string>
  refuseDefault: boolean
  /** Signing secrets by toolkit, for apps an admin's secret signs (Slack). Write-only. */
  webhookSecrets?: Record<string, string>
}

function oauthClientPath(organizationId: string, client: string): string {
  return `/v1/organizations/${encodeURIComponent(organizationId)}/apps/oauth-clients/${encodeURIComponent(client)}`
}

export function getOAuthClient(
  organizationId: string,
  client: string
): Promise<OAuthClient> {
  return appsRequest<OAuthClient>(oauthClientPath(organizationId, client))
}

/** Saves an organization's own OAuth app, or its refusal of Oryxa's. Admins only. */
export function saveOAuthClient(
  organizationId: string,
  client: string,
  input: OAuthClientInput
): Promise<OAuthClient> {
  const has = (values?: Record<string, string>) =>
    values && Object.keys(values).length ? values : undefined
  return appsRequest<OAuthClient>(oauthClientPath(organizationId, client), {
    body: {
      client_id: input.clientId,
      client_secret: input.clientSecret || undefined,
      fields: has(input.fields),
      refuse_default: input.refuseDefault,
      webhook_secrets: has(input.webhookSecrets),
    },
    method: "PUT",
  })
}

/**
 * Retires an organization's own OAuth app, or its refusal: members sign in
 * through Oryxa's app again, and connections made through the removed one
 * keep refreshing through it. Removing what is not there is not an error.
 */
export async function removeOAuthClient(
  organizationId: string,
  client: string
): Promise<void> {
  await appsRequest(oauthClientPath(organizationId, client), {
    method: "DELETE",
  })
}

/**
 * The token an organization's own app sent its endpoint (Notion's), to paste
 * back into the app. apps shows it once: every later call answers 409.
 */
export async function revealVerificationToken(
  organizationId: string,
  toolkit: string
): Promise<string> {
  const { verification_token } = await appsRequest<{
    verification_token: string
  }>(
    `/v1/organizations/${encodeURIComponent(organizationId)}/apps/webhooks/${encodeURIComponent(toolkit)}/verification-token`,
    { method: "POST" }
  )
  return verification_token
}

export function oryxaIntegrationsQueryOptions() {
  return queryOptions({
    queryKey: ["apps", "oryxa", "integrations"] as const,
    queryFn: listOryxaIntegrations,
    staleTime: 5 * 60_000,
  })
}

export function signinManifestQueryOptions(toolkit: string) {
  return queryOptions({
    queryKey: ["apps", "oryxa", "manifest", toolkit] as const,
    queryFn: () => getSigninManifest(toolkit),
    staleTime: 5 * 60_000,
  })
}

export function oauthClientQueryOptions(
  organizationId: string,
  client: string
) {
  return queryOptions({
    queryKey: [
      ...appsQueryKey(organizationId),
      "oauth-clients",
      client,
    ] as const,
    queryFn: () => getOAuthClient(organizationId, client),
  })
}

// Checking what was typed ------------------------------------------------
//
// The same rules apps' signin checks (`checkValue` in
// `apps/internal/signin/values.go`), so a member hears about a mistake before
// it is sent. apps checks again either way. No message ever repeats a value.

/** What a value with no `max_length` of its own may be, as signin bounds it. */
const maxFieldLength = 4096

function hasControlCharacter(value: string): boolean {
  for (const character of value) {
    const code = character.codePointAt(0) ?? 0
    if (code < 0x20 || (code >= 0x7f && code <= 0x9f)) return true
  }
  return false
}

const hostLabel = /^[a-zA-Z0-9]([a-zA-Z0-9-]{0,61}[a-zA-Z0-9])?$/

/** A DNS name of two labels or more: never an address, never a single label. */
export function validHostname(value: string): boolean {
  if (value.length > 253) return false
  const labels = value.split(".")
  if (labels.length < 2 || !labels.every((label) => hostLabel.test(label))) {
    return false
  }
  return !/^\d+$/.test(labels[labels.length - 1])
}

/** An absolute https URL on a hostname, with no credentials, query or fragment. */
export function validHttpsUrl(value: string): boolean {
  if (!/^https:\/\//i.test(value) || /[?#]/.test(value)) return false
  try {
    const url = new URL(value)
    return (
      url.protocol === "https:" &&
      !url.username &&
      !url.password &&
      validHostname(url.hostname)
    )
  } catch {
    return false
  }
}

const uuidPattern =
  /^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$/

function validFormat(format: string, value: string): boolean | undefined {
  switch (format) {
    case "hostname":
      return validHostname(value)
    case "https_url":
      return validHttpsUrl(value)
    case "uuid":
      return uuidPattern.test(value)
    case "email": {
      const at = value.indexOf("@")
      const local = at < 0 ? "" : value.slice(0, at)
      return (
        local !== "" &&
        local.length <= 64 &&
        !/[ @<>"]/.test(local) &&
        validHostname(value.slice(at + 1))
      )
    }
    default:
      // A format only apps knows: it decides.
      return undefined
  }
}

function formatName(format: string): string {
  switch (format) {
    case "hostname":
      return "a hostname"
    case "https_url":
      return "an https URL"
    case "uuid":
      return "a UUID"
    case "email":
      return "an email address"
    default:
      return "valid"
  }
}

/** A manifest's RE2 pattern, anchored as apps anchors it, or undefined where JavaScript reads it differently. */
function anchored(pattern?: string): RegExp | undefined {
  if (!pattern) return undefined
  try {
    return new RegExp(`^(?:${pattern})$`)
  } catch {
    return undefined
  }
}

/**
 * The rule `raw` breaks for `field`, as a sentence naming the field by its
 * label, or undefined when it passes. Values are trimmed, as apps trims them.
 */
export function fieldProblem(
  field: Pick<SigninField, "label" | "validation">,
  raw: string
): string | undefined {
  const value = raw.trim()
  const { label, validation } = field
  if (!value) return validation.required ? `${label} is required.` : undefined
  if (hasControlCharacter(value)) return `${label} must be one line of text.`
  const limit =
    validation.max_length && validation.max_length > 0
      ? validation.max_length
      : maxFieldLength
  if ([...value].length > limit) {
    return `${label} must be at most ${limit} characters.`
  }
  if (validation.enum?.length && !validation.enum.includes(value)) {
    return `${label} must be one of ${validation.enum.join(", ")}.`
  }
  if (validation.format && validFormat(validation.format, value) === false) {
    return `${label} must be ${formatName(validation.format)}.`
  }
  const pattern = anchored(validation.pattern)
  if (pattern && !pattern.test(value)) {
    return `${label} isn't in the form this app expects.`
  }
  return undefined
}

/** How long an OAuth client's id may be, as signin bounds it. */
const maxClientIdLength = 512

/** The rule an OAuth client ID breaks, or undefined. */
export function clientIdProblem(raw: string): string | undefined {
  const value = raw.trim()
  if (
    [...value].length > maxClientIdLength ||
    /\s/.test(value) ||
    hasControlCharacter(value)
  ) {
    return `Client ID must be one word of at most ${maxClientIdLength} characters.`
  }
  return undefined
}

/** Bounds on a webhook signing secret an admin enters, as apps holds them. */
const minWebhookSecret = 16
const maxWebhookSecret = 1024

/** The rule a webhook signing secret breaks, or undefined. Never quotes it. */
export function webhookSecretProblem(
  label: string,
  raw: string
): string | undefined {
  const value = raw.trim()
  if (value.length < minWebhookSecret || value.length > maxWebhookSecret) {
    return `${label} must be ${minWebhookSecret} to ${maxWebhookSecret} characters.`
  }
  for (const character of value) {
    const code = character.codePointAt(0) ?? 0
    if (code <= 0x20 || code > 0x7e) {
      return `${label} must be printable characters without spaces.`
    }
  }
  return undefined
}

/**
 * apps' wording for a field it refused (`token was refused by the app`), as a
 * sentence led by the field's label instead of its name. The wording never
 * carries a value.
 */
export function refusedFieldText(
  message: string,
  field: string,
  label: string
): string {
  const text = message.trim()
  const rest = text.startsWith(`${field} `)
    ? text.slice(field.length + 1)
    : "wasn't accepted"
  return sentenceOf(`${label} ${rest}`)
}

function sentenceOf(text: string): string {
  const trimmed = text.trim()
  const capital = trimmed.charAt(0).toUpperCase() + trimmed.slice(1)
  return /[.!?]$/.test(capital) ? capital : `${capital}.`
}
