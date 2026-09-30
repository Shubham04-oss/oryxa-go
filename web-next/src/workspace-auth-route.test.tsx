// @vitest-environment jsdom

import {
  afterEach,
  beforeAll,
  beforeEach,
  describe,
  expect,
  it,
  vi,
} from "vitest"
import {
  act,
  cleanup,
  fireEvent,
  render,
  screen,
  waitFor,
} from "@testing-library/react"
import type { QueryClient } from "@tanstack/react-query"
import { focusManager, QueryClientProvider } from "@tanstack/react-query"
import {
  createMemoryHistory,
  createRootRoute,
  createRouter,
  Outlet,
  RouterProvider,
} from "@tanstack/react-router"
import { Route } from "./routes/index"
import { Route as SignInRoute } from "./routes/auth.sign-in"
import {
  apiRequest,
  resumeSessionAfterSignIn,
  sessionHasEnded,
} from "@/lib/api/client"
import { getSession, signIn } from "@/lib/auth/client"
import type { Session } from "@/lib/auth/client"
import { getContext } from "@/integrations/tanstack-query/context"
import { rememberChoice, rememberedChoice } from "@/lib/threads/chat-choice"
import { json, mockApi } from "@/test/teamspaces"

// Keep the real WorkspaceShell, Home reads, agent chat and Agents SDK hooks.
// Only the browser's transport is a stand-in, so SDK unmount closes are
// observable.
class TestWebSocket extends EventTarget {
  static readonly CONNECTING = 0
  static readonly OPEN = 1
  static readonly CLOSING = 2
  static readonly CLOSED = 3
  readonly CONNECTING = 0
  readonly OPEN = 1
  readonly CLOSING = 2
  readonly CLOSED = 3
  readyState = 0
  readonly close = vi.fn(() => {
    this.readyState = 3
  })
  readonly send = vi.fn()
  readonly url: string

  constructor(url: string) {
    super()
    this.url = url
    sockets.push(this)
  }
}

const sockets: TestWebSocket[] = []
const clients: QueryClient[] = []
const savedThread = () => ({
  id: "th_saved",
  title: "Saved conversation",
  origin: "chat",
  updated_at: new Date().toISOString(),
})
const leo = {
  id: "agt_leo",
  organization_id: "org_sim",
  owner_user_id: "user-member",
  kind: "primary",
  name: "Leo",
  description: "",
  instructions: "",
  status: "active",
  revision: 1,
  created_at: "2026-09-20T10:00:00Z",
  updated_at: "2026-09-20T10:00:00Z",
}
const session: Session = {
  user: {
    id: "user-member",
    email: "member@example.com",
    display_name: "Member",
  },
  organizations: [
    { id: "org_sim", name: "Example workspace", slug: "example" },
  ],
}

async function renderWorkspace({ cached = true } = {}) {
  const client = getContext().queryClient
  clients.push(client)
  if (cached) client.setQueryData(["auth", "session"], session)
  const root = createRootRoute({ component: Outlet })
  const home = Route.update({
    id: "/",
    path: "/",
    getParentRoute: () => root,
  } as Parameters<typeof Route.update>[0])
  const signInRoute = SignInRoute.update({
    id: "/auth/sign-in",
    path: "/auth/sign-in",
    getParentRoute: () => root,
  } as Parameters<typeof SignInRoute.update>[0])
  const router = createRouter({
    routeTree: root.addChildren([home, signInRoute]),
    history: createMemoryHistory({ initialEntries: ["/?org=org_sim"] }),
    defaultPendingMinMs: 0,
  })
  vi.stubGlobal("WebSocket", TestWebSocket)
  render(
    <QueryClientProvider client={client}>
      <RouterProvider router={router} />
    </QueryClientProvider>
  )
  await act(async () => {
    await router.load()
  })
  return { client, router }
}

// Vitest uses the production route splitting transform. Preload its actual
// components before starting fake clocks, without making Home an eager export.
beforeAll(async () => {
  await Route.options.component?.preload?.()
  await SignInRoute.options.component?.preload?.()
}, 30_000)

beforeEach(() => {
  focusManager.setFocused(true)
  vi.stubGlobal("matchMedia", () => ({
    matches: false,
    addEventListener: vi.fn(),
    removeEventListener: vi.fn(),
  }))
  vi.stubGlobal("scrollTo", vi.fn())
  vi.spyOn(HTMLCanvasElement.prototype, "getContext").mockReturnValue(null)
  window.history.replaceState({}, "", "/?org=org_sim#work")
})

