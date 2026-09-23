import { afterEach, describe, expect, it, vi } from "vitest"
import { ApiError } from "@/lib/api/client"
import {
  appsErrorMessage,
  canReconnect,
  connect,
  connectionGroupState,
  connectionQueryOptions,
  connectionStatusHint,
  connectionStatusLabel,
  connectionStatusTone,
  connectionsQueryOptions,
  connectionTargetsQueryOptions,
  connectReturnNotice,
  createTrigger,
  disconnectConnection,
  getCatalogToolkit,
  getConnection,
  groupConnectionsByToolkit,
  hasConnectionDetails,
  listCatalog,
  listCatalogTriggerTypes,
  listConnections,
  listConnectionTargets,
  listTriggers,
  nonConnectableReason,
  parseConnectReturn,
  removeTrigger,
  toolkitName,
  triggersQueryOptions,
} from "./client"
import type { Connection, Toolkit } from "./client"

function jsonResponse(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  })
}

function errorResponse(code: string, message: string, status: number) {
  return jsonResponse({ error: { code, message } }, status)
}

afterEach(() => vi.unstubAllGlobals())

function connection(overrides: Partial<Connection> = {}): Connection {
  return {
    id: "apc_1",
    toolkit: "github",
    status: "active",
    member_id: "user-1",
    auto_subscribe: true,
    created_at: "2026-09-15T00:00:00Z",
    updated_at: "2026-09-15T00:00:00Z",
    ...overrides,
  }
}

function toolkit(overrides: Partial<Toolkit> = {}): Toolkit {
  return {
    toolkit: "github",
    name: "GitHub",
    description: "Repositories and pull requests",
    categories: [],
    auth_schemes: ["OAUTH2"],
    connectable: true,
    tools_count: 874,
    triggers_count: 46,
    ...overrides,
  }
}

// ---------------------------------------------------------------------------
// Catalog contracts
// ---------------------------------------------------------------------------

describe("catalog API contracts", () => {
  it("lists the catalog with search/category/cursor/limit", async () => {
    const fetchMock = vi.fn(async () => jsonResponse({ toolkits: [] }))
    vi.stubGlobal("fetch", fetchMock)

    await listCatalog({
      category: "dev-tools",
      cursor: "c1",
      limit: 10,
      search: "gmail",
    })

    expect(fetchMock).toHaveBeenCalledWith(
      "/v1/apps/catalog?category=dev-tools&cursor=c1&limit=10&search=gmail",
      expect.objectContaining({ method: "GET" })
    )
  })

  it("reads next_cursor as nextCursor and leaves it undefined when absent", async () => {
    const fetchMock = vi.fn(async () =>
      jsonResponse({ toolkits: [], next_cursor: "c2" })
    )
    vi.stubGlobal("fetch", fetchMock)
    await expect(listCatalog()).resolves.toEqual({
      nextCursor: "c2",
      toolkits: [],
    })

    vi.stubGlobal(
      "fetch",
      vi.fn(async () => jsonResponse({ toolkits: [] }))
    )
    await expect(listCatalog()).resolves.toEqual({
      nextCursor: undefined,
      toolkits: [],
    })
  })

  it("gets one toolkit, URL-encoded", async () => {
    const fetchMock = vi.fn(async () => jsonResponse(toolkit()))
    vi.stubGlobal("fetch", fetchMock)
    await getCatalogToolkit("google / mail")
    expect(fetchMock).toHaveBeenCalledWith(
      "/v1/apps/catalog/google%20%2F%20mail",
      expect.objectContaining({ method: "GET" })
    )
  })

  it("lists a toolkit's trigger types with cursor and limit", async () => {
    const fetchMock = vi.fn(async () => jsonResponse({ triggers: [] }))
    vi.stubGlobal("fetch", fetchMock)
    await listCatalogTriggerTypes("gmail", { cursor: "c1", limit: 50 })
    expect(fetchMock).toHaveBeenCalledWith(
      "/v1/apps/catalog/gmail/triggers?cursor=c1&limit=50",
      expect.objectContaining({ method: "GET" })
    )
  })

  it("pages trigger types by following next_cursor until it is absent", async () => {
    const pages = [
      jsonResponse({ next_cursor: "page2", triggers: [{ trigger: "A" }] }),
      jsonResponse({ triggers: [{ trigger: "B" }] }),
    ]
    const fetchMock = vi.fn(async () => pages.shift())
    vi.stubGlobal("fetch", fetchMock)

    const first = await listCatalogTriggerTypes("gmail", { limit: 50 })
    expect(first.nextCursor).toBe("page2")
    const second = await listCatalogTriggerTypes("gmail", {
      cursor: first.nextCursor,
      limit: 50,
    })
    expect(second.nextCursor).toBeUndefined()
    expect(fetchMock).toHaveBeenNthCalledWith(
      2,
      "/v1/apps/catalog/gmail/triggers?cursor=page2&limit=50",
      expect.objectContaining({ method: "GET" })
    )
  })
})

