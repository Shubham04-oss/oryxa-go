import { useEffect, useMemo, useState } from "react"
import {
  ArrowRight,
  ArrowUpRight,
  Check,
  ChevronDown,
  LoaderCircle,
  Search,
  X,
} from "lucide-react"
import {
  featuredGroups,
  groupToolkits,
  previewConnections,
  previewToolkits,
  toolkitIcon,
} from "./workspace-tools-data"
import { ApiError, readApi } from "./workspace-tools-api"
import { ConnectionWorkspace } from "./workspace-tool-connection"
import type { Connection, Organization, Toolkit } from "./workspace-tools-data"

type ViewState = "loading" | "live" | "preview"
type AuthState = "loading" | "signed-in" | "signed-out" | "unavailable"

function ToolkitLogo({ toolkit }: { toolkit: Toolkit }) {
  const icon = toolkitIcon(toolkit.toolkit)
  const remoteLogo = toolkit.logo && /^https?:\/\//.test(toolkit.logo)

  return (
    <span className="workspace-tool-logo" aria-hidden="true">
      {icon ? (
        <svg viewBox="0 0 24 24" fill={`#${icon.hex}`}>
          <path d={icon.path} />
        </svg>
      ) : remoteLogo ? (
        <img src={toolkit.logo} alt="" loading="lazy" />
      ) : (
        <span>{toolkit.name.slice(0, 1).toUpperCase()}</span>
      )}
    </span>
  )
}