function liveApi() {
  let expired = false
  let passwordAccepted = false
  const api = mockApi(({ url }) => {
    const path = url.pathname
    if (path === "/v1/auth/password/login") {
      if (!passwordAccepted) return json({ error: "Wrong password" }, 401)
      expired = false
      return new Response(null, { status: 204 })
    }
    if (path === "/v1/auth/config") return json({ identity_providers: [] })
    if (expired) return json({ error: "Session expired" }, 401)
    if (path === "/v1/auth/me") return json(session)
    if (path === "/v1/organizations/org_sim")
      return json({ membership: { role: "member" } })
    if (path.endsWith("/agents")) return json({ agents: [leo] })
    if (path.endsWith("/threads")) return json({ threads: [savedThread()] })
    if (path.endsWith("/threads/th_saved"))
      return json({ ...savedThread(), items: [], pending_confirmations: [] })
    if (path.endsWith("/get-messages")) return json([])
    if (path.endsWith("/agent-requests")) return json({ requests: [] })
    if (path.endsWith("/agent-leases"))
      return json({
        leases: [],
        observed_at: new Date().toISOString(),
        has_more: false,
      })
    if (path.endsWith("/activity")) return json({ activity: [] })
    if (path.endsWith("/members")) return json({ members: [] })
    return undefined
  })
  return {
    ...api,
    expire: () => {
      expired = true
    },
    allowSignIn: () => {
      passwordAccepted = true
    },
  }
}

afterEach(() => {
  cleanup()
  for (const client of clients) client.clear()
  clients.length = 0
  sockets.length = 0
  vi.useRealTimers()
  vi.unstubAllGlobals()
  vi.restoreAllMocks()
  focusManager.setFocused(undefined)
  resumeSessionAfterSignIn()
})

