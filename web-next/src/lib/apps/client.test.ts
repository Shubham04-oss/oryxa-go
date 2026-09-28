import { afterEach, describe, expect, it, vi } from "vitest"
import { ApiError } from "@/lib/api/client"
import {
  AppsFieldError,
  ConnectCancelledError,
  appsErrorMessage,
  canReconnect,
  canReplaceCredential,
  clientIdProblem,
  connectOryxa,
  connectReturnUrl,
  directSignin,
  fieldProblem,
  getOAuthClient,
  isOryxaConnection,
  listOryxaIntegrations,
  nextStep,
  oauthClientQueryOptions,
  oryxaIntegrationsQueryOptions,
  refusedFieldText,
  removeOAuthClient,
  replaceCredentials,
  revealVerificationToken,
  saveOAuthClient,
  signinDescription,
  signinManifestQueryOptions,
  signinMethodLabel,
  webhookSecretProblem,
  catalogSearchQueryOptions,
  catalogTriggerTypesQueryOptions,
  connect,
  connectionGroupState,
  connectionReasonText,
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
  listAllCatalogTriggerTypes,
  listCatalog,
  listCatalogTriggerTypes,
  listConnections,
  listConnectionTargets,
  listTriggers,
  nonConnectableReason,
  parseConnectReturn,
  removeTrigger,
  replacedConnections,
  toolkitName,
  triggerTypePagesLimit,
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

  it("reads every trigger type by following next_cursor, and stops at the page limit", async () => {
    const pages = [
      jsonResponse({ next_cursor: "page2", triggers: [{ trigger: "A" }] }),
      jsonResponse({ triggers: [{ trigger: "B" }] }),
    ]
    const fetchMock = vi.fn(async () => pages.shift())
    vi.stubGlobal("fetch", fetchMock)
    await expect(listAllCatalogTriggerTypes("gmail")).resolves.toEqual([
      { trigger: "A" },
      { trigger: "B" },
    ])
    expect(fetchMock).toHaveBeenNthCalledWith(
      1,
      "/v1/apps/catalog/gmail/triggers?limit=50",
      expect.anything()
    )
    expect(fetchMock).toHaveBeenNthCalledWith(
      2,
      "/v1/apps/catalog/gmail/triggers?cursor=page2&limit=50",
      expect.anything()
    )

    const endless = vi.fn(async () =>
      jsonResponse({ next_cursor: "more", triggers: [{ trigger: "X" }] })
    )
    vi.stubGlobal("fetch", endless)
    const all = await catalogTriggerTypesQueryOptions("gmail").queryFn?.(
      {} as never
    )
    expect(all).toHaveLength(triggerTypePagesLimit)
    expect(endless).toHaveBeenCalledTimes(triggerTypePagesLimit)
  })

  it("searches the catalog a page at a time, each page's cursor fetching the next", async () => {
    const fetchMock = vi.fn(async () =>
      jsonResponse({ toolkits: [toolkit()], next_cursor: "c2" })
    )
    vi.stubGlobal("fetch", fetchMock)
    const options = catalogSearchQueryOptions({ search: "git", limit: 100 })
    expect(options.queryKey).toEqual([
      "apps",
      "catalog",
      "search",
      "git",
      "",
      100,
    ])
    expect(options.initialPageParam).toBeUndefined()
    const page = await options.queryFn?.({ pageParam: "c1" } as never)
    expect(fetchMock).toHaveBeenCalledWith(
      "/v1/apps/catalog?cursor=c1&limit=100&search=git",
      expect.anything()
    )
    expect(page).toEqual({ nextCursor: "c2", toolkits: [toolkit()] })
    expect(
      options.getNextPageParam({ toolkits: [], nextCursor: "c3" }, [], "c1", [])
    ).toBe("c3")
    expect(
      options.getNextPageParam({ toolkits: [] }, [], "c1", [])
    ).toBeUndefined()
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
  it("welcomes an active connection by name", () => {
    expect(
      connectReturnNotice(
        { connectionId: "apc_1", status: "active" },
        { name: "GitHub" }
      )
    ).toEqual({
      text: "GitHub is connected and ready to use.",
      tone: "success",
    })
  })

  it("points a connection still pending at cancelling, since nothing checks it again", () => {
    const notice = connectReturnNotice(
      { connectionId: "apc_1", status: "pending" },
      { name: "Gmail" }
    )
    expect(notice.tone).toBe("info")
    expect(notice.text).toBe(
      "Gmail is still connecting. If it doesn't finish, cancel it and connect again."
    )
  })

  it("says why a connection failed when the connection itself says", () => {
    expect(
      connectReturnNotice(
        { connectionId: "apc_1", status: "failed" },
        { name: "Gmail", reason: "link expired" }
      )
    ).toEqual({
      text: "Gmail didn't connect: the link expired before it was finished. Try again.",
      tone: "danger",
    })
  })

  it("flags anything else as not finished, naming nothing it doesn't know", () => {
    expect(
      connectReturnNotice({ connectionId: "apc_1", status: "removed" })
    ).toEqual({ text: "The app didn't connect. Try again.", tone: "danger" })
  })
})

describe("connectionReasonText", () => {
  it("rewords the reasons apps writes itself", () => {
    expect(connectionReasonText("the member did not finish connecting")).toBe(
      "it wasn't finished at the app"
    )
    expect(connectionReasonText("link expired")).toBe(
      "the link expired before it was finished"
    )
  })

  it("rewords Composio's own account statuses", () => {
    expect(connectionReasonText("failed")).toBe("the app reported a failure")
    expect(connectionReasonText("expired")).toBe("the sign-in expired")
    expect(connectionReasonText("revoked")).toBe("access was revoked")
    expect(connectionReasonText("inactive")).toBe("the account is inactive")
  })

  it("passes anything else through, without its closing stop", () => {
    expect(connectionReasonText(" Refresh token revoked. ")).toBe(
      "Refresh token revoked"
    )
  })
})

describe("replacedConnections", () => {
  it("finds failed and expired rows an active connection made later has replaced", () => {
    const stale = connection({
      id: "apc_old",
      status: "expired",
      created_at: "2026-09-01T00:00:00Z",
    })
    const failed = connection({
      id: "apc_failed",
      status: "failed",
      created_at: "2026-09-10T00:00:00.5Z",
    })
    const current = connection({
      id: "apc_new",
      status: "active",
      created_at: "2026-09-10T00:00:00.75Z",
    })
    expect(
      replacedConnections([current, failed, stale]).map((item) => item.id)
    ).toEqual(["apc_failed", "apc_old"])
  })

  it("leaves stale rows alone while the new connection is still pending, or for another app", () => {
    expect(
      replacedConnections([
        connection({
          id: "apc_new",
          status: "pending",
          created_at: "2026-09-20T00:00:00Z",
        }),
        connection({
          id: "apc_old",
          status: "expired",
          created_at: "2026-09-01T00:00:00Z",
        }),
        connection({
          id: "apc_gmail",
          toolkit: "gmail",
          status: "active",
          created_at: "2026-09-21T00:00:00Z",
        }),
      ])
    ).toEqual([])
  })

  it("compares instants, not strings: Go writes microseconds and sometimes a local offset", () => {
    // 07:14:14+05:30 is 01:44:14Z, before the failed row's 01:50Z, although
    // it sorts after it as text.
    expect(
      replacedConnections([
        connection({
          id: "apc_active",
          status: "active",
          created_at: "2026-09-24T07:14:14.114319+05:30",
        }),
        connection({
          id: "apc_failed",
          status: "failed",
          created_at: "2026-09-24T01:50:00.000001Z",
        }),
      ])
    ).toEqual([])
    expect(
      replacedConnections([
        connection({
          id: "apc_active",
          status: "active",
          created_at: "2026-09-24T01:43:53.111766Z",
        }),
        connection({
          id: "apc_expired",
          status: "expired",
          created_at: "2026-09-24T06:59:00.5+05:30",
        }),
      ]).map((item) => item.id)
    ).toEqual(["apc_expired"])
  })

  it("never calls an active connection replaced, nor a stale row newer than the active one", () => {
    expect(
      replacedConnections([
        connection({
          id: "apc_newer_failure",
          status: "failed",
          created_at: "2026-09-22T00:00:00Z",
        }),
        connection({
          id: "apc_active",
          status: "active",
          created_at: "2026-09-21T00:00:00Z",
        }),
      ])
    ).toEqual([])
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

// ---------------------------------------------------------------------------
// Oryxa's own sign-in
// ---------------------------------------------------------------------------

describe("connect: Oryxa's own sign-in or Composio", () => {
  const githubManifest = {
    toolkit: "github",
    name: "GitHub",
    version: 1,
    methods: [
      { kind: "oauth2_code", client: "github", scopes: ["repo"], fields: [] },
    ],
    redirect_uri: "https://app.example.test/v1/apps/oauth/callback",
  }

  function routes(
    answers: Record<string, () => Response>
  ): ReturnType<typeof vi.fn> {
    const fetchMock = vi.fn(async (input: string, init?: RequestInit) => {
      const route = `${init?.method ?? "GET"} ${input}`
      return route in answers
        ? answers[route]()
        : errorResponse("not_found", "resource not found", 404)
    })
    vi.stubGlobal("fetch", fetchMock)
    return fetchMock
  }

  function postBody(fetchMock: ReturnType<typeof vi.fn>) {
    const call = fetchMock.mock.calls.find(
      ([, init]) => (init as RequestInit | undefined)?.method === "POST"
    )
    return JSON.parse(String((call?.[1] as RequestInit).body)) as unknown
  }

  it("sends a toolkit Oryxa's sign-in doesn't list through Composio, exactly as before", async () => {
    const fetchMock = routes({
      "GET /v1/apps/integrations": () =>
        jsonResponse({
          integrations: [
            { toolkit: "github", name: "GitHub", kinds: ["oauth2_code"] },
          ],
        }),
      "POST /v1/organizations/org_1/apps/connections": () =>
        jsonResponse(
          {
            connection: connection({ toolkit: "gmail", status: "pending" }),
            redirect_url: "https://connect.composio.dev/link/lk_1",
          },
          201
        ),
    })

    const result = await connect("org_1", {
      returnTo: "https://app.example.test/?view=tools",
      toolkit: "gmail",
    })

    expect(result.redirect_url).toBe("https://connect.composio.dev/link/lk_1")
    expect(postBody(fetchMock)).toEqual({
      return_to: "https://app.example.test/?view=tools",
      toolkit: "gmail",
    })
  })

  it("keeps Composio for an apps that predates Oryxa's sign-in (404)", async () => {
    const fetchMock = routes({
      "POST /v1/organizations/org_1/apps/connections": () =>
        jsonResponse(
          {
            connection: connection({ status: "pending" }),
            redirect_url: "https://connect.composio.dev/link/lk_2",
          },
          201
        ),
    })
    await connect("org_1", {
      returnTo: "https://app.example.test/",
      toolkit: "github",
    })
    expect(postBody(fetchMock)).toEqual({
      return_to: "https://app.example.test/",
      toolkit: "github",
    })
  })

  it("refuses to guess the broker when the list of Oryxa's apps fails", async () => {
    const fetchMock = routes({
      "GET /v1/apps/integrations": () =>
        errorResponse("unavailable", "did not answer", 503),
    })
    await expect(
      connect("org_1", {
        returnTo: "https://app.example.test/",
        toolkit: "github",
      })
    ).rejects.toMatchObject({ status: 503 })
    expect(
      fetchMock.mock.calls.some(
        ([, init]) => (init as RequestInit | undefined)?.method === "POST"
      )
    ).toBe(false)
  })

  it("starts an OAuth sign-in through Oryxa with a same-origin, credentialed POST and answers the app's page", async () => {
    const fetchMock = routes({
      "GET /v1/apps/integrations": () =>
        jsonResponse({
          integrations: [
            { toolkit: "github", name: "GitHub", kinds: ["oauth2_code"] },
          ],
        }),
      "GET /v1/apps/integrations/github/manifest": () =>
        jsonResponse(githubManifest),
      "POST /v1/organizations/org_1/apps/connections": () =>
        jsonResponse(
          {
            connection: connection({
              broker: "oryxa",
              status: "pending",
            }),
            redirect_url: "https://github.com/login/oauth/authorize?state=s",
          },
          201
        ),
    })

    const result = await connect("org_1", {
      returnTo: "https://app.example.test/?view=tools",
      toolkit: "github",
    })

    expect(result.redirect_url).toBe(
      "https://github.com/login/oauth/authorize?state=s"
    )
    expect(postBody(fetchMock)).toEqual({
      broker: "oryxa",
      kind: "oauth2_code",
      return_to: "https://app.example.test/?view=tools",
      toolkit: "github",
    })
    expect(fetchMock).toHaveBeenCalledWith(
      "/v1/organizations/org_1/apps/connections",
      expect.objectContaining({ credentials: "include", method: "POST" })
    )
  })

  it("connects with a key and replaces it, keeping the field an invalid_credentials answer blames", async () => {
    const fetchMock = routes({
      "POST /v1/organizations/org_1/apps/connections": () =>
        jsonResponse(
          {
            connection: connection({
              broker: "oryxa",
              credential_kind: "api_token",
            }),
          },
          201
        ),
      "PUT /v1/organizations/org_1/apps/connections/apc_1/credentials": () =>
        jsonResponse(
          {
            error: {
              code: "invalid_credentials",
              message: "token was refused",
              field: "token",
            },
          },
          400
        ),
    })

    const started = await connectOryxa("org_1", {
      fields: { token: "ghp_example" },
      kind: "api_token",
      returnTo: "https://app.example.test/?view=tools",
      toolkit: "github",
    })
    expect(started.redirect_url).toBeUndefined()
    expect(nextStep(started, "https://app.example.test/?view=tools")).toEqual({
      connection: started.connection,
      redirect_url:
        "https://app.example.test/?view=tools&app_connection=apc_1&status=active",
    })

    const failure = await replaceCredentials("org_1", "apc_1", {
      fields: { token: "ghp_other" },
      kind: "api_token",
    }).catch((error: unknown) => error)
    expect(failure).toBeInstanceOf(AppsFieldError)
    expect(failure).toMatchObject({
      code: "invalid_credentials",
      field: "token",
      status: 400,
    })
    const put = fetchMock.mock.calls.find(
      ([, init]) => (init as RequestInit | undefined)?.method === "PUT"
    )
    expect(JSON.parse(String((put?.[1] as RequestInit).body))).toEqual({
      fields: { token: "ghp_other" },
      kind: "api_token",
    })
  })
})

describe("organizations' own OAuth apps", () => {
  it("saves only what was typed: no empty secret, fields or webhook secrets", async () => {
    const fetchMock = vi.fn(async () =>
      jsonResponse({ client: "github", saved: true })
    )
    vi.stubGlobal("fetch", fetchMock)

    await saveOAuthClient("org_1", "github", {
      clientId: "Iv1.abc",
      fields: {},
      refuseDefault: false,
      webhookSecrets: {},
    })
    await saveOAuthClient("org_1", "slack", {
      clientId: "123.456",
      clientSecret: "s3cret-value-0000",
      refuseDefault: true,
      webhookSecrets: { slack: "signing-secret-0000" },
    })

    expect(fetchMock).toHaveBeenNthCalledWith(
      1,
      "/v1/organizations/org_1/apps/oauth-clients/github",
      expect.objectContaining({
        method: "PUT",
        body: JSON.stringify({ client_id: "Iv1.abc", refuse_default: false }),
      })
    )
    expect(fetchMock).toHaveBeenNthCalledWith(
      2,
      "/v1/organizations/org_1/apps/oauth-clients/slack",
      expect.objectContaining({
        body: JSON.stringify({
          client_id: "123.456",
          client_secret: "s3cret-value-0000",
          refuse_default: true,
          webhook_secrets: { slack: "signing-secret-0000" },
        }),
      })
    )
  })

  it("reads, removes, and reveals a verification token once (409 after)", async () => {
    let revealed = false
    const fetchMock = vi.fn(async (input: string, init?: RequestInit) => {
      if (init?.method === "DELETE") return new Response(null, { status: 204 })
      if (input.endsWith("/verification-token")) {
        if (revealed) return errorResponse("conflict", "already revealed", 409)
        revealed = true
        return jsonResponse({ toolkit: "notion", verification_token: "tok_1" })
      }
      return jsonResponse({ client: "notion", saved: false })
    })
    vi.stubGlobal("fetch", fetchMock)

    await expect(getOAuthClient("org_1", "notion")).resolves.toMatchObject({
      client: "notion",
    })
    await removeOAuthClient("org_1", "notion")
    await expect(revealVerificationToken("org_1", "notion")).resolves.toBe(
      "tok_1"
    )
    await expect(
      revealVerificationToken("org_1", "notion")
    ).rejects.toMatchObject({ status: 409 })
    expect(fetchMock).toHaveBeenCalledWith(
      "/v1/organizations/org_1/apps/webhooks/notion/verification-token",
      expect.objectContaining({ method: "POST" })
    )
    expect(fetchMock).toHaveBeenCalledWith(
      "/v1/organizations/org_1/apps/oauth-clients/notion",
      expect.objectContaining({ method: "DELETE" })
    )
  })
})

describe("what can be connected and replaced", () => {
  it("connects straight away only through one OAuth way in with nothing to type", () => {
    const oauth = { kind: "oauth2_code", fields: [] }
    const token = {
      kind: "api_token",
      fields: [
        {
          name: "token",
          label: "Token",
          secret: true,
          level: "connection",
          standard: true,
          validation: { required: true, max_length: 4096 },
        },
      ],
    }
    const manifest = { toolkit: "github", name: "GitHub", version: 1 }
    expect(directSignin({ ...manifest, methods: [oauth] })).toBe(oauth)
    expect(
      directSignin({ ...manifest, methods: [oauth, token] })
    ).toBeUndefined()
    expect(directSignin({ ...manifest, methods: [token] })).toBeUndefined()
    expect(
      directSignin({
        ...manifest,
        methods: [
          {
            kind: "oauth2_code",
            fields: [
              {
                name: "subdomain",
                label: "Subdomain",
                secret: false,
                level: "connection",
                validation: { required: true, pattern: "[a-z]+" },
              },
            ],
          },
        ],
      })
    ).toBeUndefined()
  })

  it("replaces a key, a password or client credentials of an active Oryxa connection, never OAuth or Composio's", () => {
    const oryxa = connection({ broker: "oryxa", credential_kind: "api_token" })
    expect(canReplaceCredential(oryxa)).toBe(true)
    expect(canReplaceCredential({ ...oryxa, credential_kind: "basic" })).toBe(
      true
    )
    expect(
      canReplaceCredential({ ...oryxa, credential_kind: "oauth2_code" })
    ).toBe(false)
    expect(canReplaceCredential({ ...oryxa, status: "expired" })).toBe(false)
    expect(canReplaceCredential(connection())).toBe(false)
    expect(isOryxaConnection(connection())).toBe(false)
  })

  it("names how an Oryxa connection signs in", () => {
    expect(
      signinDescription({ credential_kind: "oauth2_code" }, "GitHub")
    ).toBe("Signed in with GitHub through Oryxa's own sign-in.")
    expect(signinDescription({ credential_kind: "api_token" }, "GitHub")).toBe(
      "Connected with an API token through Oryxa's own sign-in."
    )
    expect(signinMethodLabel("oauth2_code", "GitHub")).toBe(
      "Sign in with GitHub"
    )
    expect(signinMethodLabel("basic", "Jira")).toBe("Username and password")
  })

  it("tells a member they closed the sign-in, in words", () => {
    expect(
      appsErrorMessage(new ConnectCancelledError("GitHub"), "fallback")
    ).toBe("You closed GitHub's sign-in, so it isn't connected.")
  })
})

describe("checking what was typed", () => {
  const field = (validation: Record<string, unknown>) => ({
    label: "Subdomain",
    validation: { required: false, ...validation },
  })
  const secret = "ghp_s3cretValue"

  it("applies the manifest's rules as apps does, never quoting the value", () => {
    expect(fieldProblem(field({ required: true }), "  ")).toBe(
      "Subdomain is required."
    )
    expect(fieldProblem(field({}), "")).toBeUndefined()
    expect(fieldProblem(field({ max_length: 4 }), "abcde")).toBe(
      "Subdomain must be at most 4 characters."
    )
    expect(fieldProblem(field({ enum: ["us", "eu"] }), "ap")).toBe(
      "Subdomain must be one of us, eu."
    )
    // The pattern is anchored at both ends, as apps anchors RE2.
    expect(
      fieldProblem(field({ pattern: "[a-z0-9][a-z0-9-]{0,62}" }), "acme")
    ).toBeUndefined()
    expect(
      fieldProblem(field({ pattern: "[a-z0-9][a-z0-9-]{0,62}" }), "acme.test")
    ).toBe("Subdomain isn't in the form this app expects.")
    expect(fieldProblem(field({}), "two\nlines")).toBe(
      "Subdomain must be one line of text."
    )
    for (const [format, bad, good] of [
      ["hostname", "localhost", "acme.zendesk.com"],
      ["https_url", "https://acme.test/?q=1", "https://acme.test/api"],
      ["uuid", "not-a-uuid", "123e4567-e89b-12d3-a456-426614174000"],
      ["email", "@acme.test", "ops@acme.test"],
    ]) {
      expect(fieldProblem(field({ format }), bad)).toMatch(
        /^Subdomain must be /
      )
      expect(fieldProblem(field({ format }), good)).toBeUndefined()
    }
    expect(fieldProblem(field({ max_length: 3 }), secret)).not.toContain(secret)
  })

  it("checks an OAuth client ID and a webhook signing secret as apps does", () => {
    expect(clientIdProblem("Iv1.abc")).toBeUndefined()
    expect(clientIdProblem("two words")).toBe(
      "Client ID must be one word of at most 512 characters."
    )
    expect(webhookSecretProblem("Slack signing secret", "short")).toBe(
      "Slack signing secret must be 16 to 1024 characters."
    )
    expect(
      webhookSecretProblem("Slack signing secret", "sixteen chars ok")
    ).toBe("Slack signing secret must be printable characters without spaces.")
    expect(
      webhookSecretProblem("Slack signing secret", "0123456789abcdef")
    ).toBeUndefined()
  })

  it("leads apps' refusal with the field's label", () => {
    expect(
      refusedFieldText("token was refused by the app", "token", "Token")
    ).toBe("Token was refused by the app.")
    expect(
      refusedFieldText(
        "webhook_secrets.slack must be 16 to 1024 characters",
        "webhook_secrets.slack",
        "Slack signing secret"
      )
    ).toBe("Slack signing secret must be 16 to 1024 characters.")
    expect(refusedFieldText("something else", "token", "Token")).toBe(
      "Token wasn't accepted."
    )
  })

  it("builds the return trip a key's connect sends the browser on", () => {
    expect(
      connectReturnUrl("https://app.example.test/?org=o&view=tools", {
        id: "apc_9",
        status: "active",
      })
    ).toBe(
      "https://app.example.test/?org=o&view=tools&app_connection=apc_9&status=active"
    )
  })
})

describe("Oryxa query options", () => {
  it("keys integrations and manifests apart from any organization, and OAuth clients under one", () => {
    expect(oryxaIntegrationsQueryOptions().queryKey).toEqual([
      "apps",
      "oryxa",
      "integrations",
    ])
    expect(signinManifestQueryOptions("github").queryKey).toEqual([
      "apps",
      "oryxa",
      "manifest",
      "github",
    ])
    expect(oauthClientQueryOptions("org_1", "google").queryKey).toEqual([
      "apps",
      "org_1",
      "oauth-clients",
      "google",
    ])
  })

  it("reads an empty list while Oryxa's sign-in is off", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async () => jsonResponse({ integrations: [] }))
    )
    await expect(listOryxaIntegrations()).resolves.toEqual([])
    vi.stubGlobal(
      "fetch",
      vi.fn(async () => jsonResponse({}))
    )
    await expect(listOryxaIntegrations()).resolves.toEqual([])
  })
})
