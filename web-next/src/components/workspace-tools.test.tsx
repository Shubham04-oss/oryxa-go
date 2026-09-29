// @vitest-environment jsdom

import { afterEach, describe, expect, it, vi } from "vitest"
import {
  cleanup,
  fireEvent,
  render as renderBase,
  screen,
  waitFor,
  within,
} from "@testing-library/react"
import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import type { ReactElement } from "react"
import type { Connection } from "@/lib/apps/client"
import type { Source } from "@/lib/knowledge/client"
import { WorkspaceTools } from "./workspace-tools"
import { previewToolkits } from "./workspace-tools-data"

const org = "/v1/organizations/org-1"

function render(ui: ReactElement) {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  })
  return renderBase(
    <QueryClientProvider client={client}>{ui}</QueryClientProvider>
  )
}

function json(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  })
}

function noContent() {
  return new Response(null, { status: 204 })
}

function apiError(code: string, message: string, status: number) {
  return json({ error: { code, message } }, status)
}

function connection(overrides: Partial<Connection> = {}): Connection {
  return {
    id: "apc_1",
    toolkit: "github",
    status: "active",
    member_id: "user-1",
    auto_subscribe: true,
    created_at: "2026-09-20T10:00:00Z",
    updated_at: "2026-09-20T10:00:00Z",
    ...overrides,
  }
}

function source(overrides: Partial<Source> = {}): Source {
  return {
    id: "source-1",
    provider: "github",
    connection_id: "apc_1",
    target: { repository: "oryxa/app" },
    scope: "member",
    member_id: "user-1",
    created_by: "user-1",
    status: "active",
    interval_seconds: 3600,
    created_at: "2026-09-20T10:00:00Z",
    updated_at: "2026-09-20T10:00:00Z",
    ...overrides,
  }
}

type FakeRequest = {
  method: string
  path: string
  params: URLSearchParams
  body: unknown
}
type Handler = (request: FakeRequest) => Response | undefined

/** Answers each request with the first handler that knows it; 404s the rest. */
function fakeFetch(...handlers: Handler[]) {
  const requests: FakeRequest[] = []
  const fetchMock = vi.fn(
    async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = new URL(String(input), "http://localhost:3000")
      const request: FakeRequest = {
        method: init?.method ?? "GET",
        path: url.pathname,
        params: url.searchParams,
        body:
          typeof init?.body === "string" ? JSON.parse(init.body) : undefined,
      }
      requests.push(request)
      for (const handler of handlers) {
        const response = handler(request)
        if (response) return response
      }
      if (request.path === `${org}/apps/offer`)
        return json({ offer: null, auto_subscribe_default: true })
      return apiError("not_found", "resource not found", 404)
    }
  )
  vi.stubGlobal("fetch", fetchMock)
  return { fetchMock, requests }
}

/** apps' catalog: the probe, a toolkit by slug (connectable), and search. */
function catalog(): Handler {
  return ({ method, path, params }) => {
    if (method !== "GET") return undefined
    if (path === `${org}/apps/offer`)
      return json({ offer: null, auto_subscribe_default: true })
    if (path === "/v1/apps/catalog") {
      if (params.get("search") === "slack")
        return json({
          toolkits: [
            {
              toolkit: "slack",
              name: "Slack",
              description: "Team messages",
              categories: [{ id: "messaging", name: "Messaging" }],
              auth_schemes: ["OAUTH2"],
              connectable: true,
              tools_count: 42,
              triggers_count: 3,
            },
          ],
          next_cursor: null,
        })
      return json({ toolkits: [previewToolkits[0]], next_cursor: "c2" })
    }
    const slug = /^\/v1\/apps\/catalog\/([^/]+)$/.exec(path)?.[1]
    if (!slug) return undefined
    const toolkit = previewToolkits.find((item) => item.toolkit === slug)
    return toolkit
      ? json({ ...toolkit, connectable: true })
      : apiError("not_found", "resource not found", 404)
  }
}

/** apps' connections, backed by `state` the way the store behaves. */
function connections(state: { connections: Connection[] }): Handler {
  return ({ method, path }) => {
    if (path === `${org}/apps/connections` && method === "GET")
      return json({
        connections: state.connections.filter(
          (item) => item.status !== "removed"
        ),
      })
    const id = /^\/v1\/organizations\/org-1\/apps\/connections\/([^/]+)$/.exec(
      path
    )?.[1]
    if (!id) return undefined
    const found = state.connections.find((item) => item.id === id)
    if (method === "GET")
      return found
        ? json(found)
        : apiError("not_found", "resource not found", 404)
    if (method === "DELETE") {
      state.connections = state.connections.map((item) =>
        item.id === id ? { ...item, status: "removed" } : item
      )
      return noContent()
    }
    return undefined
  }
}