describe("workspace session expiry", () => {
  it("unmounts a cached authenticated shell, closes real SDK sockets and stops private polling/focus reads", async () => {
    vi.useFakeTimers()
    const api = liveApi()
    const { client } = await renderWorkspace()
    await act(async () => {
      await vi.advanceTimersByTimeAsync(100)
    })
    expect(screen.getByRole("heading", { name: "Today" })).toBeTruthy()
    expect(api.calls.some((call) => call.path.includes("agent-leases"))).toBe(
      true
    )
    // The member opens their conversation from Recent: its chat, in the
    // center, holds a real socket.
    const choiceScope = { organizationId: "org_sim", userId: "user-member" }
    rememberChoice(choiceScope, "agt_leo", "th_saved")
    fireEvent.click(screen.getByRole("button", { name: /^Saved conversation/ }))
    // The chat gives choosing its conversation a few seconds, and opening it
    // a few renders.
    for (let round = 0; round < 6 && sockets.length === 0; round += 1) {
      await act(async () => {
        await vi.advanceTimersByTimeAsync(2_500)
      })
    }
    expect(
      sockets.some((socket) => socket.url.includes("thread-chat-agent"))
    ).toBe(true)
    const initialReads = api.calls.filter((call) =>
      call.path.includes("agent-requests")
    ).length
    await act(async () => {
      await vi.advanceTimersByTimeAsync(10_100)
    })
    expect(
      api.calls.filter((call) => call.path.includes("agent-requests")).length
    ).toBeGreaterThan(initialReads)
    const cancelQueries = vi.spyOn(client, "cancelQueries")
    api.expire()
    await act(async () => {
      await vi.advanceTimersByTimeAsync(10_100)
    })
    expect(
      screen.getByRole("heading", { name: "Your session has ended." })
    ).toBeTruthy()
    expect(screen.queryByRole("heading", { name: "Today" })).toBeNull()
    expect(sessionHasEnded()).toBe(true)
    // Every socket the chat opened, however often it reconnected while the
    // session was still readable, is closed with the shell.
    const openSockets = [...sockets]
    expect(openSockets.length).toBeGreaterThan(0)
    for (const socket of openSockets) expect(socket.close).toHaveBeenCalled()
    expect(
      client
        .getQueryCache()
        .getAll()
        .filter((query) => query.state.data !== undefined)
    ).toEqual([])
    expect(client.isFetching()).toBe(0)
    expect(cancelQueries).toHaveBeenCalled()
    expect(rememberedChoice(choiceScope, "agt_leo")).toBe("th_saved")
    const calls = api.calls.length
    await act(async () => {
      for (const socket of openSockets) {
        socket.dispatchEvent(
          new CloseEvent("close", { code: 1000, wasClean: true })
        )
      }
      focusManager.setFocused(false)
      focusManager.setFocused(true)
      window.dispatchEvent(new Event("focus"))
      window.dispatchEvent(new Event("online"))
      document.dispatchEvent(new Event("visibilitychange"))
      await vi.advanceTimersByTimeAsync(90_000)
    })
    expect(api.calls).toHaveLength(calls)
    expect(sockets).toHaveLength(openSockets.length)
    await expect(getSession()).rejects.toMatchObject({ code: "session_ended" })
    expect(api.calls).toHaveLength(calls)
    const link = screen.getByRole("link", { name: "Sign in again" })
    const returnLink = new URL(
      link.getAttribute("href")!,
      window.location.origin
    )
    expect(returnLink.pathname).toBe("/auth/sign-in")
    expect(returnLink.searchParams.get("redirect")).toBe("/?org=org_sim#work")
  })

  it("keeps the latch through failed login and recovers through the real sign-in page", async () => {
    const api = liveApi()
    await renderWorkspace()
    await screen.findByRole("heading", { name: "Today" })
    api.expire()
    await act(async () => {
      await apiRequest("/v1/expire").catch(() => undefined)
    })
    fireEvent.click(screen.getByRole("link", { name: "Sign in again" }))
    await screen.findByLabelText("Email")
    fireEvent.change(screen.getByLabelText("Email"), {
      target: { value: "member@example.com" },
    })
    fireEvent.change(screen.getByLabelText("Password"), {
      target: { value: "incorrect" },
    })
    fireEvent.click(screen.getByRole("button", { name: "Sign in" }))
    await screen.findByText("That email and password do not match.")
    expect(sessionHasEnded()).toBe(true)
    api.allowSignIn()
    fireEvent.change(screen.getByLabelText("Password"), {
      target: { value: "correct-password" },
    })
    fireEvent.click(screen.getByRole("button", { name: "Sign in" }))
    await screen.findByRole("heading", { name: "Today" })
    expect(sessionHasEnded()).toBe(false)
    expect(
      api.calls.filter((call) => call.path === "/v1/auth/password/login")
    ).toHaveLength(2)
    expect(api.calls.some((call) => call.path === "/v1/auth/me")).toBe(true)
  })

  it("a stale auth/me 401 after sign-in cannot redirect the authenticated shell", async () => {
    const api = liveApi()
    const { client } = await renderWorkspace()
    await screen.findByRole("heading", { name: "Today" })
    let answerOld!: (response: Response) => void
    api.fetchMock.mockImplementationOnce(
      async () =>
        new Promise<Response>((resolve) => {
          answerOld = resolve
        })
    )
    const oldRead = client.fetchQuery({
      queryKey: ["auth", "session"],
      queryFn: getSession,
      staleTime: 0,
    })
    const settled = oldRead.catch((error: unknown) => error)
    await waitFor(() => expect(answerOld).toBeTypeOf("function"))
    api.allowSignIn()
    await act(async () => {
      await signIn({
        email: "member@example.com",
        password: "correct-password",
      })
    })
    await act(async () => {
      answerOld(json({ error: "old session expired" }, 401))
      await settled
    })
    expect(await settled).toMatchObject({ name: "AbortError" })
    expect(sessionHasEnded()).toBe(false)
    expect(screen.getByRole("heading", { name: "Today" })).toBeTruthy()
    expect(
      screen.queryByRole("heading", { name: "Your session has ended." })
    ).toBeNull()
    expect(api.calls.some((call) => call.path === "/v1/auth/refresh")).toBe(
      false
    )
  })

  it("backs off transient boot refresh failures and reaches the workspace without a terminal logout", async () => {
    const api = liveApi()
    const normal = api.fetchMock.getMockImplementation()!
    let refreshes = 0
    let sessionReads = 0
    api.fetchMock.mockImplementation(async (input, init) => {
      if (String(input) === "/v1/auth/me" && ++sessionReads < 3)
        return json({ error: "access expired" }, 401)
      if (String(input) === "/v1/auth/refresh") {
        refreshes++
        return json({ error: "temporarily unavailable" }, 503)
      }
      return normal(input, init)
    })
    vi.useFakeTimers()
    await renderWorkspace({ cached: false })
    await act(async () => {
      await vi.advanceTimersByTimeAsync(50)
    })
    expect(sessionReads).toBe(1)
    expect(sessionHasEnded()).toBe(false)
    await act(async () => {
      await vi.advanceTimersByTimeAsync(900)
    })
    expect(sessionReads).toBe(1)
    await act(async () => {
      await vi.advanceTimersByTimeAsync(150)
    })
    expect(sessionReads).toBe(2)
    await act(async () => {
      await vi.advanceTimersByTimeAsync(2000)
    })
    expect(sessionReads).toBe(3)
    expect(refreshes).toBe(2)
    expect(screen.getByRole("heading", { name: "Today" })).toBeTruthy()
    expect(sessionHasEnded()).toBe(false)
  })
})