// ---------------------------------------------------------------------------
// Connections contracts
// ---------------------------------------------------------------------------

describe("connections API contracts", () => {
  it("connects with toolkit, return_to, alias and auto_subscribe", async () => {
    const fetchMock = vi.fn(async () =>
      jsonResponse(
        {
          connection: connection({ status: "pending" }),
          redirect_url: "https://composio.test/link",
        },
        201
      )
    )
    vi.stubGlobal("fetch", fetchMock)

    const result = await connect("org_1", {
      alias: "Work",
      autoSubscribe: false,
      returnTo: "https://app.example.test/app/apps",
      toolkit: "gmail",
    })

    expect(result.redirect_url).toBe("https://composio.test/link")
    expect(fetchMock).toHaveBeenCalledWith(
      "/v1/organizations/org_1/apps/connections",
      expect.objectContaining({
        method: "POST",
        body: JSON.stringify({
          alias: "Work",
          auto_subscribe: false,
          return_to: "https://app.example.test/app/apps",
          toolkit: "gmail",
        }),
      })
    )
  })

  it("surfaces a 409 conflict when a connection is already pending or active", async () => {
    const fetchMock = vi.fn(async () =>
      errorResponse(
        "conflict",
        "gmail is already connected for this member",
        409
      )
    )
    vi.stubGlobal("fetch", fetchMock)

    await expect(
      connect("org_1", {
        returnTo: "https://app.example.test",
        toolkit: "gmail",
      })
    ).rejects.toMatchObject({ status: 409, code: "conflict" })
  })

  it("lists the caller's connections with no scope by default", async () => {
    const fetchMock = vi.fn(async () =>
      jsonResponse({ connections: [connection()] })
    )
    vi.stubGlobal("fetch", fetchMock)
    await expect(listConnections("org_1")).resolves.toEqual([connection()])
    expect(fetchMock).toHaveBeenCalledWith(
      "/v1/organizations/org_1/apps/connections",
      expect.objectContaining({ method: "GET" })
    )
  })

  it("lists with scope=organization and a toolkit filter", async () => {
    const fetchMock = vi.fn(async () => jsonResponse({ connections: [] }))
    vi.stubGlobal("fetch", fetchMock)
    await listConnections("org_1", { scope: "organization", toolkit: "gmail" })
    expect(fetchMock).toHaveBeenCalledWith(
      "/v1/organizations/org_1/apps/connections?scope=organization&toolkit=gmail",
      expect.objectContaining({ method: "GET" })
    )
  })

  it("gets one connection by id, URL-encoded", async () => {
    const fetchMock = vi.fn(async () =>
      jsonResponse(connection({ id: "apc / 1" }))
    )
    vi.stubGlobal("fetch", fetchMock)
    await getConnection("org_1", "apc / 1")
    expect(fetchMock).toHaveBeenCalledWith(
      "/v1/organizations/org_1/apps/connections/apc%20%2F%201",
      expect.objectContaining({ method: "GET" })
    )
  })

  it("disconnects with DELETE", async () => {
    const fetchMock = vi.fn(async () => new Response(null, { status: 204 }))
    vi.stubGlobal("fetch", fetchMock)
    await disconnectConnection("org_1", "apc_1")
    expect(fetchMock).toHaveBeenCalledWith(
      "/v1/organizations/org_1/apps/connections/apc_1",
      expect.objectContaining({ method: "DELETE" })
    )
  })

  it("lists a connection's targets", async () => {
    const fetchMock = vi.fn(async () => jsonResponse({ targets: [] }))
    vi.stubGlobal("fetch", fetchMock)
    await expect(listConnectionTargets("org_1", "apc_1")).resolves.toEqual([])
    expect(fetchMock).toHaveBeenCalledWith(
      "/v1/organizations/org_1/apps/connections/apc_1/targets",
      expect.objectContaining({ method: "GET" })
    )
  })
})

