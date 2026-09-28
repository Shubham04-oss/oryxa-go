import { useEffect, useMemo, useRef, useState } from "react"
import {
  keepPreviousData,
  useInfiniteQuery,
  useMutation,
  useQueries,
  useQuery,
  useQueryClient,
} from "@tanstack/react-query"
import {
  ArrowRight,
  ArrowUpRight,
  Check,
  ChevronDown,
  CircleAlert,
  Info,
  LoaderCircle,
  Search,
  X,
} from "lucide-react"
import { ApiError } from "@/lib/api/client"
import { getSession } from "@/lib/auth/client"
import {
  appsErrorMessage,
  appsQueryKey,
  catalogQueryOptions,
  catalogSearchQueryOptions,
  catalogToolkitQueryOptions,
  connect,
  connectReturnNotice,
  connectionQueryOptions,
  connectionStatusLabel,
  connectionStatusTone,
  connectionsQueryOptions,
  disconnectConnection,
  hasConnectionDetails,
  nonConnectableReason,
  parseConnectReturn,
  replacedConnections,
} from "@/lib/apps/client"
import type {
  ConnectNotice,
  ConnectReturn,
  Connection,
  Toolkit,
} from "@/lib/apps/client"
import {
  featuredSlugs,
  groupToolkits,
  placeholderToolkit,
  previewConnections,
  previewToolkits,
} from "./workspace-tools-data"
import {
  connectRedirect,
  connectReturnTo,
  connectionCountLabel,
  withoutConnectReturn,
} from "./workspace-tool-helpers"
import { ConnectionWorkspace } from "./workspace-tool-connection"
import { ConnectionSummary } from "./workspace-tool-lifecycle"
import type { RemovalKind } from "./workspace-tool-lifecycle"
import { ToolkitLogo } from "./workspace-tool-logo"

type ViewState = "loading" | "live" | "preview"

const detailId = "workspace-tools-connection-detail"
// The shortest query Composio's catalog search takes: shorter ones it refuses
// as a validation error, which apps passes on as a 400.
const minimumCatalogSearch = 3

/**
 * The Apps service is missing rather than failing: nothing answered with
 * apps' own error code, or nothing serves the route. A coded failure
 * (Composio `unavailable`, say) means apps is here and the page stays live.
 */
function appsServiceAbsent(error: unknown): boolean {
  return !(error instanceof ApiError) || !error.code || error.status === 404
}

function isUnauthorized(error: unknown): boolean {
  return error instanceof ApiError && error.status === 401
}

function useDebouncedValue<T>(value: T, delay: number): T {
  const [debounced, setDebounced] = useState(value)
  useEffect(() => {
    const timeout = window.setTimeout(() => setDebounced(value), delay)
    return () => window.clearTimeout(timeout)
  }, [value, delay])
  return debounced
}

/**
 * Composio's return trip (`?app_connection=&status=`), read once; its
 * parameters come off the address straight away so a reload doesn't read it
 * twice. TanStack Router's history wraps `replaceState`, so the router sees
 * the new address too. Safe under StrictMode: a second run finds nothing.
 */
function useConnectReturn(): ConnectReturn | null {
  const [value, setValue] = useState<ConnectReturn | null>(null)
  useEffect(() => {
    const params = new URLSearchParams(window.location.search)
    const parsed = parseConnectReturn({
      app_connection: params.get("app_connection") ?? undefined,
      status: params.get("status") ?? undefined,
    })
    if (!parsed) return
    setValue(parsed)
    window.history.replaceState(
      window.history.state,
      "",
      withoutConnectReturn(window.location.href)
    )
  }, [])
  return value
}

function removalNotice(kind: RemovalKind, name: string): ConnectNotice {
  switch (kind) {
    case "cancelled":
      return {
        text: `Stopped connecting ${name}. You can connect it again below.`,
        tone: "info",
      }
    case "removed":
      return {
        text: `Removed the ${name} connection that failed.`,
        tone: "info",
      }
    case "replaced":
      return { text: `Removed the old ${name} connection.`, tone: "info" }
    default:
      return { text: `${name} disconnected.`, tone: "info" }
  }
}

