import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"
import worker from "./server"
import type { Env } from "./server"

// The Worker's own decisions, with the Agents SDK, the Durable Object and
// TanStack Start standing in: `fetch` is Go, `routeAgentRequest` is the
// object, and `startFetch` is the app.
const sdk = vi.hoisted(() => ({
  startFetch: vi.fn(),
  routeAgentRequest: vi.fn(),
  getAgentByName: vi.fn(),
  purgeTranscript: vi.fn(),
}))

vi.mock("@tanstack/react-start/server-entry", () => ({
  default: { fetch: sdk.startFetch },
}))

vi.mock("agents", () => ({
  Agent: class {},
  getAgentByName: sdk.getAgentByName,
  routeAgentRequest: sdk.routeAgentRequest,
  getCurrentAgent: () => ({ connection: undefined }),
}))

vi.mock("@cloudflare/ai-chat", () => ({ AIChatAgent: class {} }))

const app = "http://localhost:3003"
const goThread = "http://go.test/v1/organizations/org_sim/threads/thread_1"
const chat = `${app}/agents/thread-chat-agent/org_sim~thread_1`
const cookie = "oryxa_access=token; sidebar_state=true"

const env = {
  ORYXA_API_ORIGIN: "http://go.test",
  ThreadChatAgent: { idFromName: () => "id" },
} as unknown as Env

function json(body: unknown, status: number): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  })
}

function goError(status: number, code: string, message: string): Response {
  return json({ error: { code, message } }, status)
}

function socketRequest(url = chat, headers: Record<string, string> = {}) {
  return new Request(url, {
    headers: {
      Upgrade: "websocket",
      Connection: "Upgrade",
      Origin: app,
      Cookie: cookie,
      ...headers,
    },
  })
}

function deleteRequest(headers: Record<string, string> = {}) {
  return new Request(chat, {
    method: "DELETE",
    headers: { Origin: app, Cookie: cookie, ...headers },
  })
}

/** The Worker's answer. A test's request has no `cf` properties; the Worker never reads them. */
async function send(request: Request): Promise<Response> {
  return worker.fetch(request as Parameters<typeof worker.fetch>[0], env)
}

async function errorOf(
  response: Response
): Promise<{ code?: string; message?: string }> {
  const body: { error?: { code?: string; message?: string } } =
    await response.json()
  return body.error ?? {}
}

let fetchMock: ReturnType<typeof vi.fn>

beforeEach(() => {
  fetchMock = vi.fn(async () => json({ id: "thread_1", items: [] }, 200))
  vi.stubGlobal("fetch", fetchMock)
  sdk.startFetch.mockReset()
  sdk.startFetch.mockImplementation(async () => new Response("the app"))
  sdk.routeAgentRequest.mockReset()
  sdk.routeAgentRequest.mockImplementation(async () => new Response("[]"))
  sdk.purgeTranscript.mockReset()
  sdk.purgeTranscript.mockResolvedValue(undefined)
  sdk.getAgentByName.mockReset()
  sdk.getAgentByName.mockResolvedValue({
    purgeTranscript: sdk.purgeTranscript,
  })
})

afterEach(() => {
  vi.unstubAllGlobals()
  vi.restoreAllMocks()
})

describe("the app", () => {
  it("serves every other path through TanStack Start", async () => {
    for (const path of ["/", "/app/agents", "/agents", "/agentsx/thread"]) {
      const request = new Request(`${app}${path}`)
      const response = await send(request)
      expect(await response.text()).toBe("the app")
      expect(sdk.startFetch).toHaveBeenLastCalledWith(request)
    }
    expect(fetchMock).not.toHaveBeenCalled()
    expect(sdk.routeAgentRequest).not.toHaveBeenCalled()
  })

  it("answers 404 for any other agent path, without asking Go", async () => {
    for (const path of [
      "/agents/other-agent/org_sim~thread_1",
      "/agents/thread-chat-agent/org_sim~thread_1/purge",
      "/agents/thread-chat-agent/org_sim~thread_1/get-messages/extra",
      "/agents/thread-chat-agent",
    ]) {
      const response = await send(
        new Request(`${app}${path}`, { headers: { Cookie: cookie } })
      )
      expect(response.status).toBe(404)
    }
    expect(fetchMock).not.toHaveBeenCalled()
    expect(sdk.routeAgentRequest).not.toHaveBeenCalled()
    expect(sdk.startFetch).not.toHaveBeenCalled()
  })
})