// ---------------------------------------------------------------------------
// Triggers contracts
// ---------------------------------------------------------------------------

describe("triggers API contracts", () => {
  it("lists triggers filtered by connection_id", async () => {
    const fetchMock = vi.fn(async () => jsonResponse({ triggers: [] }))
    vi.stubGlobal("fetch", fetchMock)
    await listTriggers("org_1", "apc_1")
    expect(fetchMock).toHaveBeenCalledWith(
      "/v1/organizations/org_1/apps/triggers?connection_id=apc_1",
      expect.objectContaining({ method: "GET" })
    )
  })

  it("lists every trigger with no connection filter", async () => {
    const fetchMock = vi.fn(async () => jsonResponse({ triggers: [] }))
    vi.stubGlobal("fetch", fetchMock)
    await listTriggers("org_1")
    expect(fetchMock).toHaveBeenCalledWith(
      "/v1/organizations/org_1/apps/triggers",
      expect.objectContaining({ method: "GET" })
    )
  })

  it("creates a trigger with connection_id, trigger and config, defaulting config to {}", async () => {
    const fetchMock = vi.fn(async () =>
      jsonResponse(
        {
          config: {},
          connection_id: "apc_1",
          created_at: "2026-09-15T00:00:00Z",
          delivery_count: 0,
          id: "apt_1",
          member_id: "user-1",
          status: "active",
          toolkit: "github",
          trigger: "GITHUB_COMMIT_EVENT",
          updated_at: "2026-09-15T00:00:00Z",
        },
        201
      )
    )
    vi.stubGlobal("fetch", fetchMock)

    await createTrigger("org_1", {
      connectionId: "apc_1",
      trigger: "GITHUB_COMMIT_EVENT",
    })

    expect(fetchMock).toHaveBeenCalledWith(
      "/v1/organizations/org_1/apps/triggers",
      expect.objectContaining({
        method: "POST",
        body: JSON.stringify({
          config: {},
          connection_id: "apc_1",
          trigger: "GITHUB_COMMIT_EVENT",
        }),
      })
    )
  })

  it("removes a trigger with DELETE", async () => {
    const fetchMock = vi.fn(async () => new Response(null, { status: 204 }))
    vi.stubGlobal("fetch", fetchMock)
    await removeTrigger("org_1", "apt_1")
    expect(fetchMock).toHaveBeenCalledWith(
      "/v1/organizations/org_1/apps/triggers/apt_1",
      expect.objectContaining({ method: "DELETE" })
    )
  })
})

// ---------------------------------------------------------------------------
// parseConnectReturn — Composio's return trip
// ---------------------------------------------------------------------------

describe("parseConnectReturn", () => {
  it("reads app_connection and status when both are present", () => {
    expect(
      parseConnectReturn({ app_connection: "apc_1", status: "active" })
    ).toEqual({
      connectionId: "apc_1",
      status: "active",
    })
  })

  it("is undefined when this load is not a return trip", () => {
    expect(parseConnectReturn({})).toBeUndefined()
    expect(parseConnectReturn({ status: "active" })).toBeUndefined()
    expect(parseConnectReturn({ app_connection: "apc_1" })).toBeUndefined()
  })
})

describe("connectReturnNotice", () => {
  it("welcomes an active connection", () => {
    expect(
      connectReturnNotice({ connectionId: "apc_1", status: "active" }).tone
    ).toBe("success")
  })

  it("explains a connection still finishing", () => {
    expect(
      connectReturnNotice({ connectionId: "apc_1", status: "pending" }).tone
    ).toBe("info")
  })

  it("flags anything else as not finished", () => {
    expect(
      connectReturnNotice({ connectionId: "apc_1", status: "failed" }).tone
    ).toBe("danger")
  })
})

// ---------------------------------------------------------------------------
// Status/label helpers
// ---------------------------------------------------------------------------