export function WorkspaceTools({
  organizationId,
}: {
  organizationId?: string
}) {
  const queryClient = useQueryClient()
  const connectedSection = useRef<HTMLElement>(null)
  const [query, setQuery] = useState("")
  const [selectedId, setSelectedId] = useState<string | null>(null)
  const [expandedToolkit, setExpandedToolkit] = useState<string | null>(null)
  const [notice, setNotice] = useState<ConnectNotice | null>(null)
  const [returnDismissed, setReturnDismissed] = useState(false)
  const [keptIds, setKeptIds] = useState<ReadonlySet<string>>(new Set())
  const [retireProblem, setRetireProblem] = useState("")
  const returned = useConnectReturn()

  useEffect(() => {
    if (returned) setSelectedId(returned.connectionId)
  }, [returned])

  // One small read decides whether the Apps service is here at all.
  const probe = useQuery({
    ...catalogQueryOptions({ limit: 1 }),
    retry: false,
    staleTime: Number.POSITIVE_INFINITY,
  })
  const viewState: ViewState = probe.data
    ? "live"
    : probe.isPending
      ? "loading"
      : appsServiceAbsent(probe.error)
        ? "preview"
        : "live"
  const isPreview = viewState === "preview"
  const live = viewState === "live"
  const catalogUp = Boolean(probe.data)

  const connectionsQuery = useQuery({
    ...connectionsQueryOptions(organizationId ?? ""),
    enabled: live && Boolean(organizationId),
  })
  // Without a workspace there is nothing to list; the session says whether
  // that is because the member is signed out. The route has already read it.
  const session = useQuery({
    queryKey: ["auth", "session"],
    queryFn: getSession,
    retry: false,
    staleTime: 60_000,
    enabled: live && !organizationId,
  })
  const signedOut = organizationId
    ? isUnauthorized(connectionsQuery.error)
    : isUnauthorized(session.error)
  const signedIn = organizationId ? !signedOut : Boolean(session.data)
  const canConnect = live && Boolean(organizationId) && signedIn

  const connections = useMemo(
    () =>
      (connectionsQuery.data ?? []).filter(
        (connection) => connection.status !== "removed"
      ),
    [connectionsQuery.data]
  )

  // A reconnect makes a new connection and leaves the failed or expired one
  // listed. Once the new one is active, a failed row (nothing hangs off it)
  // is removed here; an expired row still holds indexed documents, and may
  // be another account, so it stays until the member removes it.
  const replaced = useMemo(
    () => replacedConnections(connections),
    [connections]
  )
  const replacedFailed = replaced.filter(
    (connection) => connection.status === "failed"
  )
  const replacedExpiredIds: ReadonlySet<string> = new Set(
    replaced
      .filter((connection) => connection.status === "expired")
      .map((connection) => connection.id)
  )
  const retire = useMutation({
    mutationFn: async (ids: string[]) => {
      const results = await Promise.allSettled(
        ids.map((id) => disconnectConnection(organizationId ?? "", id))
      )
      return ids.flatMap((id, index) => {
        const result = results[index]
        return result.status === "rejected"
          ? [{ id, error: result.reason as unknown }]
          : []
      })
    },
    onSuccess: (failures) => {
      if (!failures.length) return
      setKeptIds(
        (current) => new Set([...current, ...failures.map((item) => item.id)])
      )
      setRetireProblem(
        `Couldn't remove the failed connection you replaced. ${appsErrorMessage(
          failures[0].error,
          "Remove it from its details."
        )}`
      )
    },
    onSettled: () =>
      queryClient.invalidateQueries({
        queryKey: [...appsQueryKey(organizationId ?? ""), "connections"],
      }),
  })
  const retired = useRef(new Set<string>())
  const replacedKey = replacedFailed
    .map((connection) => connection.id)
    .join(" ")
  const retireReplaced = retire.mutate
  useEffect(() => {
    if (!organizationId || !replacedKey) return
    const fresh = replacedKey
      .split(" ")
      .filter((id) => !retired.current.has(id))
    if (!fresh.length) return
    for (const id of fresh) retired.current.add(id)
    retireReplaced(fresh)
  }, [organizationId, replacedKey, retireReplaced])

  const hiddenIds = new Set(
    replacedFailed
      .map((connection) => connection.id)
      .filter((id) => !keptIds.has(id))
  )
  const rowConnections = isPreview
    ? previewConnections
    : connections.filter((connection) => !hiddenIds.has(connection.id))
  const rowToolkits = new Set(
    rowConnections.map((connection) => connection.toolkit)
  )

  // The featured apps, and any app the member has connected beyond them, are
  // read one by one: the catalog describes them better than a slug does.
  const extraSlugs = [...rowToolkits].filter(
    (slug) => !featuredSlugs.includes(slug)
  )
  const toolkitReads = useQueries({
    queries: [...featuredSlugs, ...extraSlugs].map((slug) => ({
      ...catalogToolkitQueryOptions(slug),
      enabled: catalogUp,
    })),
  })
  const featuredReads = toolkitReads.slice(0, featuredSlugs.length)
  const featuredSettled = featuredReads.every((read) => !read.isPending)
  const featuredToolkits = featuredReads.flatMap((read) =>
    read.data ? [read.data] : []
  )

  const trimmed = query.trim()
  // Composio refuses a catalog search shorter than three characters, so a
  // shorter query only filters the apps already on screen.
  const searchable = [...trimmed].length >= minimumCatalogSearch
  const searchTerm = useDebouncedValue(searchable ? trimmed : "", 300)
  const search = useInfiniteQuery({
    ...catalogSearchQueryOptions({ search: searchTerm, limit: 100 }),
    enabled: catalogUp && searchTerm !== "",
    placeholderData: keepPreviousData,
  })
  const results =
    live && searchable && search.data
      ? search.data.pages.flatMap((page) => page.toolkits)
      : null
  const searchBusy =
    live && searchable && (trimmed !== searchTerm || search.isFetching)

  const knownToolkits = new Map<string, Toolkit>()
  for (const toolkit of isPreview ? previewToolkits : []) {
    knownToolkits.set(toolkit.toolkit, toolkit)
  }
  for (const read of toolkitReads) {
    if (read.data) knownToolkits.set(read.data.toolkit, read.data)
  }
  for (const toolkit of results ?? []) {
    if (!knownToolkits.has(toolkit.toolkit))
      knownToolkits.set(toolkit.toolkit, toolkit)
  }
  const toolkitFor = (slug: string) =>
    knownToolkits.get(slug) ?? placeholderToolkit(slug)

  const shownToolkits = isPreview ? previewToolkits : featuredToolkits
  const localMatches =
    trimmed && (isPreview || !searchable)
      ? shownToolkits.filter((toolkit) =>
          `${toolkit.name} ${toolkit.description}`
            .toLowerCase()
            .includes(trimmed.toLowerCase())
        )
      : null
  const visibleToolkits = (results ?? localMatches ?? shownToolkits).filter(
    (toolkit) => !rowToolkits.has(toolkit.toolkit)
  )
  const groups = groupToolkits(
    visibleToolkits,
    results === null && localMatches === null
  )

  const connectApp = useMutation({
    mutationFn: async (
      toolkit: string
    ): Promise<{ existing?: Connection; redirected?: true }> => {
      const workspace = organizationId ?? ""
      try {
        const started = await connect(workspace, {
          toolkit,
          returnTo: connectReturnTo(window.location.href, workspace),
        })
        window.location.assign(
          connectRedirect(started.redirect_url, window.location.origin)
        )
        return { redirected: true }
      } catch (error) {
        // apps keeps one pending-or-active connection per app and member,
        // and says so with 409 `conflict`. Show that one rather than trying
        // again (lib/apps/README.md).
        if (!(error instanceof ApiError) || error.code !== "conflict") {
          throw error
        }
        const current = await queryClient.fetchQuery({
          ...connectionsQueryOptions(workspace),
          staleTime: 0,
        })
        const existing = current.find(
          (connection) =>
            connection.toolkit === toolkit &&
            (connection.status === "active" || connection.status === "pending")
        )
        if (!existing) throw error
        return { existing }
      }
    },
    onSuccess: ({ existing }) => {
      if (!existing) return
      const name = toolkitFor(existing.toolkit).name
      setSelectedId(existing.id)
      setExpandedToolkit(null)
      setNotice({
        text: `${name} is already ${existing.status === "active" ? "connected" : "connecting"}. It's open in My tools.`,
        tone: "info",
      })
      // jsdom has no scrollIntoView; every browser does.
      const section = connectedSection.current
      if (section && "scrollIntoView" in section) {
        section.scrollIntoView({ block: "start" })
      }
    },
  })
  // Once the link is open the page is leaving, so the button stays "Opening…".
  const connectingToolkit =
    connectApp.isPending || connectApp.data?.redirected
      ? connectApp.variables
      : null
  const connectErrorFor = (slug: string) =>
    connectApp.isError && connectApp.variables === slug
      ? appsErrorMessage(connectApp.error, "Could not start the connection.")
      : undefined

  // The connection Composio sent the member back with: its reason, if it
  // failed, lives only on the connection itself.
  const returnDetailEnabled = Boolean(returned && organizationId && live)
  const returnDetail = useQuery({
    ...connectionQueryOptions(
      organizationId ?? "",
      returned?.connectionId ?? ""
    ),
    enabled: returnDetailEnabled,
    retry: false,
  })
  const returnNotice =
    returned &&
    viewState !== "loading" &&
    (!returnDetailEnabled || !returnDetail.isPending)
      ? connectReturnNotice(returned, {
          name: returnDetail.data
            ? toolkitFor(returnDetail.data.toolkit).name
            : undefined,
          reason: returnDetail.data?.status_reason,
        })
      : null
  const shownNotice = notice ?? (returnDismissed ? null : returnNotice)

  const selected = rowConnections.find(
    (connection) => connection.id === selectedId
  )
  const selectedToolkit = selected && toolkitFor(selected.toolkit)

  const rowLoading =
    viewState === "loading" ||
    (live && Boolean(organizationId) && connectionsQuery.isPending)
  const rowProblem =
    live && connectionsQuery.isError && !connectionsQuery.data && !signedOut
      ? appsErrorMessage(
          connectionsQuery.error,
          "Your connections could not be loaded."
        )
      : ""
  const exploreProblem =
    live && !catalogUp
      ? appsErrorMessage(probe.error, "The app catalog could not be loaded.")
      : live && searchable && search.isError && !search.data
        ? appsErrorMessage(search.error, "Search could not be loaded.")
        : ""

  function signIn() {
    const params = new URLSearchParams(window.location.search)
    params.set("view", "tools")
    const redirect = `${window.location.pathname}?${params}`
    window.location.assign(
      `/auth/sign-in?redirect=${encodeURIComponent(redirect)}`
    )
  }

  function dismissNotice() {
    setNotice(null)
    setReturnDismissed(true)
  }

  const NoticeIcon =
    shownNotice?.tone === "success"
      ? Check
      : shownNotice?.tone === "danger"
        ? CircleAlert
        : Info

  return (
    <div className="workspace-tools-page">
      {isPreview && (
        <div className="workspace-tools-notice" role="status">
          Layout preview · The Apps service is offline. Logos below are
          examples, not real connections.
        </div>
      )}
      {live && signedOut && (
        <div className="workspace-tools-notice" role="status">
          Sign in to see your connections and link a tool.
          <button type="button" onClick={signIn}>
            Sign in <ArrowUpRight size={14} />
          </button>
        </div>
      )}
      {live && !organizationId && signedIn && (
        <div className="workspace-tools-notice" role="status">
          Create or join a workspace to connect tools.
        </div>
      )}
      <div role="status" aria-live="polite">
        {shownNotice && (
          <div className="workspace-tools-notice" data-tone={shownNotice.tone}>
            <span className="workspace-tools-notice-text">
              <NoticeIcon size={14} aria-hidden="true" />
              {shownNotice.text}
            </span>
            <button
              type="button"
              aria-label="Dismiss message"
              onClick={dismissNotice}
            >
              <X size={14} />
            </button>
          </div>
        )}
      </div>
      {retireProblem && (
        <div className="workspace-tools-error" role="alert">
          {retireProblem}
          <button
            type="button"
            aria-label="Dismiss message"
            onClick={() => setRetireProblem("")}
          >
            <X size={14} />
          </button>
        </div>
      )}

      <section
        className="workspace-tools-connected"
        aria-label="Connected tools"
        ref={connectedSection}
      >
        <div className="workspace-tools-section-heading">
          <div>
            <span className="workspace-eyebrow">01 / MY TOOLS</span>
            <h3>{isPreview ? "Example connections" : "Connected tools"}</h3>
          </div>
          <span className="workspace-tools-count">
            {rowLoading
              ? "Loading"
              : isPreview
                ? `${rowConnections.length} examples`
                : signedOut
                  ? "Signed out"
                  : connectionCountLabel(rowConnections, replacedExpiredIds)}
          </span>
        </div>
        {rowLoading ? (
          <p className="workspace-tools-empty">Loading your toolkit…</p>
        ) : rowProblem ? (
          <p
            className="workspace-tools-empty workspace-tools-problem"
            role="alert"
          >
            {rowProblem}{" "}
            <button
              type="button"
              className="workspace-tools-retry"
              onClick={() => void connectionsQuery.refetch()}
            >
              Retry
            </button>
          </p>
        ) : rowConnections.length ? (
          <div className="workspace-tools-connected-line">
            {rowConnections.map((connection) => {
              const toolkit = toolkitFor(connection.toolkit)
              const isSelected = selectedId === connection.id
              const settling = !isPreview && connection.status !== "active"
              const superseded = replacedExpiredIds.has(connection.id)
              const state = superseded
                ? "Replaced"
                : connectionStatusLabel(connection.status)
              return (
                <button
                  className={
                    isSelected
                      ? "workspace-tools-connected-item is-selected"
                      : "workspace-tools-connected-item"
                  }
                  type="button"
                  key={connection.id}
                  data-status={connection.status}
                  aria-label={`Expand ${toolkit.name} connection${settling ? ` (${superseded ? "Replaced by your new connection" : state})` : ""}`}
                  aria-expanded={isSelected}
                  aria-controls={isSelected ? detailId : undefined}
                  onClick={() =>
                    setSelectedId((current) =>
                      current === connection.id ? null : connection.id
                    )
                  }
                >
                  <ToolkitLogo toolkit={toolkit} />
                  <span className="workspace-tools-connected-copy">
                    <span>{toolkit.name}</span>
                    {settling && (
                      <small
                        data-tone={
                          superseded
                            ? "muted"
                            : connectionStatusTone(connection.status)
                        }
                      >
                        {state}
                      </small>
                    )}
                  </span>
                </button>
              )
            })}
          </div>
        ) : (
          <p className="workspace-tools-empty">
            {live && signedOut
              ? "Sign in to see your connected tools."
              : "No connected tools yet. Choose an app below to get started."}
          </p>
        )}
        {selected && selectedToolkit && (
          // Keyed once, here: both panes start afresh for another connection.
          <div id={detailId} key={selected.id}>
            <ConnectionSummary
              connection={selected}
              toolkit={selectedToolkit}
              organizationId={organizationId ?? ""}
              preview={isPreview}
              replaced={replacedExpiredIds.has(selected.id)}
              reconnecting={connectingToolkit === selected.toolkit}
              connectBusy={Boolean(connectingToolkit)}
              reconnectError={connectErrorFor(selected.toolkit)}
              onReconnect={() => connectApp.mutate(selected.toolkit)}
              onRemoved={(connection, kind) => {
                setSelectedId(null)
                setNotice(
                  removalNotice(kind, toolkitFor(connection.toolkit).name)
                )
              }}
            />
            {hasConnectionDetails(selected.status) && (
              <ConnectionWorkspace
                connection={selected}
                toolkit={selectedToolkit}
                organizationId={organizationId ?? ""}
                preview={isPreview}
              />
            )}
          </div>
        )}
      </section>

      <section className="workspace-tools-discover" aria-label="Explore tools">
        <div className="workspace-tools-discover-head">
          <div>
            <span className="workspace-eyebrow">02 / EXPLORE</span>
            <h3>Find your next connection.</h3>
            <p>
              Browse by what your team needs, or search the Composio catalog.
            </p>
          </div>
          <label className="workspace-tools-search">
            <Search size={17} aria-hidden="true" />
            <span className="sr-only">Search tools</span>
            <input
              value={query}
              onChange={(event) => setQuery(event.target.value)}
              placeholder="Search tools"
              type="search"
            />
            {searchBusy && (
              <LoaderCircle className="workspace-tools-spinner" size={16} />
            )}
          </label>
        </div>
        {exploreProblem ? (
          <p
            className="workspace-tools-no-results workspace-tools-problem"
            role="alert"
          >
            {exploreProblem}{" "}
            <button
              type="button"
              className="workspace-tools-retry"
              onClick={() =>
                void (catalogUp ? search.refetch() : probe.refetch())
              }
            >
              Retry
            </button>
          </p>
        ) : groups.length ? (
          <div className="workspace-tools-groups">
            {groups.map((group) => (
              <div className="workspace-tools-group" key={group.name}>
                <div className="workspace-tools-group-heading">
                  <div>
                    <h4>{group.name}</h4>
                    {group.description && <p>{group.description}</p>}
                  </div>
                  <span>{String(group.toolkits.length).padStart(2, "0")}</span>
                </div>
                <div className="workspace-tools-grid">
                  {group.toolkits.map((toolkit) => {
                    const expanded = expandedToolkit === toolkit.toolkit
                    const connectable = canConnect && toolkit.connectable
                    const unavailable = canConnect
                      ? nonConnectableReason(toolkit)
                      : undefined
                    const connectError = connectErrorFor(toolkit.toolkit)
                    return (
                      <article
                        className={
                          expanded
                            ? "workspace-tool-card is-expanded"
                            : "workspace-tool-card"
                        }
                        key={toolkit.toolkit}
                      >
                        <div className="workspace-tool-card-main">
                          <ToolkitLogo toolkit={toolkit} />
                          <h5>{toolkit.name}</h5>
                          <ChevronDown
                            className="workspace-tool-card-chevron"
                            size={15}
                            aria-hidden="true"
                          />
                          <button
                            type="button"
                            className="workspace-tool-card-toggle"
                            aria-label={`${toolkit.name} details`}
                            aria-expanded={expanded}
                            onClick={() =>
                              setExpandedToolkit((openToolkit) =>
                                openToolkit === toolkit.toolkit
                                  ? null
                                  : toolkit.toolkit
                              )
                            }
                          />
                        </div>
                        {expanded && (
                          <div className="workspace-tool-card-details">
                            {toolkit.description && (
                              <p>{toolkit.description}</p>
                            )}
                            {connectable && (
                              <button
                                type="button"
                                className="workspace-tool-card-connect"
                                disabled={Boolean(connectingToolkit)}
                                onClick={() =>
                                  connectApp.mutate(toolkit.toolkit)
                                }
                                aria-label={`Connect ${toolkit.name}`}
                              >
                                {connectingToolkit === toolkit.toolkit ? (
                                  <>
                                    <LoaderCircle
                                      className="workspace-tools-spinner"
                                      size={14}
                                    />
                                    Opening…
                                  </>
                                ) : (
                                  <>
                                    Connect <ArrowUpRight size={14} />
                                  </>
                                )}
                              </button>
                            )}
                            {unavailable && (
                              <small className="workspace-tool-card-note">
                                {unavailable}
                              </small>
                            )}
                            {connectError && (
                              <p
                                className="workspace-tool-inline-error"
                                role="alert"
                              >
                                {connectError}
                              </p>
                            )}
                          </div>
                        )}
                      </article>
                    )
                  })}
                </div>
              </div>
            ))}
          </div>
        ) : (
          <p className="workspace-tools-no-results">
            {viewState === "loading" ||
            searchBusy ||
            (live && results === null && !featuredSettled)
              ? "Loading tools…"
              : live && trimmed && !searchable
                ? "Keep typing to search every app."
                : "No tools found. Try a different search."}
          </p>
        )}
        {live &&
          searchable &&
          !search.isPlaceholderData &&
          search.hasNextPage && (
            <button
              className="workspace-tools-more"
              type="button"
              disabled={search.isFetchingNextPage}
              onClick={() => void search.fetchNextPage()}
            >
              {search.isFetchingNextPage ? "Loading…" : "Browse more"}{" "}
              <ChevronDown size={16} />
            </button>
          )}
        {search.isFetchNextPageError && (
          <p className="workspace-tool-inline-error" role="alert">
            {appsErrorMessage(search.error, "Could not load more tools.")}
          </p>
        )}
        <div className="workspace-tools-footnote">
          <span>Powered by Composio</span>
          <ArrowRight size={14} />
          <span>Your connections stay private to you.</span>
        </div>
      </section>
    </div>
  )
}