describe("the chat route's checks", () => {
  it("refuses a name threadAgentName could not have made", async () => {
    for (const name of [
      "org_sim",
      "org_sim~",
      "~thread_1",
      "org_sim~thread_1~extra",
      "org_sim%7Ethread_1",
      "org.sim~thread_1",
      "org_sim~thread%201",
    ]) {
      const response = await send(
        new Request(`${app}/agents/thread-chat-agent/${name}`, {
          headers: { Cookie: cookie },
        })
      )
      expect(response.status).toBe(400)
      expect((await errorOf(response)).code).toBe("invalid_request")
    }
    expect(fetchMock).not.toHaveBeenCalled()
  })

  it("refuses a cross-site socket or delete by its Origin", async () => {
    for (const request of [
      socketRequest(chat, { Origin: "https://evil.test" }),
      new Request(chat, {
        headers: { Upgrade: "websocket", Cookie: cookie },
      }),
      deleteRequest({ Origin: "https://evil.test" }),
      new Request(chat, { method: "DELETE", headers: { Cookie: cookie } }),
    ]) {
      const response = await send(request)
      expect(response.status).toBe(403)
    }
    expect(fetchMock).not.toHaveBeenCalled()
    expect(sdk.routeAgentRequest).not.toHaveBeenCalled()
    expect(sdk.getAgentByName).not.toHaveBeenCalled()
  })

  it("requires a cookie", async () => {
    for (const request of [
      new Request(`${chat}/get-messages`),
      socketRequest(chat, { Cookie: "" }),
      deleteRequest({ Cookie: "" }),
    ]) {
      const response = await send(request)
      expect(response.status).toBe(401)
      expect((await errorOf(response)).code).toBe("unauthorized")
    }
    expect(fetchMock).not.toHaveBeenCalled()
  })

  it("allows only what the page does: GET, the socket, and DELETE of the conversation", async () => {
    const refused = [
      new Request(chat, { method: "POST", headers: { Cookie: cookie } }),
      new Request(chat, { method: "PUT", headers: { Cookie: cookie } }),
      new Request(`${chat}/get-messages`, {
        method: "DELETE",
        headers: { Cookie: cookie, Origin: app },
      }),
      socketRequest(`${chat}/get-messages`),
    ]
    for (const request of refused) {
      const response = await send(request)
      expect(response.status).toBe(405)
    }
    expect(fetchMock).not.toHaveBeenCalled()
  })
})

describe("the access check", () => {
  it("asks Go for the thread with the member's cookie before routing", async () => {
    const response = await send(
      new Request(`${chat}/get-messages`, {
        headers: { Cookie: cookie },
      })
    )
    expect(response.status).toBe(200)
    expect(fetchMock).toHaveBeenCalledTimes(1)
    const [url, init] = fetchMock.mock.calls[0] as [URL, RequestInit]
    expect(String(url)).toBe(goThread)
    expect(init.method ?? "GET").toBe("GET")
    expect(init.headers).toEqual({ Cookie: cookie })
    expect(sdk.routeAgentRequest).toHaveBeenCalledTimes(1)
  })

  it("passes on Go's refusals the page can act on, with Go's code", async () => {
    const cases: Array<[number, string]> = [
      [401, "unauthorized"],
      [403, "forbidden"],
      [404, "not_found"],
      [503, "agents_unavailable"],
    ]
    for (const [status, code] of cases) {
      fetchMock.mockResolvedValueOnce(goError(status, code, "Go's words"))
      const response = await send(socketRequest())
      expect(response.status).toBe(status)
      expect(await errorOf(response)).toEqual({ code, message: "Go's words" })
    }
    expect(sdk.routeAgentRequest).not.toHaveBeenCalled()
  })

  it("gives its own words when something else refused in Go's place", async () => {
    fetchMock.mockResolvedValueOnce(new Response("Not Found", { status: 404 }))
    const response = await send(socketRequest())
    expect(response.status).toBe(404)
    expect((await errorOf(response)).code).toBe("not_found")
  })

  it("reports Go failing as a bad gateway", async () => {
    for (const status of [500, 429, 400]) {
      fetchMock.mockResolvedValueOnce(goError(status, "internal", "boom"))
      const response = await send(socketRequest())
      expect(response.status).toBe(502)
    }
    expect(sdk.routeAgentRequest).not.toHaveBeenCalled()
  })

  it("reports a Go it cannot reach as unavailable", async () => {
    fetchMock.mockRejectedValueOnce(new TypeError("fetch failed"))
    const response = await send(socketRequest())
    expect(response.status).toBe(503)
    expect(sdk.routeAgentRequest).not.toHaveBeenCalled()
  })
})