describe("connection status helpers", () => {
  it("labels and tones every status", () => {
    expect(connectionStatusLabel("active")).toBe("Active")
    expect(connectionStatusLabel("pending")).toBe("Connecting…")
    expect(connectionStatusTone("expired")).toBe("warning")
    expect(connectionStatusTone("failed")).toBe("danger")
    expect(connectionStatusTone("active")).toBe("active")
    expect(connectionStatusTone("removed")).toBe("muted")
    expect(connectionStatusTone("pending")).toBe("neutral")
  })

  it("says what to do about every status a member can act on, and nothing for active", () => {
    for (const status of ["expired", "failed", "pending", "removed"] as const) {
      expect(connectionStatusHint(status)).not.toBe("")
    }
    expect(connectionStatusHint("active")).toBe("")
  })

  it("only opens details for a connection that works", () => {
    expect(hasConnectionDetails("active")).toBe(true)
    for (const status of ["expired", "failed", "pending", "removed"] as const) {
      expect(hasConnectionDetails(status)).toBe(false)
    }
  })

  it("offers reconnect for expired AND failed, since Connect() only blocks pending/active", () => {
    expect(canReconnect("expired")).toBe(true)
    expect(canReconnect("failed")).toBe(true)
    expect(canReconnect("active")).toBe(false)
    expect(canReconnect("pending")).toBe(false)
    expect(canReconnect("removed")).toBe(false)
  })
})

describe("toolkitName", () => {
  it("reads a slug as a name", () => {
    expect(toolkitName("google_calendar")).toBe("Google Calendar")
    expect(toolkitName("hubspot")).toBe("Hubspot")
    expect(toolkitName("microsoft-teams")).toBe("Microsoft Teams")
  })

  it("survives a slug with empty parts", () => {
    expect(toolkitName("__gmail__")).toBe("Gmail")
    expect(toolkitName("")).toBe("")
  })
})

describe("nonConnectableReason", () => {
  it("says nothing about a connectable app", () => {
    expect(nonConnectableReason(toolkit({ connectable: true }))).toBeUndefined()
  })

  it("explains a no-auth app needs no connection", () => {
    expect(
      nonConnectableReason(toolkit({ connectable: false, no_auth: true }))
    ).toBe("This app needs no connection.")
  })

  it("explains an app with no OAuth app of Oryxa's own", () => {
    expect(
      nonConnectableReason(toolkit({ connectable: false, no_auth: false }))
    ).toBe("Connecting this app isn't set up yet.")
  })
})

describe("groupConnectionsByToolkit", () => {
  it("groups by toolkit, counts active and troubled connections, and drops removed ones", () => {
    const groups = groupConnectionsByToolkit([
      connection({ id: "1", status: "active", toolkit: "slack" }),
      connection({ id: "2", status: "active", toolkit: "github" }),
      connection({ id: "3", status: "expired", toolkit: "github" }),
      connection({ id: "4", status: "removed", toolkit: "github" }),
      connection({ id: "5", status: "failed", toolkit: "notion" }),
    ])

    expect(groups.map((group) => group.toolkit)).toEqual([
      "github",
      "notion",
      "slack",
    ])
    const github = groups.find((group) => group.toolkit === "github")
    expect(github?.connections).toHaveLength(2)
    expect(github?.activeCount).toBe(1)
    expect(github?.attentionCount).toBe(1)
  })

  it("leaves out a toolkit whose every connection was removed", () => {
    expect(
      groupConnectionsByToolkit([
        connection({ id: "1", status: "removed", toolkit: "github" }),
      ])
    ).toEqual([])
  })
})

describe("connectionGroupState", () => {
  const group = (activeCount: number, attentionCount: number) => ({
    activeCount,
    attentionCount,
    connections: [],
    toolkit: "gmail",
  })

  it("calls a working app connected", () => {
    expect(connectionGroupState(group(2, 0))).toEqual({
      label: "All connected",
      tone: "active",
    })
  })

  it("raises a hand for an app with a broken connection", () => {
    expect(connectionGroupState(group(1, 1))).toEqual({
      label: "Needs attention",
      tone: "warning",
    })
  })

  it("never calls an app connected while nothing about it is", () => {
    expect(connectionGroupState(group(0, 0))).toEqual({
      label: "Connecting…",
      tone: "neutral",
    })
  })
})