/** Nothing subscribed or indexed yet, for tests about something else. */
function quietDetails(): Handler {
  return ({ method, path }) => {
    if (method !== "GET") return undefined
    if (/^\/v1\/apps\/catalog\/[^/]+\/triggers$/.test(path))
      return json({ triggers: [], next_cursor: null })
    if (path === `${org}/apps/triggers`) return json({ triggers: [] })
    if (path === "/v1/sources/providers")
      return json({ providers: ["github", "gmail"] })
    if (path === `${org}/sources`) return json({ sources: [] })
    return undefined
  }
}

function stubLocation(href: string) {
  const url = new URL(href)
  const assign = vi.fn()
  vi.stubGlobal("location", {
    assign,
    hash: url.hash,
    href: url.href,
    origin: url.origin,
    pathname: url.pathname,
    search: url.search,
  })
  return assign
}

afterEach(() => {
  cleanup()
  vi.unstubAllGlobals()
  window.history.replaceState(null, "", "/")
})

describe("WorkspaceTools", () => {
  it("labels offline examples and lets the logo strip expand without implying a live connection", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async () => json({ error: { message: "offline" } }, 502))
    )
    render(<WorkspaceTools />)

    expect(await screen.findByText(/Layout preview/)).toBeTruthy()
    expect(
      screen.getByRole("heading", { name: "Example connections" })
    ).toBeTruthy()
    expect(screen.getByText("4 examples")).toBeTruthy()
    expect(screen.getByRole("heading", { name: "Gmail" })).toBeTruthy()
    expect(screen.queryByRole("button", { name: "Connect Gmail" })).toBeNull()
    expect(
      screen.queryByText("Keep email within reach for the work that matters.")
    ).toBeNull()
    fireEvent.click(screen.getByRole("button", { name: "Gmail details" }))
    expect(
      screen
        .getByRole("button", { name: "Gmail details" })
        .closest(".workspace-tool-card")
        ?.classList.contains("is-expanded")
    ).toBe(true)
    expect(
      screen.getByText("Keep email within reach for the work that matters.")
    ).toBeTruthy()
    fireEvent.click(screen.getByRole("button", { name: "Gmail details" }))
    expect(
      screen.queryByText("Keep email within reach for the work that matters.")
    ).toBeNull()

    fireEvent.click(
      screen.getByRole("button", { name: "Expand GitHub connection" })
    )
    expect(
      screen
        .getByRole("button", { name: "Expand GitHub connection" })
        .classList.contains("is-selected")
    ).toBe(true)
    expect(
      screen.getByText("Example connection · no account linked")
    ).toBeTruthy()
    // An example is never offered a real action.
    expect(screen.queryByRole("button", { name: "Disconnect" })).toBeNull()
    expect(screen.getByRole("tab", { name: "Events" })).toBeTruthy()
    expect(screen.getByText("New item")).toBeTruthy()
    fireEvent.click(screen.getByRole("tab", { name: "Indexing" }))
    expect(screen.getByText("Example source")).toBeTruthy()
    fireEvent.click(screen.getByRole("tab", { name: "Runs" }))
    expect(screen.getByText("Example run")).toBeTruthy()

    fireEvent.change(screen.getByRole("searchbox", { name: "Search tools" }), {
      target: { value: "zoom" },
    })
    expect(screen.getByRole("heading", { name: "Zoom" })).toBeTruthy()
    expect(screen.queryByRole("heading", { name: "Gmail" })).toBeNull()
  })

  it("filters the apps on screen for a query too short for Composio's search, and searches the catalog from three characters", async () => {
    const state = { connections: [connection()] }
    const { requests } = fakeFetch(
      catalog(),
      connections(state),
      quietDetails()
    )
    render(<WorkspaceTools organizationId="org-1" />)
    expect(await screen.findByRole("heading", { name: "Zoom" })).toBeTruthy()
    const searchbox = screen.getByRole("searchbox", { name: "Search tools" })
    const searched = (term: string) =>
      requests.some(
        (request) =>
          request.path === "/v1/apps/catalog" &&
          request.params.get("search") === term
      )

    fireEvent.change(searchbox, { target: { value: "zo" } })
    expect(screen.getByRole("heading", { name: "Zoom" })).toBeTruthy()
    expect(screen.queryByRole("heading", { name: "Gmail" })).toBeNull()
    fireEvent.change(searchbox, { target: { value: "qx" } })
    expect(screen.getByText("Keep typing to search every app.")).toBeTruthy()
    // Past the debounce, a short query has still asked Composio nothing.
    await new Promise((resolve) => setTimeout(resolve, 400))
    expect(searched("zo") || searched("qx")).toBe(false)
    expect(screen.queryByText(/Search could not be loaded/)).toBeNull()

    fireEvent.change(searchbox, { target: { value: "slack" } })
    expect(
      await screen.findByRole("heading", { name: "Messaging" })
    ).toBeTruthy()
    expect(searched("slack")).toBe(true)
  })

  it("shows only offered apps and keeps restricted connections removable", async () => {
    const state = { connections: [connection()] }
    fakeFetch(
      ({ path }) =>
        path === `${org}/apps/offer`
          ? json({ offer: ["zoom"], auto_subscribe_default: false })
          : undefined,
      catalog(),
      connections(state),
      quietDetails()
    )
    render(<WorkspaceTools organizationId="org-1" />)
    expect(await screen.findByRole("heading", { name: "Zoom" })).toBeTruthy()
    expect(screen.queryByRole("heading", { name: "Gmail" })).toBeNull()
    fireEvent.click(
      screen.getByRole("button", { name: /Expand GitHub connection/ })
    )
    expect(screen.getByText(/not offered by your workspace/)).toBeTruthy()
    expect(screen.queryByRole("tab", { name: "Events" })).toBeNull()
    expect(screen.getByRole("button", { name: "Disconnect" })).toBeTruthy()
  })

  it("keeps the catalog visible when employee connections are disabled", async () => {
    fakeFetch(
      ({ path }) =>
        path === `${org}/apps/offer`
          ? json({
              offer: ["gmail"],
              employee_connections: false,
              can_connect: false,
              auto_subscribe_default: true,
            })
          : undefined,
      catalog(),
      connections({ connections: [] }),
      quietDetails()
    )
    render(<WorkspaceTools organizationId="org-1" />)
    expect(await screen.findByRole("heading", { name: "Gmail" })).toBeTruthy()
    expect(screen.getByText(/turned off employee app connections/)).toBeTruthy()
    fireEvent.click(screen.getByRole("button", { name: "Gmail details" }))
    expect(screen.queryByRole("button", { name: "Connect Gmail" })).toBeNull()
  })

  it("stays live and says so when apps answers but the catalog is down", async () => {
    const state = { connections: [connection()] }
    fakeFetch(
      ({ path }) =>
        path === "/v1/apps/catalog"
          ? apiError(
              "unavailable",
              "Composio or a service apps depends on did not answer; retry",
              503
            )
          : undefined,
      connections(state),
      quietDetails()
    )
    render(<WorkspaceTools organizationId="org-1" />)

    expect(
      await screen.findByText(
        /The app catalog or Composio did not answer. Try again shortly./
      )
    ).toBeTruthy()
    expect(screen.queryByText(/Layout preview/)).toBeNull()
    // The member's own connection is still shown, named from its slug.
    expect(
      await screen.findByRole("button", { name: "Expand Github connection" })
    ).toBeTruthy()
    expect(screen.getByText("1 connected")).toBeTruthy()
  })

  it("reads connections, events, indexing, runs and search through the shared clients", async () => {
    const state = {
      connections: [connection({ id: "connection-1", label: "github-acme" })],
    }
    let subscribed = false
    let indexed = false
    const { fetchMock, requests } = fakeFetch(
      catalog(),
      connections(state),
      ({ method, path, params }) => {
        if (path === "/v1/apps/catalog/github/triggers" && method === "GET")
          return json({
            triggers: [
              {
                trigger: "GITHUB_PUSH_EVENT",
                name: "Push",
                description: "A repository receives a push.",
                toolkit: "github",
                kind: "webhook",
                config_schema: {
                  properties: { repo: { type: "string", title: "Repository" } },
                  required: ["repo"],
                },
              },
            ],
            next_cursor: null,
          })
        if (
          path === `${org}/apps/triggers` &&
          method === "GET" &&
          params.get("connection_id") === "connection-1"
        )
          return json({
            triggers: subscribed
              ? [
                  {
                    id: "trigger-1",
                    connection_id: "connection-1",
                    toolkit: "github",
                    trigger: "GITHUB_PUSH_EVENT",
                    config: { repo: "oryxa/app" },
                    status: "active",
                    member_id: "user-1",
                    delivery_count: 0,
                    created_at: "2026-09-23T10:00:00Z",
                    updated_at: "2026-09-23T10:00:00Z",
                  },
                ]
              : [],
          })
        if (path === `${org}/apps/triggers` && method === "POST") {
          subscribed = true
          return json({ id: "trigger-1", status: "active" }, 201)
        }
        if (path === "/v1/sources/providers")
          return json({ providers: ["github"] })
        if (path === `${org}/sources` && method === "GET")
          return json({
            sources: indexed ? [source({ connection_id: "connection-1" })] : [],
          })
        if (path === `${org}/apps/connections/connection-1/targets`)
          return json({
            targets: [
              {
                kind: "repository",
                id: "repo-1",
                name: "oryxa/app",
                target: { repository: "oryxa/app" },
              },
            ],
          })
        if (path === `${org}/sources` && method === "POST") {
          indexed = true
          return json(source({ connection_id: "connection-1" }), 201)
        }
        if (path === `${org}/sources/source-1/runs`)
          return json({
            runs: [
              {
                id: "run-1",
                organization_id: "org-1",
                source_id: "source-1",
                kind: "incremental",
                status: "completed",
                started_at: "2026-09-23T10:00:00Z",
                documents_seen: 12,
                documents_published: 8,
                documents_removed: 1,
                documents_excluded: 3,
              },
            ],
          })
        return undefined
      }
    )
    render(<WorkspaceTools organizationId="org-1" />)

    expect(
      await screen.findByRole("heading", { name: "Connected tools" })
    ).toBeTruthy()
    expect(
      await screen.findByRole("button", { name: "Expand GitHub connection" })
    ).toBeTruthy()
    expect(screen.getByText("1 connected")).toBeTruthy()
    expect(screen.queryByText(/Layout preview/)).toBeNull()
    // A connected app leaves the catalog; the rest can be connected.
    await waitFor(() =>
      expect(screen.getByRole("button", { name: "Gmail details" })).toBeTruthy()
    )
    expect(screen.queryByRole("button", { name: "GitHub details" })).toBeNull()
    expect(screen.queryByRole("button", { name: "Connect Gmail" })).toBeNull()
    fireEvent.click(screen.getByRole("button", { name: "Gmail details" }))
    expect(
      screen
        .getByRole("button", { name: "Connect Gmail" })
        .hasAttribute("disabled")
    ).toBe(false)
    fireEvent.click(
      screen.getByRole("button", { name: "Expand GitHub connection" })
    )
    expect(screen.getByText("github-acme")).toBeTruthy()

    // "Add event" waits for the app's events to load before it opens.
    expect(
      await screen.findByText(
        "No events watched yet. Choose an event below to start."
      )
    ).toBeTruthy()
    fireEvent.click(screen.getByRole("button", { name: "Add event" }))
    fireEvent.click(await screen.findByRole("button", { name: /^Push/ }))
    fireEvent.change(screen.getByRole("textbox", { name: "Repository *" }), {
      target: { value: "oryxa/app" },
    })
    fireEvent.click(screen.getByRole("button", { name: "Watch event" }))
    await waitFor(() => expect(subscribed).toBe(true))
    expect(
      requests.find(
        (request) =>
          request.method === "POST" && request.path === `${org}/apps/triggers`
      )?.body
    ).toEqual({
      connection_id: "connection-1",
      trigger: "GITHUB_PUSH_EVENT",
      config: { repo: "oryxa/app" },
    })
    // Wait for the "add event" form (its own heading also reads "Push") to
    // close before looking for the subscribed trigger's row.
    await waitFor(() =>
      expect(screen.queryByRole("button", { name: "Watch event" })).toBeNull()
    )
    const activeDot = (await screen.findByText("Push"))
      .closest(".workspace-tool-row")
      ?.querySelector(".workspace-tool-dot")
    expect(activeDot?.classList.contains("is-live")).toBe(true)

    fireEvent.click(screen.getByRole("tab", { name: "Indexing" }))
    expect(
      await screen.findByText(
        "No sources registered yet. Add one to make its content searchable."
      )
    ).toBeTruthy()
    fireEvent.click(screen.getByRole("button", { name: "Add source" }))
    fireEvent.change(
      await screen.findByRole("combobox", { name: "Repository *" }),
      { target: { value: "repo-1" } }
    )
    fireEvent.click(screen.getByRole("button", { name: "Register source" }))
    await waitFor(() => expect(indexed).toBe(true))
    expect(fetchMock).toHaveBeenCalledWith(
      `${org}/sources`,
      expect.objectContaining({
        method: "POST",
        credentials: "include",
        body: JSON.stringify({
          connection_id: "connection-1",
          provider: "github",
          target: { repository: "oryxa/app" },
          interval_seconds: 3600,
        }),
      })
    )
    fireEvent.click(await screen.findByRole("button", { name: "Runs" }))
    expect(
      await screen.findByText("8 published · 1 removed · 12 seen · 3 left out")
    ).toBeTruthy()
    expect(
      requests.some(
        (request) =>
          request.path === `${org}/sources/source-1/runs` &&
          request.params.get("limit") === "10"
      )
    ).toBe(true)

    fireEvent.change(screen.getByRole("searchbox", { name: "Search tools" }), {
      target: { value: "slack" },
    })
    expect(
      await screen.findByRole("heading", { name: "Messaging" })
    ).toBeTruthy()
    expect(
      requests.some(
        (request) =>
          request.path === "/v1/apps/catalog" &&
          request.params.get("search") === "slack" &&
          request.params.get("limit") === "100"
      )
    ).toBe(true)
    fireEvent.click(screen.getByRole("button", { name: "Slack details" }))
    expect(screen.getByRole("button", { name: "Connect Slack" })).toBeTruthy()
  })

  it("asks the member to sign in again when the session has lapsed, and offers nothing to connect", async () => {
    const { requests } = fakeFetch(catalog(), ({ path }) =>
      path === `${org}/apps/connections`
        ? apiError("unauthorized", "authentication failed", 401)
        : undefined
    )
    render(<WorkspaceTools organizationId="org-1" />)

    expect(
      await screen.findByText(
        "Sign in to see your connections and link a tool."
      )
    ).toBeTruthy()
    // The shared client tried one silent refresh before giving up.
    expect(requests.map((request) => request.path)).toContain(
      "/v1/auth/refresh"
    )
    expect(screen.getByText("Signed out")).toBeTruthy()
    expect(
      screen.getByText("Sign in to see your connected tools.")
    ).toBeTruthy()
    fireEvent.click(
      await screen.findByRole("button", { name: "Gmail details" })
    )
    expect(screen.queryByRole("button", { name: "Connect Gmail" })).toBeNull()
  })

  it("uses the organizationId prop, never the first organization from /v1/auth/me", async () => {
    const { requests } = fakeFetch(
      catalog(),
      ({ path }) =>
        path === "/v1/auth/me"
          ? json({
              organizations: [
                { id: "org_a", name: "Org A" },
                { id: "org_b", name: "Org B" },
              ],
            })
          : undefined,
      ({ path }) =>
        path === "/v1/organizations/org_b/apps/connections"
          ? json({ connections: [] })
          : undefined
    )
    render(<WorkspaceTools organizationId="org_b" />)

    await waitFor(() =>
      expect(requests.map((request) => request.path)).toContain(
        "/v1/organizations/org_b/apps/connections"
      )
    )
    expect(requests.map((request) => request.path)).not.toContain(
      "/v1/organizations/org_a/apps/connections"
    )
  })

  it("disconnects only after the member confirms, then offers the app again", async () => {
    const state = {
      connections: [connection({ id: "apc_gh", label: "octo" })],
    }
    const { requests } = fakeFetch(
      catalog(),
      connections(state),
      quietDetails()
    )
    render(<WorkspaceTools organizationId="org-1" />)

    fireEvent.click(
      await screen.findByRole("button", { name: "Expand GitHub connection" })
    )
    expect(screen.getByText("octo")).toBeTruthy()
    fireEvent.click(screen.getByRole("button", { name: "Disconnect" }))
    const confirm = screen.getByRole("group", { name: "Disconnect GitHub" })
    expect(
      within(confirm).getByText(/Your agents lose access to it/)
    ).toBeTruthy()
    const keep = within(confirm).getByRole("button", { name: "Keep connected" })
    expect(document.activeElement).toBe(keep)
    fireEvent.click(keep)
    expect(
      screen.queryByRole("group", { name: "Disconnect GitHub" })
    ).toBeNull()
    expect(document.activeElement).toBe(
      screen.getByRole("button", { name: "Disconnect" })
    )
    expect(requests.some((request) => request.method === "DELETE")).toBe(false)

    fireEvent.click(screen.getByRole("button", { name: "Disconnect" }))
    fireEvent.keyDown(screen.getByRole("button", { name: "Yes, disconnect" }), {
      key: "Escape",
    })
    expect(
      screen.queryByRole("group", { name: "Disconnect GitHub" })
    ).toBeNull()

    fireEvent.click(screen.getByRole("button", { name: "Disconnect" }))
    fireEvent.click(screen.getByRole("button", { name: "Yes, disconnect" }))
    expect(await screen.findByText("GitHub disconnected.")).toBeTruthy()
    expect(requests).toContainEqual(
      expect.objectContaining({
        method: "DELETE",
        path: `${org}/apps/connections/apc_gh`,
      })
    )
    expect(
      screen.queryByRole("button", { name: /Expand GitHub connection/ })
    ).toBeNull()
    expect(
      screen.getByText(
        "No connected tools yet. Choose an app below to get started."
      )
    ).toBeTruthy()
    expect(
      await screen.findByRole("button", { name: "GitHub details" })
    ).toBeTruthy()
  })

  it("shows a pending connection honestly and cancels it so the app can be connected again", async () => {
    const state = {
      connections: [
        connection({ id: "apc_gm", toolkit: "gmail", status: "pending" }),
      ],
    }
    const { requests } = fakeFetch(
      catalog(),
      connections(state),
      quietDetails()
    )
    render(<WorkspaceTools organizationId="org-1" />)

    const item = await screen.findByRole("button", {
      name: "Expand Gmail connection (Connecting…)",
    })
    expect(within(item).getByText("Connecting…")).toBeTruthy()
    expect(screen.getByText("0 connected · 1 connecting")).toBeTruthy()
    await waitFor(() =>
      expect(
        screen.getByRole("button", { name: "Discord details" })
      ).toBeTruthy()
    )
    expect(screen.queryByRole("button", { name: "Gmail details" })).toBeNull()

    fireEvent.click(item)
    expect(
      screen.getByText(/Waiting for the app to confirm the connection/)
    ).toBeTruthy()
    // Nothing to watch or index until it is active.
    expect(screen.queryByRole("tab", { name: "Events" })).toBeNull()
    fireEvent.click(screen.getByRole("button", { name: "Cancel connecting" }))
    expect(
      await screen.findByText(
        "Stopped connecting Gmail. You can connect it again below."
      )
    ).toBeTruthy()
    expect(requests).toContainEqual(
      expect.objectContaining({
        method: "DELETE",
        path: `${org}/apps/connections/apc_gm`,
      })
    )
    fireEvent.click(
      await screen.findByRole("button", { name: "Gmail details" })
    )
    expect(
      screen
        .getByRole("button", { name: "Connect Gmail" })
        .hasAttribute("disabled")
    ).toBe(false)
  })

  it("reconnects an expired connection and keeps the old row until the member removes it", async () => {
    const stale = connection({
      id: "apc_old",
      status: "expired",
      status_reason: "Refresh token revoked",
      created_at: "2026-09-01T00:00:00Z",
    })
    const state = { connections: [stale] }
    const assign = stubLocation("http://localhost:3000/?view=tools")
    const first = fakeFetch(
      catalog(),
      connections(state),
      quietDetails(),
      ({ method, path }) => {
        if (method !== "POST" || path !== `${org}/apps/connections`)
          return undefined
        const created = connection({
          id: "apc_new",
          status: "pending",
          created_at: "2026-09-23T12:00:00Z",
        })
        state.connections = [created, ...state.connections]
        return json(
          {
            connection: created,
            redirect_url: "https://connect.composio.dev/link/lk_1",
          },
          201
        )
      }
    )
    render(<WorkspaceTools organizationId="org-1" />)

    fireEvent.click(
      await screen.findByRole("button", {
        name: "Expand GitHub connection (Expired)",
      })
    )
    expect(screen.getByText("Expired: Refresh token revoked.")).toBeTruthy()
    expect(
      screen.getByText(
        "Reconnecting starts a new connection; this one stays until you remove it."
      )
    ).toBeTruthy()
    expect(screen.getByText("0 connected · 1 needs attention")).toBeTruthy()
    fireEvent.click(screen.getByRole("button", { name: /Reconnect/ }))
    await waitFor(() =>
      expect(assign).toHaveBeenCalledWith(
        "https://connect.composio.dev/link/lk_1"
      )
    )
    expect(screen.getByRole("button", { name: /Opening/ })).toBeTruthy()
    expect(
      first.requests.find(
        (request) =>
          request.method === "POST" &&
          request.path === `${org}/apps/connections`
      )?.body
    ).toEqual({
      return_to: "http://localhost:3000/?view=tools&org=org-1",
      toolkit: "github",
    })
    expect(first.requests.some((request) => request.method === "DELETE")).toBe(
      false
    )

    // Composio sends the browser back; the new connection is active.
    cleanup()
    vi.unstubAllGlobals()
    state.connections = state.connections.map((item) =>
      item.id === "apc_new"
        ? { ...item, status: "active", connected_at: "2026-09-23T12:01:00Z" }
        : item
    )
    window.history.replaceState(
      null,
      "",
      "/?view=tools&org=org-1&app_connection=apc_new&status=active"
    )
    const back = fakeFetch(catalog(), connections(state), quietDetails())
    render(<WorkspaceTools organizationId="org-1" />)

    expect(
      await screen.findByText("GitHub is connected and ready to use.")
    ).toBeTruthy()
    expect(window.location.search).toBe("?view=tools&org=org-1")
    // The returned connection's detail is open.
    expect(
      screen
        .getByRole("button", { name: "Expand GitHub connection" })
        .getAttribute("aria-expanded")
    ).toBe("true")
    expect(await screen.findByRole("tab", { name: "Events" })).toBeTruthy()

    // The expired row stays, as replaced: it still holds what it indexed,
    // and deleting that is the member's call, never the page's.
    const old = await screen.findByRole("button", {
      name: "Expand GitHub connection (Replaced by your new connection)",
    })
    expect(within(old).getByText("Replaced")).toBeTruthy()
    expect(screen.getByText("1 connected · 1 replaced")).toBeTruthy()
    expect(back.requests.some((request) => request.method === "DELETE")).toBe(
      false
    )

    fireEvent.click(old)
    expect(screen.getByText("Replaced by your new connection.")).toBeTruthy()
    expect(
      screen.getByText(
        "It expired: Refresh token revoked. What it indexed stays searchable until you remove it."
      )
    ).toBeTruthy()
    // apps refuses a second connection while the new one is active.
    expect(screen.queryByRole("button", { name: /Reconnect/ })).toBeNull()
    expect(screen.queryByRole("button", { name: "Disconnect" })).toBeNull()

    fireEvent.click(screen.getByRole("button", { name: "Remove" }))
    const confirm = screen.getByRole("group", {
      name: "Remove the old GitHub connection",
    })
    expect(
      within(confirm).getByText(
        "Remove the old GitHub connection? What it indexed is removed from search. Your new connection isn't affected."
      )
    ).toBeTruthy()
    expect(document.activeElement).toBe(
      within(confirm).getByRole("button", { name: "Keep it" })
    )
    expect(back.requests.some((request) => request.method === "DELETE")).toBe(
      false
    )
    fireEvent.click(
      within(confirm).getByRole("button", { name: "Yes, remove" })
    )

    expect(
      await screen.findByText("Removed the old GitHub connection.")
    ).toBeTruthy()
    expect(back.requests).toContainEqual(
      expect.objectContaining({
        method: "DELETE",
        path: `${org}/apps/connections/apc_old`,
      })
    )
    expect(
      back.requests.some(
        (request) =>
          request.method === "DELETE" &&
          request.path === `${org}/apps/connections/apc_new`
      )
    ).toBe(false)
    await waitFor(() => expect(screen.getByText("1 connected")).toBeTruthy())
    expect(
      screen.getAllByRole("button", { name: /Expand GitHub connection/ })
    ).toHaveLength(1)
  })

  it("removes a failed row by itself once a newer connection to the app is active", async () => {
    const state = {
      connections: [
        connection({
          id: "apc_new",
          status: "active",
          created_at: "2026-09-23T12:00:00.123456Z",
        }),
        connection({
          id: "apc_failed",
          status: "failed",
          status_reason: "link expired",
          created_at: "2026-09-22T09:00:00Z",
        }),
      ],
    }
    window.history.replaceState(
      null,
      "",
      "/?view=tools&app_connection=apc_new&status=active"
    )
    const { requests } = fakeFetch(
      catalog(),
      connections(state),
      quietDetails()
    )
    render(<WorkspaceTools organizationId="org-1" />)

    await waitFor(() =>
      expect(requests).toContainEqual(
        expect.objectContaining({
          method: "DELETE",
          path: `${org}/apps/connections/apc_failed`,
        })
      )
    )
    expect(
      await screen.findByText("GitHub is connected and ready to use.")
    ).toBeTruthy()
    // Nothing hung off it, so it never showed as a second GitHub card.
    expect(
      screen.queryByRole("button", { name: /Expand GitHub connection \(/ })
    ).toBeNull()
    expect(screen.getByText("1 connected")).toBeTruthy()
    expect(
      requests.filter((request) => request.method === "DELETE")
    ).toHaveLength(1)
  })

  it("reads a failed return, shows why from the connection itself, and clears the address", async () => {
    const state = {
      connections: [
        connection({
          id: "apc_fail",
          toolkit: "gmail",
          status: "failed",
          status_reason: "the member did not finish connecting",
        }),
      ],
    }
    window.history.replaceState(
      null,
      "",
      "/?view=tools&app_connection=apc_fail&status=failed"
    )
    const { requests } = fakeFetch(
      catalog(),
      connections(state),
      quietDetails()
    )
    render(<WorkspaceTools organizationId="org-1" />)

    expect(
      await screen.findByText(
        "Gmail didn't connect: it wasn't finished at the app. Try again."
      )
    ).toBeTruthy()
    expect(requests).toContainEqual(
      expect.objectContaining({
        method: "GET",
        path: `${org}/apps/connections/apc_fail`,
      })
    )
    expect(window.location.search).toBe("?view=tools")
    // Its detail is open, with what can be done about it.
    expect(
      screen.getByText("Didn't connect: it wasn't finished at the app.")
    ).toBeTruthy()
    expect(screen.getByRole("button", { name: /Reconnect/ })).toBeTruthy()

    fireEvent.click(screen.getByRole("button", { name: "Dismiss message" }))
    expect(screen.queryByText(/Gmail didn't connect/)).toBeNull()

    // A failed connection has nothing attached, so removing it asks nothing.
    fireEvent.click(screen.getByRole("button", { name: "Remove" }))
    expect(
      await screen.findByText("Removed the Gmail connection that failed.")
    ).toBeTruthy()
    expect(requests).toContainEqual(
      expect.objectContaining({
        method: "DELETE",
        path: `${org}/apps/connections/apc_fail`,
      })
    )
  })

  it("opens the existing connection instead of retrying when connect answers conflict", async () => {
    const state: { connections: Connection[] } = { connections: [] }
    const assign = stubLocation("http://localhost:3000/?view=tools")
    fakeFetch(
      catalog(),
      connections(state),
      quietDetails(),
      ({ method, path }) => {
        if (method !== "POST" || path !== `${org}/apps/connections`)
          return undefined
        // Another tab started connecting Gmail after this page loaded.
        state.connections = [
          connection({ id: "apc_gm", toolkit: "gmail", status: "pending" }),
        ]
        return apiError(
          "conflict",
          "gmail is already being connected for this member",
          409
        )
      }
    )
    render(<WorkspaceTools organizationId="org-1" />)

    fireEvent.click(
      await screen.findByRole("button", { name: "Gmail details" })
    )
    fireEvent.click(screen.getByRole("button", { name: "Connect Gmail" }))
    expect(
      await screen.findByText(
        "Gmail is already connecting. It's open in My tools."
      )
    ).toBeTruthy()
    expect(
      screen.getByRole("button", { name: "Cancel connecting" })
    ).toBeTruthy()
    expect(assign).not.toHaveBeenCalled()
    expect(screen.queryByRole("alert")).toBeNull()
  })

  it("queues a run, chooses kinds, and removes a source only after confirming", async () => {
    const state = { connections: [connection()] }
    let sources = [source()]
    const { requests } = fakeFetch(
      catalog(),
      connections(state),
      ({ method, path, body }) => {
        if (path === "/v1/sources/providers")
          return json({ providers: ["github"] })
        if (path === "/v1/sources/kinds")
          return json({
            kinds: [
              {
                name: "conversation",
                label: "Conversations",
                hint: "Mail and messages with people",
                default: true,
              },
              {
                name: "work_material",
                label: "Documents and notes",
                hint: "Plans, specs, notes",
                default: true,
              },
              {
                name: "newsletter",
                label: "Newsletters and announcements",
                hint: "Digests sent to many",
                default: false,
              },
            ],
          })
        if (path === `${org}/sources` && method === "GET")
          return json({ sources })
        if (path === `${org}/sources/source-1/sync` && method === "POST")
          return json(source())
        if (path === `${org}/sources/source-1/kinds` && method === "PUT") {
          const include = (body as { include_kinds: string[] | null })
            .include_kinds
          sources = [source({ include_kinds: include ?? undefined })]
          return json(sources[0])
        }
        if (path === `${org}/sources/source-1` && method === "DELETE") {
          sources = []
          return noContent()
        }
        return undefined
      },
      quietDetails()
    )
    render(<WorkspaceTools organizationId="org-1" />)

    fireEvent.click(
      await screen.findByRole("button", { name: "Expand GitHub connection" })
    )
    fireEvent.click(screen.getByRole("tab", { name: "Indexing" }))
    fireEvent.click(
      await screen.findByRole("button", { name: "Run oryxa/app now" })
    )
    await waitFor(() =>
      expect(requests).toContainEqual(
        expect.objectContaining({
          method: "POST",
          path: `${org}/sources/source-1/sync`,
        })
      )
    )

    fireEvent.click(
      screen.getByRole("button", {
        name: "Kinds of document indexed from oryxa/app",
      })
    )
    const kinds = await screen.findByRole("form", {
      name: "Kinds indexed from oryxa/app",
    })
    const conversations = await within(kinds).findByRole<HTMLInputElement>(
      "checkbox",
      { name: /Conversations/ }
    )
    expect(conversations.checked).toBe(true)
    expect(
      within(kinds).getByRole<HTMLInputElement>("checkbox", {
        name: /Newsletters/,
      }).checked
    ).toBe(false)
    fireEvent.click(conversations)
    fireEvent.click(
      within(kinds).getByRole("checkbox", { name: /Newsletters/ })
    )
    fireEvent.click(within(kinds).getByRole("button", { name: "Save kinds" }))
    await waitFor(() =>
      expect(
        requests.find(
          (request) =>
            request.method === "PUT" &&
            request.path === `${org}/sources/source-1/kinds`
        )?.body
      ).toEqual({ include_kinds: ["work_material", "newsletter"] })
    )
    expect(await screen.findByText(/Chosen kinds/)).toBeTruthy()

    fireEvent.click(
      screen.getByRole("button", {
        name: "Kinds of document indexed from oryxa/app",
      })
    )
    fireEvent.click(await screen.findByRole("button", { name: "Use defaults" }))
    await waitFor(() =>
      expect(
        requests.filter(
          (request) =>
            request.method === "PUT" &&
            request.path === `${org}/sources/source-1/kinds`
        )[1]?.body
      ).toEqual({ include_kinds: null })
    )

    fireEvent.click(screen.getByRole("button", { name: "Remove oryxa/app" }))
    expect(
      screen.getByText(
        "Stops indexing it and removes what it indexed from search."
      )
    ).toBeTruthy()
    expect(requests.some((request) => request.method === "DELETE")).toBe(false)
    fireEvent.click(
      screen.getByRole("button", { name: "Stop indexing oryxa/app" })
    )
    expect(
      await screen.findByText(
        "No sources registered yet. Add one to make its content searchable."
      )
    ).toBeTruthy()
    expect(requests).toContainEqual(
      expect.objectContaining({
        method: "DELETE",
        path: `${org}/sources/source-1`,
      })
    )
  })
})
