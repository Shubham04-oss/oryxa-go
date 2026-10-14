import { describe, expect, it, vi } from "vitest"
import { EDGE_MEMBER_HEADER, handleLive, parseLivePath } from "./edge-proxy"
import type { LiveRequest } from "./edge-proxy"

const app = "http://localhost:3003"
const cookie = "oryxa_access=token"

function liveRequest(
  path: string,
  init: RequestInit & { headers?: Record<string, string> } = {}
): { request: Request; live: LiveRequest } {
  const live = parseLivePath(new URL(path, app).pathname)
  if (!live) throw new Error(`not a live path: ${path}`)
  return {
    request: new Request(`${app}${path}`, {
      ...init,
      headers: { Cookie: cookie, Origin: app, ...init.headers },
    }),
    live,
  }
}

function edge() {
  const fetch = vi.fn(
    async (_request: Request) => new Response('{"call":null}', { status: 200 })
  )
  return { env: { EDGE: { fetch } as unknown as Fetcher }, fetch }
}

const member = vi.fn(async () => ({ userId: "usr_ada" }))

describe("parseLivePath", () => {
  it("names the organization, the room and the rest of the path", () => {
    expect(
      parseLivePath("/live/org_1/rooms/teamspace/tsp_1/call/join")
    ).toEqual({
      org: "org_1",
      kind: "teamspace",
      id: "tsp_1",
      rest: "/call/join",
    })
    expect(parseLivePath("/live/org_1/rooms/thread/th_1")).toEqual({
      org: "org_1",
      kind: "thread",
      id: "th_1",
      rest: "",
    })
  })

  it("refuses an unknown kind and ids that could break out of a name", () => {
    expect(parseLivePath("/live/org_1/rooms/office/x/log")).toBeUndefined()
    expect(
      parseLivePath("/live/org_1/rooms/teamspace/a%2Fb/log")
    ).toBeUndefined()
    expect(
      parseLivePath("/live/org.1/rooms/teamspace/tsp_1/log")
    ).toBeUndefined()
    expect(parseLivePath("/live/org_1/teamspace/tsp_1/log")).toBeUndefined()
  })
})