// ---------------------------------------------------------------------------
// appsErrorMessage
// ---------------------------------------------------------------------------

describe("appsErrorMessage", () => {
  it("uses apps' own copy for every known code", () => {
    expect(
      appsErrorMessage(new ApiError("x", 422, "not_connectable"), "fallback")
    ).toBe("This app can't be connected here yet.")
    expect(
      appsErrorMessage(new ApiError("x", 409, "conflict"), "fallback")
    ).toBe("That didn't go through because something changed. Try again.")
    expect(
      appsErrorMessage(new ApiError("x", 409, "needs_reconnect"), "fallback")
    ).toBe("Reconnect this app to keep using it.")
    expect(
      appsErrorMessage(new ApiError("x", 409, "not_connected"), "fallback")
    ).toBe("Connect this app first.")
    expect(
      appsErrorMessage(new ApiError("x", 409, "choose_connection"), "fallback")
    ).toBe("More than one connection could answer this. Choose one.")
    expect(
      appsErrorMessage(new ApiError("x", 403, "not_read_only"), "fallback")
    ).toBe("That can only be done through a read-only request.")
    expect(
      appsErrorMessage(new ApiError("x", 429, "rate_limited"), "fallback")
    ).toBe("Too many requests. Try again in a minute.")
    expect(
      appsErrorMessage(new ApiError("x", 503, "unavailable"), "fallback")
    ).toBe("The app catalog or Composio did not answer. Try again shortly.")
  })

  it("uses Go's own message for a code with no override, such as invalid_request", () => {
    expect(
      appsErrorMessage(
        new ApiError(
          "a toolkit is a lowercase slug such as github",
          400,
          "invalid_request"
        ),
        "fallback"
      )
    ).toBe("a toolkit is a lowercase slug such as github")
  })

  it("falls back to the generic wording on a 5xx, even with a code", () => {
    expect(
      appsErrorMessage(
        new ApiError("internal server error", 500, "internal"),
        "Could not save"
      )
    ).toBe("Could not save")
  })

  it("falls back for a non-ApiError failure", () => {
    expect(appsErrorMessage(new Error("boom"), "Could not save")).toBe(
      "Could not save"
    )
  })
})

// ---------------------------------------------------------------------------
// Query keys/options
// ---------------------------------------------------------------------------

describe("query options", () => {
  it("keys connections by organization, scope and toolkit, and loads through listConnections", async () => {
    const fetchMock = vi.fn(async () =>
      jsonResponse({ connections: [connection()] })
    )
    vi.stubGlobal("fetch", fetchMock)
    const options = connectionsQueryOptions("org_1", { scope: "organization" })
    expect(options.queryKey).toEqual([
      "apps",
      "org_1",
      "connections",
      "organization",
      "",
    ])
    await expect(options.queryFn?.({} as never)).resolves.toEqual([
      connection(),
    ])
  })

  it("keys one connection detail, not an encoded copy of the connections list key", async () => {
    const fetchMock = vi.fn(async () =>
      jsonResponse(connection({ id: "apc_detail" }))
    )
    vi.stubGlobal("fetch", fetchMock)
    const options = connectionQueryOptions("org_1", "apc_detail")
    expect(options.queryKey).toEqual([
      "apps",
      "org_1",
      "connections",
      "apc_detail",
    ])
    await expect(options.queryFn?.({} as never)).resolves.toMatchObject({
      id: "apc_detail",
    })
    expect(fetchMock).toHaveBeenCalledWith(
      "/v1/organizations/org_1/apps/connections/apc_detail",
      expect.objectContaining({ method: "GET" })
    )
  })

  it("keys connection targets under the connection", () => {
    const options = connectionTargetsQueryOptions("org_1", "apc_1")
    expect(options.queryKey).toEqual([
      "apps",
      "org_1",
      "connections",
      "apc_1",
      "targets",
    ])
    expect(options.staleTime).toBe(60_000)
  })

  it("keys triggers under the connection", () => {
    const options = triggersQueryOptions("org_1", "apc_1")
    expect(options.queryKey).toEqual(["apps", "org_1", "triggers", "apc_1"])
  })
})