export function WorkspaceTools() {
  const [viewState, setViewState] = useState<ViewState>("loading")
  const [authState, setAuthState] = useState<AuthState>("loading")
  const [toolkits, setToolkits] = useState<Toolkit[]>([])
  const [connections, setConnections] = useState<Connection[]>([])
  const [organizations, setOrganizations] = useState<Organization[]>([])
  const [organizationId, setOrganizationId] = useState("")
  const [query, setQuery] = useState("")
  const [results, setResults] = useState<Toolkit[] | null>(null)
  const [nextCursor, setNextCursor] = useState<string | null>(null)
  const [searching, setSearching] = useState(false)
  const [selectedConnection, setSelectedConnection] = useState<string | null>(
    null
  )
  const [expandedToolkit, setExpandedToolkit] = useState<string | null>(null)
  const [connecting, setConnecting] = useState<string | null>(null)
  const [message, setMessage] = useState("")

  useEffect(() => {
    let cancelled = false

    async function load() {
      try {
        // One small read checks that the Apps service is present before the
        // featured catalog fans out into individual toolkit reads.
        await readApi<{ toolkits: Toolkit[] }>("/v1/apps/catalog?limit=1")
      } catch {
        if (cancelled) return
        setViewState("preview")
        setAuthState("unavailable")
        setToolkits(previewToolkits)
        return
      }

      const slugs = featuredGroups.flatMap((group) => [...group.slugs])
      const [catalog, identity] = await Promise.all([
        Promise.allSettled(
          slugs.map((slug) =>
            readApi<Toolkit>(`/v1/apps/catalog/${encodeURIComponent(slug)}`)
          )
        ),
        readApi<{ organizations: Organization[] }>("/v1/auth/me")
          .then((value) => ({ ok: true as const, value }))
          .catch((error: unknown) => ({ ok: false as const, error })),
      ])
      if (cancelled) return

      setToolkits(
        catalog.flatMap((result) =>
          result.status === "fulfilled" ? [result.value] : []
        )
      )
      setViewState("live")
      if (!identity.ok) {
        setAuthState(
          identity.error instanceof ApiError && identity.error.status === 401
            ? "signed-out"
            : "unavailable"
        )
      } else {
        setAuthState("signed-in")
        setOrganizations(identity.value.organizations)
        setOrganizationId(identity.value.organizations[0]?.id || "")
      }
    }

    void load()
    return () => {
      cancelled = true
    }
  }, [])

  useEffect(() => {
    if (viewState !== "live" || !organizationId) return
    let cancelled = false
    setConnections([])
    readApi<{ connections: Connection[] }>(
      `/v1/organizations/${encodeURIComponent(organizationId)}/apps/connections`
    )
      .then(async (data) => {
        if (!cancelled) setConnections(data.connections)
        const knownSlugs = new Set<string>(
          featuredGroups.flatMap((group) => [...group.slugs])
        )
        const missingSlugs = [
          ...new Set(
            data.connections
              .filter((connection) => connection.status === "active")
              .map((connection) => connection.toolkit)
              .filter((slug) => !knownSlugs.has(slug))
          ),
        ]
        const details = await Promise.allSettled(
          missingSlugs.map((slug) =>
            readApi<Toolkit>(`/v1/apps/catalog/${encodeURIComponent(slug)}`)
          )
        )
        if (cancelled) return
        setToolkits((current) => {
          const map = new Map(
            current.map((toolkit) => [toolkit.toolkit, toolkit])
          )
          for (const detail of details) {
            if (detail.status === "fulfilled")
              map.set(detail.value.toolkit, detail.value)
          }
          return [...map.values()]
        })
      })
      .catch((error: unknown) => {
        if (!cancelled)
          setMessage(
            error instanceof Error
              ? `Connections could not be loaded: ${error.message}`
              : "Connections could not be loaded."
          )
      })
    return () => {
      cancelled = true
    }
  }, [viewState, organizationId])

  useEffect(() => {
    if (!query.trim() || viewState !== "live") {
      setResults(null)
      setNextCursor(null)
      return
    }
    const controller = new AbortController()
    const timeout = window.setTimeout(async () => {
      setSearching(true)
      try {
        const params = new URLSearchParams({
          search: query.trim(),
          limit: "100",
        })
        const data = await readApi<{
          toolkits: Toolkit[]
          next_cursor: string | null
        }>(`/v1/apps/catalog?${params}`, { signal: controller.signal })
        if (!controller.signal.aborted) {
          setResults(data.toolkits)
          setNextCursor(data.next_cursor)
        }
      } catch (error) {
        if (!controller.signal.aborted)
          setMessage(
            error instanceof Error
              ? `Search could not be loaded: ${error.message}`
              : "Search could not be loaded."
          )
      } finally {
        if (!controller.signal.aborted) setSearching(false)
      }
    }, 300)
    return () => {
      window.clearTimeout(timeout)
      controller.abort()
    }
  }, [query, viewState])

  const isPreview = viewState === "preview"
  const activeConnections = (
    isPreview ? previewConnections : connections
  ).filter((connection) => connection.status === "active")
  const connectedSlugs = new Set(
    activeConnections.map((connection) => connection.toolkit)
  )
  const allToolkits = useMemo(() => {
    const map = new Map(toolkits.map((toolkit) => [toolkit.toolkit, toolkit]))
    for (const toolkit of results || []) map.set(toolkit.toolkit, toolkit)
    return map
  }, [toolkits, results])
  const visibleToolkits = (
    results ||
    (isPreview && query.trim()
      ? toolkits.filter((toolkit) =>
          `${toolkit.name} ${toolkit.description}`
            .toLowerCase()
            .includes(query.trim().toLowerCase())
        )
      : toolkits)
  ).filter((toolkit) => !connectedSlugs.has(toolkit.toolkit))
  const groups = groupToolkits(
    visibleToolkits,
    !query.trim() || (viewState === "live" && results === null)
  )
  const selected = activeConnections.find(
    (connection) => connection.id === selectedConnection
  )
  const selectedToolkit = selected && allToolkits.get(selected.toolkit)

  async function loadMore() {
    if (!nextCursor) return
    setSearching(true)
    try {
      const params = new URLSearchParams({
        search: query.trim(),
        cursor: nextCursor,
        limit: "100",
      })
      const data = await readApi<{
        toolkits: Toolkit[]
        next_cursor: string | null
      }>(`/v1/apps/catalog?${params}`)
      setResults((current) => [...(current || []), ...data.toolkits])
      setNextCursor(data.next_cursor)
    } catch (error) {
      setMessage(
        error instanceof Error ? error.message : "Could not load more tools."
      )
    } finally {
      setSearching(false)
    }
  }

  async function connect(toolkit: Toolkit) {
    if (!organizationId || !toolkit.connectable || connecting) return
    setConnecting(toolkit.toolkit)
    setMessage("")
    try {
      const returnTo = new URL(window.location.pathname, window.location.origin)
      returnTo.searchParams.set("view", "tools")
      const data = await readApi<{ redirect_url: string }>(
        `/v1/organizations/${encodeURIComponent(organizationId)}/apps/connections`,
        {
          method: "POST",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify({
            toolkit: toolkit.toolkit,
            return_to: returnTo.toString(),
          }),
        }
      )
      const target = new URL(data.redirect_url)
      if (
        target.protocol !== "https:" &&
        target.origin !== window.location.origin
      )
        throw new Error("The connection link was not secure.")
      window.location.assign(target.toString())
    } catch (error) {
      setMessage(
        error instanceof Error
          ? error.message
          : "Could not start the connection."
      )
      setConnecting(null)
    }
  }

  function signIn() {
    const returnTo = new URL(window.location.pathname, window.location.origin)
    returnTo.searchParams.set("view", "tools")
    window.location.assign(
      `/v1/auth/login?return_to=${encodeURIComponent(returnTo.toString())}`
    )
  }

  return (
    <div className="workspace-tools-page">
      {organizations.length > 1 && (
        <div className="workspace-tools-intro">
          <label className="workspace-tools-org">
            Workspace
            <select
              value={organizationId}
              onChange={(event) => setOrganizationId(event.target.value)}
            >
              {organizations.map((organization) => (
                <option key={organization.id} value={organization.id}>
                  {organization.name}
                </option>
              ))}
            </select>
          </label>
        </div>
      )}

      {isPreview && (
        <div className="workspace-tools-notice" role="status">
          Layout preview · The Apps service is offline. Logos below are
          examples, not real connections.
        </div>
      )}
      {viewState === "live" && authState === "signed-out" && (
        <div className="workspace-tools-notice" role="status">
          Sign in to see your connections and link a tool.
          <button type="button" onClick={signIn}>
            Sign in <ArrowUpRight size={14} />
          </button>
        </div>
      )}
      {viewState === "live" && authState === "signed-in" && !organizationId && (
        <div className="workspace-tools-notice" role="status">
          Create or join a workspace to connect tools.
        </div>
      )}
      {message && (
        <div className="workspace-tools-error" role="alert">
          {message}
          <button
            type="button"
            aria-label="Dismiss message"
            onClick={() => setMessage("")}
          >
            <X size={14} />
          </button>
        </div>
      )}

      <section
        className="workspace-tools-connected"
        aria-label="Connected tools"
      >
        <div className="workspace-tools-section-heading">
          <div>
            <span className="workspace-eyebrow">01 / MY TOOLS</span>
            <h3>{isPreview ? "Example connections" : "Connected tools"}</h3>
          </div>
          <span className="workspace-tools-count">
            {viewState === "loading"
              ? "Loading"
              : `${activeConnections.length} ${isPreview ? "examples" : "connected"}`}
          </span>
        </div>
        {viewState === "loading" ? (
          <p className="workspace-tools-empty">Loading your toolkit…</p>
        ) : activeConnections.length ? (
          <div className="workspace-tools-connected-line">
            {activeConnections.map((connection) => {
              const toolkit = allToolkits.get(connection.toolkit)
              if (!toolkit) return null
              return (
                <button
                  className={
                    selectedConnection === connection.id
                      ? "workspace-tools-connected-item is-selected"
                      : "workspace-tools-connected-item"
                  }
                  type="button"
                  key={connection.id}
                  aria-label={`Expand ${toolkit.name} connection`}
                  aria-expanded={selectedConnection === connection.id}
                  onClick={() =>
                    setSelectedConnection((current) =>
                      current === connection.id ? null : connection.id
                    )
                  }
                >
                  <ToolkitLogo toolkit={toolkit} />
                  <span>{toolkit.name}</span>
                </button>
              )
            })}
          </div>
        ) : (
          <p className="workspace-tools-empty">
            No connected tools yet. Choose an app below to get started.
          </p>
        )}
        {selected && selectedToolkit && (
          <>
            <div className="workspace-tools-expanded">
              <ToolkitLogo toolkit={selectedToolkit} />
              <div>
                <strong>{selectedToolkit.name}</strong>
                <p>{selectedToolkit.description}</p>
                <small>
                  {isPreview
                    ? "Example connection · no account linked"
                    : selected.label || "Connected to your account"}
                </small>
              </div>
              <span className="workspace-tools-connected-badge">
                <Check size={13} /> {isPreview ? "Preview" : "Connected"}
              </span>
            </div>
            <ConnectionWorkspace
              key={selected.id}
              connection={selected}
              toolkit={selectedToolkit}
              organizationId={organizationId}
              preview={isPreview}
            />
          </>
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
            {searching && (
              <LoaderCircle className="workspace-tools-spinner" size={16} />
            )}
          </label>
        </div>
        {groups.length ? (
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
                    const current = connections.find(
                      (connection) => connection.toolkit === toolkit.toolkit
                    )
                    const pending = current?.status === "pending"
                    const canConnect =
                      viewState === "live" &&
                      authState === "signed-in" &&
                      Boolean(organizationId) &&
                      toolkit.connectable &&
                      !pending
                    const expanded = expandedToolkit === toolkit.toolkit
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
                            <p>{toolkit.description}</p>
                            {canConnect && (
                              <button
                                type="button"
                                className="workspace-tool-card-connect"
                                disabled={Boolean(connecting)}
                                onClick={() => void connect(toolkit)}
                                aria-label={`Connect ${toolkit.name}`}
                              >
                                {connecting === toolkit.toolkit ? (
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
            {viewState === "loading" || searching
              ? "Loading tools…"
              : "No tools found. Try a different search."}
          </p>
        )}
        {nextCursor && (
          <button
            className="workspace-tools-more"
            type="button"
            disabled={searching}
            onClick={() => void loadMore()}
          >
            Browse more <ChevronDown size={16} />
          </button>
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