describe("routing to the Durable Object", () => {
  it("sets the member's cookie and conversation itself, over anything the browser sent", async () => {
    const response = await send(
      socketRequest(chat, {
        "X-Oryxa-Cookie": "oryxa_access=forged",
        "X-Oryxa-Organization": "org_other",
        "X-Oryxa-Thread": "thread_other",
      })
    )
    expect(response.status).toBe(200)
    const [routed, routedEnv] = sdk.routeAgentRequest.mock.calls[0] as [
      Request,
      Env,
    ]
    expect(routed.url).toBe(chat)
    expect(routed.headers.get("Upgrade")).toBe("websocket")
    expect(routed.headers.get("X-Oryxa-Cookie")).toBe(cookie)
    expect(routed.headers.get("X-Oryxa-Organization")).toBe("org_sim")
    expect(routed.headers.get("X-Oryxa-Thread")).toBe("thread_1")
    expect(routedEnv).toBe(env)
  })

  it("routes the stored transcript's GET", async () => {
    sdk.routeAgentRequest.mockResolvedValueOnce(
      json([{ id: "user_1", role: "user", parts: [] }], 200)
    )
    const response = await send(
      new Request(`${chat}/get-messages`, { headers: { Cookie: cookie } })
    )
    expect(await response.json()).toEqual([
      { id: "user_1", role: "user", parts: [] },
    ])
    const [routed] = sdk.routeAgentRequest.mock.calls[0] as [Request]
    expect(new URL(routed.url).pathname).toBe(
      "/agents/thread-chat-agent/org_sim~thread_1/get-messages"
    )
  })

  it("answers 404 when no object matched, and 503 when it could not be reached", async () => {
    sdk.routeAgentRequest.mockResolvedValueOnce(null)
    expect((await send(socketRequest())).status).toBe(404)
    vi.spyOn(console, "error").mockImplementation(() => {})
    sdk.routeAgentRequest.mockRejectedValueOnce(new Error("overloaded"))
    expect((await send(socketRequest())).status).toBe(503)
  })
})

describe("deleting a conversation", () => {
  it("deletes the thread in Go first, then purges the transcript", async () => {
    fetchMock.mockResolvedValueOnce(new Response(null, { status: 204 }))
    const response = await send(deleteRequest())

    expect(response.status).toBe(204)
    expect(fetchMock).toHaveBeenCalledTimes(1)
    const [url, init] = fetchMock.mock.calls[0] as [URL, RequestInit]
    expect(String(url)).toBe(goThread)
    expect(init.method).toBe("DELETE")
    expect(init.headers).toEqual({ Cookie: cookie, Origin: app })
    expect(sdk.getAgentByName).toHaveBeenCalledWith(
      env.ThreadChatAgent,
      "org_sim~thread_1"
    )
    expect(sdk.purgeTranscript).toHaveBeenCalledTimes(1)
    expect(fetchMock.mock.invocationCallOrder[0]).toBeLessThan(
      sdk.purgeTranscript.mock.invocationCallOrder[0]
    )
    expect(sdk.routeAgentRequest).not.toHaveBeenCalled()
  })

  it("keeps the transcript when Go refuses, and passes Go's answer on", async () => {
    for (const [status, code] of [
      [404, "not_found"],
      [403, "forbidden"],
      [401, "unauthorized"],
      [500, "internal"],
    ] as const) {
      fetchMock.mockResolvedValueOnce(goError(status, code, "Go's words"))
      const response = await send(deleteRequest())
      expect(response.status).toBe(status)
      expect(await errorOf(response)).toEqual({ code, message: "Go's words" })
    }
    expect(sdk.getAgentByName).not.toHaveBeenCalled()
    expect(sdk.purgeTranscript).not.toHaveBeenCalled()
  })

  it("keeps the transcript when Go cannot be reached", async () => {
    fetchMock.mockRejectedValueOnce(new TypeError("fetch failed"))
    const response = await send(deleteRequest())
    expect(response.status).toBe(503)
    expect(sdk.purgeTranscript).not.toHaveBeenCalled()
  })

  it("still reports the deletion when the purge after it fails", async () => {
    const logged = vi.spyOn(console, "error").mockImplementation(() => {})
    fetchMock.mockResolvedValueOnce(new Response(null, { status: 204 }))
    sdk.purgeTranscript.mockRejectedValueOnce(new Error("overloaded"))
    const response = await send(deleteRequest())
    expect(response.status).toBe(204)
    expect(logged).toHaveBeenCalled()
  })
})