describe("handleLive", () => {
  it("hands a member's call to the edge, naming only them", async () => {
    const { env, fetch } = edge()
    const { request, live } = liveRequest(
      "/live/org_1/rooms/teamspace/tsp_1/call/join",
      {
        method: "POST",
        body: '{"name":"Ada"}',
        headers: {
          "Content-Type": "application/json",
          Authorization: "Bearer stolen",
          [EDGE_MEMBER_HEADER]: '{"org":"org_1","member":"usr_mallory"}',
          "X-Oryxa-Claims": "{}",
        },
      }
    )
    const response = await handleLive(request, env, live, member)

    expect(response.status).toBe(200)
    expect(member).toHaveBeenCalledWith("org_1", cookie)
    const sent = fetch.mock.calls[0][0]
    expect(sent.url).toBe(
      "https://edge.internal/v1/rooms/teamspace/tsp_1/call/join"
    )
    expect(sent.method).toBe("POST")
    expect(JSON.parse(sent.headers.get(EDGE_MEMBER_HEADER) ?? "")).toEqual({
      org: "org_1",
      member: "usr_ada",
    })
    expect(sent.headers.get("cookie")).toBeNull()
    expect(sent.headers.get("authorization")).toBeNull()
    expect(sent.headers.get("x-oryxa-claims")).toBeNull()
    expect(sent.headers.get("content-type")).toBe("application/json")
    expect(await sent.text()).toBe('{"name":"Ada"}')
  })

  it("passes the log's query on", async () => {
    const { env, fetch } = edge()
    const { request, live } = liveRequest(
      "/live/org_1/rooms/thread/th_1/log?after=12&limit=50"
    )
    await handleLive(request, env, live, member)
    expect(fetch.mock.calls[0][0].url).toBe(
      "https://edge.internal/v1/rooms/thread/th_1/log?after=12&limit=50"
    )
  })

  it("passes on Go's refusal and never reaches the edge", async () => {
    const { env, fetch } = edge()
    const { request, live } = liveRequest("/live/org_1/rooms/thread/th_1/call")
    const refusal = Response.json(
      { error: { code: "forbidden" } },
      { status: 403 }
    )
    const response = await handleLive(request, env, live, async () => refusal)
    expect(response).toBe(refusal)
    expect(fetch).not.toHaveBeenCalled()
  })

  it("refuses routes a page may not use, and the wrong method", async () => {
    const { env, fetch } = edge()
    const write = liveRequest("/live/org_1/rooms/thread/th_1/log", {
      method: "POST",
      body: "{}",
    })
    expect(
      (await handleLive(write.request, env, write.live, member)).status
    ).toBe(405)
    const sandbox = liveRequest(
      "/live/org_1/rooms/thread/th_1/sandbox/shared/exec",
      {
        method: "POST",
        body: "{}",
      }
    )
    expect(
      (await handleLive(sandbox.request, env, sandbox.live, member)).status
    ).toBe(404)
    const notSocket = liveRequest("/live/org_1/rooms/thread/th_1/voice/live")
    expect(
      (await handleLive(notSocket.request, env, notSocket.live, member)).status
    ).toBe(405)
    expect(fetch).not.toHaveBeenCalled()
  })

  it("refuses a cross-site change, a missing cookie and a missing edge", async () => {
    const { env } = edge()
    const crossSite = liveRequest("/live/org_1/rooms/thread/th_1/call/leave", {
      method: "POST",
      headers: { Origin: "https://evil.test" },
    })
    expect(
      (await handleLive(crossSite.request, env, crossSite.live, member)).status
    ).toBe(403)

    const live = parseLivePath("/live/org_1/rooms/thread/th_1/call")
    if (!live) throw new Error("no path")
    const anonymous = new Request(`${app}/live/org_1/rooms/thread/th_1/call`)
    expect((await handleLive(anonymous, env, live, member)).status).toBe(401)

    const { request } = liveRequest("/live/org_1/rooms/thread/th_1/call")
    const unbound = await handleLive(request, {}, live, member)
    expect(unbound.status).toBe(503)
  })

  it("opens the room's live socket for a page of this app", async () => {
    const { env, fetch } = edge()
    const { request, live } = liveRequest(
      "/live/org_1/rooms/teamspace/tsp_1/live",
      {
        headers: {
          Upgrade: "websocket",
          Connection: "Upgrade",
          "Sec-WebSocket-Key": "abc",
          "Sec-WebSocket-Version": "13",
        },
      }
    )
    await handleLive(request, env, live, member)
    const sent = fetch.mock.calls[0][0]
    expect(sent.url).toBe("https://edge.internal/v1/rooms/teamspace/tsp_1/live")
    expect(sent.headers.get("upgrade")).toBe("websocket")
    expect(sent.headers.get("sec-websocket-key")).toBe("abc")
  })
})

describe("a room's sandboxes and browsers", () => {
  it("passes what a member may do, and nothing that runs or drives", async () => {
    const { env, fetch } = edge()
    const allowed: [string, string][] = [
      ["GET", "/children"],
      ["GET", "/sandbox/shared/files"],
      ["POST", "/sandbox/shared/files/read"],
      ["POST", "/sandbox/private-usr_ada/preview"],
      ["POST", "/sandbox/run_1/terminal"],
      ["POST", "/browser/br_1/live"],
      ["GET", "/browser/br_1/handoff"],
      ["POST", "/browser/br_1/resume"],
    ]
    for (const [method, rest] of allowed) {
      const { request, live } = liveRequest(`/live/org_1/rooms/thread/th_1${rest}`, {
        method,
        body: method === "POST" ? "{}" : undefined,
      })
      expect((await handleLive(request, env, live, member)).status).toBe(200)
    }
    const refused: [string, string][] = [
      ["POST", "/sandbox/shared/exec/command"],
      ["POST", "/sandbox/shared/files/write"],
      ["POST", "/browser/br_1/navigate"],
      ["POST", "/browser/br_1/handoff"],
      ["GET", "/sandbox/../files"],
    ]
    for (const [method, rest] of refused) {
      const { request, live } = liveRequest(`/live/org_1/rooms/thread/th_1${rest}`, {
        method,
        body: method === "POST" ? "{}" : undefined,
      })
      expect((await handleLive(request, env, live, member)).status).not.toBe(200)
    }
    expect(fetch).toHaveBeenCalledTimes(allowed.length)
  })
})
