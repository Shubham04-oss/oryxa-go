import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"
import { signPurge } from "./realtime-purge"
import worker from "./server"
import type { Env } from "./server"

// The Worker's table of realtime objects: the checks every entry shares, run
// for each of them, and what differs between them. As in server.test.ts,
// `fetch` is Go, `routeAgentRequest` the objects, and `startFetch` the app.
const sdk = vi.hoisted(() => ({
  startFetch: vi.fn(),
  routeAgentRequest: vi.fn(),
  getAgentByName: vi.fn(),
  purgeTranscript: vi.fn(),
  purge: vi.fn(),
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
const cookie = "oryxa_access=token; sidebar_state=true"

const purgeSecret = "0123456789abcdef0123456789abcdef"

const env = {
  ORYXA_API_ORIGIN: "http://go.test",
  REALTIME_PURGE_SECRET: purgeSecret,
  ThreadChatAgent: { idFromName: () => "thread-id" },
  SpecialistChatAgent: { idFromName: () => "specialist-id" },
  TeamspaceRoomAgent: { idFromName: () => "room-id" },
  DirectMessageAgent: { idFromName: () => "dm-id" },
  InboxAgent: { idFromName: () => "inbox-id" },
  RealtimeRegistryAgent: { idFromName: () => "registry-id" },
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

type RealtimeCase = {
  segment: string
  binding:
    | "ThreadChatAgent"
    | "SpecialistChatAgent"
    | "TeamspaceRoomAgent"
    | "DirectMessageAgent"
    | "InboxAgent"
  /** What a page may GET beneath it, besides the socket. */
  reads: string[]
  /** Whether a page may DELETE it. */
  deletes: boolean
  /** A name the object's builder makes. */
  name: string
  /** Names it could not have made. */
  invalidNames: string[]
  /** Go's access check: every URL it asks, in order. */
  accessUrls: string[]
  /** Go's answer letting the member in, for each of them. */
  accepted: (url: string) => Response
  /** The headers the object trusts. */
  identity: Record<string, string>
  /** An `X-Oryxa-*` header only another object reads. */
  foreignHeader: string
}

/** Go's `GET /v1/organizations/{org}` for the member, and the organization's members. */
function organizationAnswer(url: string, members = ["user-ada", "user-bo"]) {
  if (url.endsWith("/members")) {
    return json(
      {
        members: members.map((userId) => ({
          organization_id: "org_sim",
          user_id: userId,
          role: "member",
        })),
      },
      200
    )
  }
  return json(
    {
      organization: { id: "org_sim", name: "Simulated Org" },
      membership: { organization_id: "org_sim", user_id: "user-ada" },
    },
    200
  )
}

const objects: RealtimeCase[] = [
  {
    segment: "thread-chat-agent",
    binding: "ThreadChatAgent",
    reads: ["get-messages"],
    deletes: true,
    name: "org_sim~thread_1",
    invalidNames: [
      "org_sim",
      "~thread_1",
      "org_sim~thread_1~extra",
      "org_sim%7Ethread_1",
      "org.sim~thread_1",
    ],
    accessUrls: ["http://go.test/v1/organizations/org_sim/threads/thread_1"],
    accepted: () => json({ id: "thread_1", items: [] }, 200),
    identity: {
      "X-Oryxa-Cookie": cookie,
      "X-Oryxa-Organization": "org_sim",
      "X-Oryxa-Thread": "thread_1",
    },
    foreignHeader: "X-Oryxa-Agent",
  },
  {
    segment: "specialist-chat-agent",
    binding: "SpecialistChatAgent",
    reads: ["get-messages"],
    deletes: true,
    name: "org_sim~agt_Sa-l3s_x",
    invalidNames: [
      "org_sim",
      "org_sim~",
      "~agt_sales",
      "org_sim~agt_sales~thread_1",
      "org_sim%7Eagt_sales",
      "org_sim~agt%20sales",
      "org_sim~agt.sales",
    ],
    accessUrls: ["http://go.test/v1/organizations/org_sim/agents/agt_Sa-l3s_x"],
    accepted: () =>
      json({ id: "agt_Sa-l3s_x", kind: "specialist", status: "active" }, 200),
    identity: {
      "X-Oryxa-Cookie": cookie,
      "X-Oryxa-Organization": "org_sim",
      "X-Oryxa-Agent": "agt_Sa-l3s_x",
    },
    foreignHeader: "X-Oryxa-Thread",
  },
  {
    segment: "teamspace-room-agent",
    binding: "TeamspaceRoomAgent",
    reads: [],
    deletes: false,
    name: "org_sim~tsp_Jixq-Qo1",
    invalidNames: [
      "org_sim",
      "org_sim~",
      "~tsp_launch",
      "org_sim~tsp_launch~user-ada",
      "org_sim%7Etsp_launch",
      "org_sim~tsp%20launch",
      "org.sim~tsp_launch",
    ],
    accessUrls: [
      "http://go.test/v1/organizations/org_sim/teamspaces/tsp_Jixq-Qo1",
    ],
    accepted: () =>
      json({ id: "tsp_Jixq-Qo1", is_member: true, status: "active" }, 200),
    identity: {
      "X-Oryxa-Cookie": cookie,
      "X-Oryxa-Organization": "org_sim",
      "X-Oryxa-Teamspace": "tsp_Jixq-Qo1",
    },
    foreignHeader: "X-Oryxa-Thread",
  },
  {
    segment: "direct-message-agent",
    binding: "DirectMessageAgent",
    reads: [],
    deletes: false,
    name: "org_sim~user-ada~user-bo",
    invalidNames: [
      "org_sim",
      "org_sim~user-ada",
      "org_sim~user-bo~user-ada",
      "org_sim~user-ada~user-ada",
      "org_sim~~user-bo",
      "org_sim~user-ada~user-bo~user-cy",
      "org_sim%7Euser-ada%7Euser-bo",
      "org_sim~user-ada~user%20bo",
      "org.sim~user-ada~user-bo",
      `org_sim~user-ada~${"b".repeat(129)}`,
    ],
    accessUrls: [
      "http://go.test/v1/organizations/org_sim",
      "http://go.test/v1/organizations/org_sim/members",
    ],
    accepted: (url) => organizationAnswer(url),
    identity: {
      "X-Oryxa-Organization": "org_sim",
      "X-Oryxa-User": "user-ada",
    },
    // A DM never calls Go, so it is not handed the member's cookie.
    foreignHeader: "X-Oryxa-Cookie",
  },
  {
    segment: "inbox-agent",
    binding: "InboxAgent",
    reads: [],
    deletes: false,
    name: "org_sim~user-ada",
    invalidNames: [
      "org_sim",
      "org_sim~",
      "~user-ada",
      "org_sim~user-ada~user-bo~x",
      "org_sim%7Euser-ada",
      "org_sim~user%20ada",
      "org.sim~user-ada",
    ],
    accessUrls: ["http://go.test/v1/organizations/org_sim"],
    accepted: (url) => organizationAnswer(url),
    identity: {
      "X-Oryxa-Organization": "org_sim",
      "X-Oryxa-User": "user-ada",
    },
    foreignHeader: "X-Oryxa-Cookie",
  },
]

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

function pathOf(object: RealtimeCase, name = object.name) {
  return `${app}/agents/${object.segment}/${name}`
}

/** A plain GET the object serves: its first read, or, without one, the object itself. */
function readPathOf(object: RealtimeCase) {
  const [read] = object.reads
  return read ? `${pathOf(object)}/${read}` : pathOf(object)
}

function socketRequest(url: string, headers: Record<string, string> = {}) {
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

function deleteRequest(url: string, headers: Record<string, string> = {}) {
  return new Request(url, {
    method: "DELETE",
    headers: { Origin: app, Cookie: cookie, ...headers },
  })
}

let fetchMock: ReturnType<typeof vi.fn>

beforeEach(() => {
  fetchMock = vi.fn()
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

describe.each(objects)("the $segment route", (object) => {
  beforeEach(() => {
    fetchMock.mockImplementation(async (url: URL | string) =>
      object.accepted(String(url))
    )
  })

  it("refuses a name its builder could not have made, before asking Go", async () => {
    for (const name of object.invalidNames) {
      const response = await send(
        new Request(pathOf(object, name), { headers: { Cookie: cookie } })
      )
      expect(response.status).toBe(400)
      expect((await errorOf(response)).code).toBe("invalid_request")
    }
    expect(fetchMock).not.toHaveBeenCalled()
    expect(sdk.routeAgentRequest).not.toHaveBeenCalled()
  })

  it("refuses a cross-site socket or DELETE by its Origin", async () => {
    const deletes = object.deletes
      ? [
          deleteRequest(pathOf(object), { Origin: "https://evil.test" }),
          new Request(pathOf(object), {
            method: "DELETE",
            headers: { Cookie: cookie },
          }),
        ]
      : []
    for (const request of [
      socketRequest(pathOf(object), { Origin: "https://evil.test" }),
      new Request(pathOf(object), {
        headers: { Upgrade: "websocket", Cookie: cookie },
      }),
      ...deletes,
    ]) {
      const response = await send(request)
      expect(response.status).toBe(403)
      expect((await errorOf(response)).code).toBe("forbidden")
    }
    expect(fetchMock).not.toHaveBeenCalled()
    expect(sdk.getAgentByName).not.toHaveBeenCalled()
  })

  it("requires a cookie", async () => {
    for (const request of [
      new Request(readPathOf(object)),
      socketRequest(pathOf(object), { Cookie: "" }),
      ...(object.deletes
        ? [deleteRequest(pathOf(object), { Cookie: "" })]
        : []),
    ]) {
      const response = await send(request)
      expect(response.status).toBe(401)
      expect((await errorOf(response)).code).toBe("unauthorized")
    }
    expect(fetchMock).not.toHaveBeenCalled()
  })

  it("allows only the socket, its reads, and DELETE when it has one", async () => {
    const allowed = object.deletes ? "GET, DELETE" : "GET"
    const refused: Array<[Request, string]> = [
      [
        new Request(pathOf(object), {
          method: "POST",
          headers: { Cookie: cookie },
        }),
        allowed,
      ],
      [
        new Request(pathOf(object), {
          method: "PUT",
          headers: { Cookie: cookie },
        }),
        allowed,
      ],
      ...object.reads.flatMap((read): Array<[Request, string]> => [
        [deleteRequest(`${pathOf(object)}/${read}`), "GET"],
        [socketRequest(`${pathOf(object)}/${read}`), "GET"],
      ]),
      ...(object.deletes
        ? []
        : [[deleteRequest(pathOf(object)), "GET"] as [Request, string]]),
    ]
    for (const [request, allow] of refused) {
      const response = await send(request)
      expect(response.status).toBe(405)
      expect(response.headers.get("Allow")).toBe(allow)
    }
    expect(fetchMock).not.toHaveBeenCalled()
  })

  it("answers 404 for a path beneath it that it does not offer", async () => {
    for (const path of [
      `${pathOf(object)}/purge`,
      `${pathOf(object)}/get-messages/extra`,
      ...(object.reads.includes("get-messages")
        ? []
        : [`${pathOf(object)}/get-messages`]),
    ]) {
      const response = await send(
        new Request(path, { headers: { Cookie: cookie } })
      )
      expect(response.status).toBe(404)
    }
    expect(fetchMock).not.toHaveBeenCalled()
    expect(sdk.startFetch).not.toHaveBeenCalled()
  })

  it("asks Go with the member's cookie before routing", async () => {
    const response = await send(
      new Request(readPathOf(object), {
        headers: { Cookie: cookie },
      })
    )
    expect(response.status).toBe(200)
    const calls = fetchMock.mock.calls as Array<[URL, RequestInit]>
    expect(calls.map(([url]) => String(url))).toEqual(object.accessUrls)
    for (const [, init] of calls) {
      expect(init.method ?? "GET").toBe("GET")
      expect(init.headers).toEqual({ Cookie: cookie })
    }
    for (const order of fetchMock.mock.invocationCallOrder) {
      expect(order).toBeLessThan(
        sdk.routeAgentRequest.mock.invocationCallOrder[0]
      )
    }
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
      const response = await send(socketRequest(pathOf(object)))
      expect(response.status).toBe(status)
      expect(await errorOf(response)).toEqual({ code, message: "Go's words" })
    }
    fetchMock.mockResolvedValueOnce(new Response("Not Found", { status: 404 }))
    const proxied = await send(socketRequest(pathOf(object)))
    expect([proxied.status, (await errorOf(proxied)).code]).toEqual([
      404,
      "not_found",
    ])
    expect(sdk.routeAgentRequest).not.toHaveBeenCalled()
  })

  it("reports Go failing as a bad gateway, and a Go it cannot reach as unavailable", async () => {
    for (const status of [500, 429, 400]) {
      fetchMock.mockResolvedValueOnce(goError(status, "internal", "boom"))
      const response = await send(socketRequest(pathOf(object)))
      expect(response.status).toBe(502)
      expect((await errorOf(response)).code).toBe("bad_gateway")
    }
    fetchMock.mockRejectedValueOnce(new TypeError("fetch failed"))
    const unreachable = await send(socketRequest(pathOf(object)))
    expect(unreachable.status).toBe(503)
    expect(sdk.routeAgentRequest).not.toHaveBeenCalled()
  })

  it("sets its identity headers itself, and drops every other X-Oryxa header the browser sent", async () => {
    const response = await send(
      socketRequest(pathOf(object), {
        "X-Oryxa-Cookie": "oryxa_access=forged",
        "X-Oryxa-Organization": "org_other",
        "X-Oryxa-Thread": "thread_other",
        "X-Oryxa-Agent": "agt_other",
        "X-Oryxa-User": "user-forged",
        "x-oryxa-anything": "forged",
      })
    )
    expect(response.status).toBe(200)
    const [routed, routedEnv] = sdk.routeAgentRequest.mock.calls[0] as [
      Request,
      Env,
    ]
    expect(routed.url).toBe(pathOf(object))
    expect(routed.headers.get("Upgrade")).toBe("websocket")
    const oryxaHeaders = Object.fromEntries(
      [...routed.headers].filter(([name]) => name.startsWith("x-oryxa-"))
    )
    expect(oryxaHeaders).toEqual(
      Object.fromEntries(
        Object.entries(object.identity).map(([name, value]) => [
          name.toLowerCase(),
          value,
        ])
      )
    )
    expect(routed.headers.get(object.foreignHeader)).toBeNull()
    expect(routedEnv).toBe(env)
  })

  it("routes a page's GET beside the socket to the object", async () => {
    sdk.routeAgentRequest.mockResolvedValueOnce(
      json([{ id: "user_1", role: "user", parts: [] }], 200)
    )
    const response = await send(
      new Request(readPathOf(object), {
        headers: { Cookie: cookie },
      })
    )
    expect(await response.json()).toEqual([
      { id: "user_1", role: "user", parts: [] },
    ])
    const [routed] = sdk.routeAgentRequest.mock.calls[0] as [Request]
    expect(new URL(routed.url).pathname).toBe(
      new URL(readPathOf(object)).pathname
    )
  })

  it("answers 404 when no object matched, and 503 when it could not be reached", async () => {
    sdk.routeAgentRequest.mockResolvedValueOnce(null)
    expect((await send(socketRequest(pathOf(object)))).status).toBe(404)
    vi.spyOn(console, "error").mockImplementation(() => {})
    sdk.routeAgentRequest.mockRejectedValueOnce(new Error("overloaded"))
    const unavailable = await send(socketRequest(pathOf(object)))
    expect(unavailable.status).toBe(503)
    expect((await errorOf(unavailable)).code).toBe("unavailable")
  })
})

describe("the thread chat's DELETE", () => {
  it("deletes the thread in Go first, then purges the object's transcript", async () => {
    fetchMock.mockResolvedValueOnce(new Response(null, { status: 204 }))
    const response = await send(
      deleteRequest(`${app}/agents/thread-chat-agent/org_sim~thread_1`)
    )
    expect(response.status).toBe(204)
    const [url, init] = fetchMock.mock.calls[0] as [URL, RequestInit]
    expect([String(url), init.method]).toEqual([
      "http://go.test/v1/organizations/org_sim/threads/thread_1",
      "DELETE",
    ])
    expect(sdk.getAgentByName).toHaveBeenCalledWith(
      env.ThreadChatAgent,
      "org_sim~thread_1"
    )
    expect(fetchMock.mock.invocationCallOrder[0]).toBeLessThan(
      sdk.purgeTranscript.mock.invocationCallOrder[0]
    )
  })
})

describe("the specialist chat", () => {
  const chat = `${app}/agents/specialist-chat-agent/org_sim~agt_sales`
  const goAgent = "http://go.test/v1/organizations/org_sim/agents/agt_sales"

  it("is only for a specialist: the primary agent's chat is its threads", async () => {
    fetchMock.mockImplementation(async () =>
      json({ id: "agt_sales", kind: "primary", status: "active" }, 200)
    )
    for (const request of [
      socketRequest(chat),
      new Request(`${chat}/get-messages`, { headers: { Cookie: cookie } }),
    ]) {
      const response = await send(request)
      expect(response.status).toBe(404)
      expect(await errorOf(response)).toEqual({
        code: "not_found",
        message: "This is not one of your specialists.",
      })
    }
    expect(sdk.routeAgentRequest).not.toHaveBeenCalled()
  })

  it("opens for a paused specialist: Go refuses its runs, not its transcript", async () => {
    fetchMock.mockResolvedValue(
      json({ id: "agt_sales", kind: "specialist", status: "disabled" }, 200)
    )
    expect((await send(socketRequest(chat))).status).toBe(200)
    expect(sdk.routeAgentRequest).toHaveBeenCalledTimes(1)
  })

  it("reports Go accepting with something that is not an agent as a bad gateway", async () => {
    fetchMock.mockResolvedValue(new Response("<html>ok</html>"))
    const response = await send(socketRequest(chat))
    expect(response.status).toBe(502)
    expect(sdk.routeAgentRequest).not.toHaveBeenCalled()
  })

  it("passes on Go's 404 for another member's agent", async () => {
    fetchMock.mockResolvedValue(goError(404, "not_found", "agent not found"))
    const response = await send(
      new Request(`${chat}/get-messages`, { headers: { Cookie: cookie } })
    )
    expect(response.status).toBe(404)
    expect(await errorOf(response)).toEqual({
      code: "not_found",
      message: "agent not found",
    })
  })

  describe("DELETE, which only starts the chat over", () => {
    it("checks that the specialist is the member's own, then clears the object, and deletes nothing in Go", async () => {
      fetchMock.mockResolvedValue(
        json({ id: "agt_sales", kind: "specialist" }, 200)
      )
      const response = await send(deleteRequest(chat))

      expect(response.status).toBe(204)
      expect(fetchMock).toHaveBeenCalledTimes(1)
      const [url, init] = fetchMock.mock.calls[0] as [URL, RequestInit]
      expect(String(url)).toBe(goAgent)
      expect(init.method ?? "GET").toBe("GET")
      expect(init.headers).toEqual({ Cookie: cookie })
      expect(sdk.getAgentByName).toHaveBeenCalledWith(
        env.SpecialistChatAgent,
        "org_sim~agt_sales"
      )
      expect(sdk.purgeTranscript).toHaveBeenCalledTimes(1)
      expect(fetchMock.mock.invocationCallOrder[0]).toBeLessThan(
        sdk.purgeTranscript.mock.invocationCallOrder[0]
      )
      expect(sdk.routeAgentRequest).not.toHaveBeenCalled()
    })

    it("keeps the transcript when Go refuses the member", async () => {
      const refusals = [
        goError(404, "not_found", "agent not found"),
        goError(401, "unauthorized", "access token is required"),
        goError(
          403,
          "forbidden",
          "an agent cannot hold a member's conversation"
        ),
        json({ id: "agt_sales", kind: "primary" }, 200),
      ]
      for (const refusal of refusals) {
        fetchMock.mockResolvedValueOnce(refusal)
        const response = await send(deleteRequest(chat))
        expect([404, 401, 403]).toContain(response.status)
      }
      fetchMock.mockRejectedValueOnce(new TypeError("fetch failed"))
      expect((await send(deleteRequest(chat))).status).toBe(503)
      expect(sdk.getAgentByName).not.toHaveBeenCalled()
      expect(sdk.purgeTranscript).not.toHaveBeenCalled()
    })

    it("reports a clear that failed, since nothing else changed and the page can try again", async () => {
      const logged = vi.spyOn(console, "error").mockImplementation(() => {})
      fetchMock.mockResolvedValue(
        json({ id: "agt_sales", kind: "specialist" }, 200)
      )
      sdk.purgeTranscript.mockRejectedValueOnce(new Error("overloaded"))
      const response = await send(deleteRequest(chat))
      expect(response.status).toBe(503)
      expect(await errorOf(response)).toEqual({
        code: "unavailable",
        message: "Your specialist's conversation is unavailable.",
      })
      expect(logged).toHaveBeenCalled()
    })
  })
})

describe("the teamspace room", () => {
  const roomPath = `${app}/agents/teamspace-room-agent/org_sim~tsp_launch`
  const goTeamspace =
    "http://go.test/v1/organizations/org_sim/teamspaces/tsp_launch"

  it("is only for the teamspace's team: a manager who is not on it is kept out", async () => {
    fetchMock.mockImplementation(async () =>
      json(
        {
          id: "tsp_launch",
          is_member: false,
          can_manage: true,
          status: "active",
        },
        200
      )
    )
    for (const request of [
      socketRequest(roomPath),
      new Request(roomPath, { headers: { Cookie: cookie } }),
    ]) {
      const response = await send(request)
      expect(response.status).toBe(403)
      expect(await errorOf(response)).toEqual({
        code: "forbidden",
        message: "Only the teamspace's team can open its room.",
      })
    }
    const [url] = fetchMock.mock.calls[0] as [URL]
    expect(String(url)).toBe(goTeamspace)
    expect(sdk.routeAgentRequest).not.toHaveBeenCalled()
  })

  it("opens for a paused teamspace: the room itself tells its sockets it is paused", async () => {
    fetchMock.mockResolvedValue(
      json({ id: "tsp_launch", is_member: true, status: "disabled" }, 200)
    )
    expect((await send(socketRequest(roomPath))).status).toBe(200)
    expect(sdk.routeAgentRequest).toHaveBeenCalledTimes(1)
  })

  it("passes on Go's 404 for a teamspace the member is not on, or one deleted", async () => {
    fetchMock.mockResolvedValue(
      goError(404, "not_found", "teamspace not found")
    )
    const response = await send(
      new Request(roomPath, { headers: { Cookie: cookie } })
    )
    expect(response.status).toBe(404)
    expect(await errorOf(response)).toEqual({
      code: "not_found",
      message: "teamspace not found",
    })
  })

  it("reports Go accepting with something that is not a teamspace as a bad gateway", async () => {
    fetchMock.mockResolvedValue(new Response("<html>ok</html>"))
    const response = await send(socketRequest(roomPath))
    expect(response.status).toBe(502)
    expect(sdk.routeAgentRequest).not.toHaveBeenCalled()
  })

  it("has no DELETE: Go deletes the room with its teamspace", async () => {
    const response = await send(deleteRequest(roomPath))
    expect(response.status).toBe(405)
    expect(response.headers.get("Allow")).toBe("GET")
    expect(fetchMock).not.toHaveBeenCalled()
    expect(sdk.getAgentByName).not.toHaveBeenCalled()
  })
})

describe("the direct messages", () => {
  const dm = `${app}/agents/direct-message-agent/org_sim~user-ada~user-bo`

  it("is only for its two people: anyone else is refused as not a participant", async () => {
    fetchMock.mockImplementation(async (url: URL | string) => {
      const answer = String(url)
      if (answer.endsWith("/members")) return organizationAnswer(answer)
      return json(
        { membership: { organization_id: "org_sim", user_id: "user-cy" } },
        200
      )
    })
    for (const request of [
      socketRequest(dm),
      new Request(dm, { headers: { Cookie: cookie } }),
    ]) {
      const response = await send(request)
      expect(response.status).toBe(403)
      expect(await errorOf(response)).toEqual({
        code: "not_participant",
        message: "Only the two people in a conversation can open it.",
      })
    }
    expect(sdk.routeAgentRequest).not.toHaveBeenCalled()
  })

  it("is refused when the other person is not in the organization", async () => {
    fetchMock.mockImplementation(async (url: URL | string) =>
      organizationAnswer(String(url), ["user-ada", "user-cy"])
    )
    const response = await send(socketRequest(dm))
    expect(response.status).toBe(404)
    expect(await errorOf(response)).toEqual({
      code: "peer_not_found",
      message: "This person is not in your organization.",
    })
    expect(sdk.routeAgentRequest).not.toHaveBeenCalled()
  })

  it("passes on Go's refusal of a member outside the organization before anything else", async () => {
    fetchMock.mockImplementation(async () =>
      goError(404, "not_found", "resource not found")
    )
    const response = await send(socketRequest(dm))
    expect(response.status).toBe(404)
    expect(await errorOf(response)).toEqual({
      code: "not_found",
      message: "resource not found",
    })
  })

  it("refuses an agent's token: DMs are between people", async () => {
    fetchMock.mockImplementation(async (url: URL | string) => {
      const answer = String(url)
      if (answer.endsWith("/members")) return organizationAnswer(answer)
      return json(
        {
          membership: { organization_id: "org_sim", user_id: "user-ada" },
          agent: true,
        },
        200
      )
    })
    const response = await send(socketRequest(dm))
    expect(response.status).toBe(403)
    expect((await errorOf(response)).code).toBe("forbidden")
    expect(sdk.routeAgentRequest).not.toHaveBeenCalled()
  })

  it("reports Go answering with something else as a bad gateway", async () => {
    for (const [membership, members] of [
      [json({ membership: {} }, 200), organizationAnswer("/members")],
      [
        json({ membership: { user_id: "user ada" } }, 200),
        organizationAnswer("/members"),
      ],
      [organizationAnswer("/org"), json({ members: "everyone" }, 200)],
      [organizationAnswer("/org"), new Response("<html>ok</html>")],
    ] as Array<[Response, Response]>) {
      fetchMock.mockReset()
      fetchMock.mockResolvedValueOnce(membership).mockResolvedValueOnce(members)
      const response = await send(socketRequest(dm))
      expect(response.status).toBe(502)
    }
    expect(sdk.routeAgentRequest).not.toHaveBeenCalled()
  })
})

describe("a member's inbox", () => {
  const inbox = `${app}/agents/inbox-agent/org_sim~user-bo`

  it("is only its own member's", async () => {
    fetchMock.mockImplementation(async (url: URL | string) =>
      organizationAnswer(String(url))
    )
    const response = await send(socketRequest(inbox))
    expect(response.status).toBe(403)
    expect(await errorOf(response)).toEqual({
      code: "forbidden",
      message: "This is another member's inbox.",
    })
    expect(sdk.routeAgentRequest).not.toHaveBeenCalled()
  })

  it("refuses an agent's token", async () => {
    fetchMock.mockResolvedValue(
      json({ membership: { user_id: "user-bo" }, agent: true }, 200)
    )
    const response = await send(socketRequest(inbox))
    expect(response.status).toBe(403)
    expect(sdk.routeAgentRequest).not.toHaveBeenCalled()
  })
})

describe("the purge", () => {
  const purgePath = `${app}/internal/realtime/purge`
  const body = JSON.stringify({
    event_id: "evt_1",
    organization_id: "org_sim",
    user_id: "user-ada",
  })

  async function signed(timestamp = Math.floor(Date.now() / 1000)) {
    return new Request(purgePath, {
      method: "POST",
      headers: {
        "X-Oryxa-Timestamp": String(timestamp),
        "X-Oryxa-Signature": await signPurge(
          purgeSecret,
          String(timestamp),
          body
        ),
      },
      body,
    })
  }

  it("takes a signed purge without a cookie, and hands it to the organization's registry", async () => {
    sdk.getAgentByName.mockResolvedValueOnce({ purge: sdk.purge })
    sdk.purge.mockResolvedValueOnce("purged")
    const response = await send(await signed())
    expect(response.status).toBe(204)
    expect(sdk.getAgentByName).toHaveBeenCalledWith(
      env.RealtimeRegistryAgent,
      "org_sim"
    )
    expect(sdk.purge).toHaveBeenCalledWith("evt_1", "user-ada")
    expect(fetchMock).not.toHaveBeenCalled()
    expect(sdk.startFetch).not.toHaveBeenCalled()
  })

  it("refuses one that is not signed, and anything but a POST", async () => {
    const unsigned = await send(
      new Request(purgePath, {
        method: "POST",
        body,
        headers: { Cookie: cookie },
      })
    )
    expect(unsigned.status).toBe(401)
    const stale = await send(await signed(Math.floor(Date.now() / 1000) - 600))
    expect(stale.status).toBe(401)
    const get = await send(new Request(purgePath))
    expect(get.status).toBe(405)
    expect(sdk.getAgentByName).not.toHaveBeenCalled()
  })

  it("leaves every other internal path to the app", async () => {
    const response = await send(
      new Request(`${purgePath}/more`, { method: "POST" })
    )
    expect(await response.text()).toBe("the app")
  })
})

describe("paths the table does not hold", () => {
  it("answers 404 for an object it does not route, the registry among them, and serves the rest of the app", async () => {
    for (const path of [
      "/agents/realtime-registry-agent/org_sim",
      "/agents/other-agent/org_sim~user-ada",
    ]) {
      const unknown = await send(
        new Request(`${app}${path}`, {
          headers: {
            Cookie: cookie,
            Upgrade: "websocket",
            Origin: app,
          },
        })
      )
      expect(unknown.status).toBe(404)
    }
    const page = await send(new Request(`${app}/agents`))
    expect(await page.text()).toBe("the app")
    expect(fetchMock).not.toHaveBeenCalled()
    expect(sdk.routeAgentRequest).not.toHaveBeenCalled()
  })
})