describe("security headers", () => {
  const site = "https://app.oryxa.in"
  const secured = (response: Response) => {
    expect(response.headers.get("Strict-Transport-Security")).toBe(
      "max-age=31536000; includeSubDomains"
    )
    expect(response.headers.get("X-Content-Type-Options")).toBe("nosniff")
    expect(response.headers.get("Referrer-Policy")).toBe(
      "strict-origin-when-cross-origin"
    )
    expect(response.headers.get("X-Frame-Options")).toBe("DENY")
    expect(response.headers.get("Content-Security-Policy")).toBe(
      "frame-ancestors 'none'; object-src 'none'; base-uri 'self'"
    )
    expect(
      response.headers.get("Content-Security-Policy-Report-Only")
    ).toContain("connect-src 'self' wss://app.oryxa.in")
  }

  it("puts them on the app's pages, which are otherwise left as TanStack Start made them", async () => {
    sdk.startFetch.mockResolvedValueOnce(
      new Response("<!doctype html><p>the app</p>", {
        status: 200,
        headers: { "Content-Type": "text/html; charset=utf-8" },
      })
    )
    const response = await send(new Request(`${site}/`))

    secured(response)
    expect(response.status).toBe(200)
    expect(response.headers.get("Content-Type")).toBe(
      "text/html; charset=utf-8"
    )
    expect(await response.text()).toBe("<!doctype html><p>the app</p>")
  })

  it("puts them on a redirect from the app, whose headers TanStack Start may have frozen", async () => {
    sdk.startFetch.mockResolvedValueOnce(
      Response.redirect(`${site}/auth/sign-in`, 307)
    )
    const response = await send(new Request(`${site}/`))

    secured(response)
    expect(response.status).toBe(307)
    expect(response.headers.get("Location")).toBe(`${site}/auth/sign-in`)
  })

  it("puts them on the Worker's own answers: a refusal, a 404 and a purge's 405", async () => {
    const refused = await send(
      new Request(
        `${site}/agents/thread-chat-agent/org_sim~thread_1/get-messages`
      )
    )
    expect(refused.status).toBe(401)
    secured(refused)

    const missing = await send(new Request(`${site}/agents/other-agent/x~y`))
    expect(missing.status).toBe(404)
    secured(missing)

    const purge = await send(new Request(`${site}/internal/realtime/purge`))
    expect(purge.status).toBe(405)
    secured(purge)
  })

  it("puts them on what a Durable Object answers, and on Go's answer to a delete", async () => {
    sdk.routeAgentRequest.mockResolvedValueOnce(
      json([{ id: "user_1", role: "user", parts: [] }], 200)
    )
    const read = await send(
      new Request(`${chat.replace(app, site)}/get-messages`, {
        headers: { Cookie: cookie },
      })
    )
    secured(read)
    expect(await read.json()).toEqual([
      { id: "user_1", role: "user", parts: [] },
    ])

    fetchMock.mockResolvedValueOnce(goError(500, "internal", "Go's words"))
    const deleted = await send(
      new Request(chat.replace(app, site), {
        method: "DELETE",
        headers: { Origin: site, Cookie: cookie },
      })
    )
    expect(deleted.status).toBe(500)
    secured(deleted)
    expect(await errorOf(deleted)).toEqual({
      code: "internal",
      message: "Go's words",
    })
  })

  it("hands a WebSocket upgrade over exactly as the Durable Object made it", async () => {
    // Node cannot build a 101 Response; the object's is this shape.
    const upgrade = {
      status: 101,
      headers: new Headers({ Upgrade: "websocket" }),
      body: null,
      webSocket: {},
    } as unknown as Response
    sdk.routeAgentRequest.mockResolvedValueOnce(upgrade)

    const response = await send(
      socketRequest(chat.replace(app, site), { Origin: site })
    )

    expect(response).toBe(upgrade)
    expect(response.headers.has("Content-Security-Policy")).toBe(false)
  })

  it("still sends the rest over plain http, as in development, but not HSTS", async () => {
    const response = await send(new Request(`${app}/`))

    expect(response.headers.get("Strict-Transport-Security")).toBeNull()
    expect(response.headers.get("X-Frame-Options")).toBe("DENY")
    expect(response.headers.get("Content-Security-Policy")).toContain(
      "frame-ancestors 'none'"
    )
    expect(
      response.headers.get("Content-Security-Policy-Report-Only")
    ).toContain("connect-src 'self' ws://localhost:3003")
  })
})
