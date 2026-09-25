// A stand-in for the Go API a side conversation works with
// (src/side-conversation-agent.ts): a member's threads and their runs, agent
// requests in a teamspace and their decisions, and the teamspace itself. It
// answers as the member whose cookie a request carries, keeps what it is
// asked, and records every call. Use its `fetch` as the global one.
import { vi } from "vitest"
import type { Item } from "@/lib/threads/client"
import type { SideRoster } from "@/lib/side/protocol"

export const goOrigin = "http://go.test"
export const pageOrigin = "http://localhost:3004"
export const org = "org_sim"
export const teamspaceId = "tsp_launch"

/** A person in the test world: their user id, cookie, name and primary agent. */
export type Person = {
  userId: string
  cookie: string
  name: string
  agentId: string
  agentName: string
}

export const ada: Person = {
  userId: "user-ada",
  cookie: "oryxa_access=ada",
  name: "Ada",
  agentId: "agt_leo",
  agentName: "Leo",
}

export const bo: Person = {
  userId: "user-bo",
  cookie: "oryxa_access=bo",
  name: "Bo",
  agentId: "agt_nia",
  agentName: "Nia",
}

export const cy: Person = {
  userId: "user-cy",
  cookie: "oryxa_access=cy",
  name: "Cy",
  agentId: "agt_juno",
  agentName: "Juno",
}

export const people = [ada, bo, cy]

/** Bo's specialists in the teamspace: one decides for itself, one waits for Bo. */
export const pricing = {
  agentId: "agt_pricing",
  ownerId: bo.userId,
  name: "Pricing",
  role: "Checks the launch pricing",
  delegationMode: "automatic" as const,
}

export const legal = {
  agentId: "agt_legal",
  ownerId: bo.userId,
  name: "Legal",
  role: "Reads contracts",
  delegationMode: "request" as const,
}

/** The roster the Worker hands a socket of the Ada–Bo conversation. */
export function roster(overrides: Partial<SideRoster> = {}): SideRoster {
  return {
    teamspaceName: "Launch room",
    paused: false,
    people: [ada, bo].map((person) => ({
      userId: person.userId,
      displayName: person.name,
      agentId: person.agentId,
      agentName: person.agentName,
    })),
    contributions: [pricing, legal],
    ...overrides,
  }
}

/** Go's teamspace, as `GET …/teamspaces/{id}` answers it. */
export function teamspaceAnswer(
  extra: { contributions?: Array<Record<string, unknown>> } = {}
) {
  return {
    id: teamspaceId,
    organization_id: org,
    team_id: "team_launch",
    team_name: "Launch",
    name: "Launch room",
    status: "active",
    is_member: true,
    whisperers: people.map((person) => ({
      user_id: person.userId,
      user_display_name: person.name,
      agent_id: person.agentId,
      agent_name: person.agentName,
      mode: "whisperer",
    })),
    contributions: [
      ...[pricing, legal].map((agent, position) => ({
        agent_id: agent.agentId,
        owner_user_id: agent.ownerId,
        name: agent.name,
        status: "active",
        role: agent.role,
        delegation_mode: agent.delegationMode,
        position,
        created_at: "2026-09-20T09:00:00Z",
      })),
      ...(extra.contributions ?? []),
    ],
  }
}

export type GoInteraction = {
  id: string
  organization_id: string
  teamspace_id?: string
  approval: "agent" | "owner"
  requester: { user_id: string; agent_id: string; agent_name: string }
  target: { user_id: string; agent_id: string; agent_name: string }
  purpose: string
  task: string
  context: Array<{ label: string; value: string }>
  requested_tools: string[]
  requested_permissions: string[]
  status: string
  decision?: {
    decision: string
    message: string
    allowed_tools: string[]
    granted_permissions: string[]
    decided_at: string
  }
  result?: {
    status: string
    summary: string
    content: string
    tools_used: string[]
    artifacts: string[]
    completed_at: string
  }
  revision: number
  expires_at: string
  created_at: string
  updated_at: string
}

/** One call Go was sent. */
export type GoCall = {
  method: string
  path: string
  search: URLSearchParams
  cookie: string | null
  origin: string | null
  idempotencyKey: string | null
  body: unknown
}

function json(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  })
}

function goError(status: number, code: string, message: string): Response {
  return json({ error: { code, message } }, status)
}

const encoder = new TextEncoder()

/** A run's answer: its items as Go streams them, the response closing after. */
export function runStream(items: Item[]): Response {
  const stream = new ReadableStream<Uint8Array>({
    start(controller) {
      controller.enqueue(encoder.encode(": keep-alive\n\n"))
      for (const item of items) {
        controller.enqueue(
          encoder.encode(
            `event: ${item.type}\ndata: ${JSON.stringify(item)}\n\n`
          )
        )
      }
      controller.close()
    },
  })
  return new Response(stream, {
    headers: { "Content-Type": "text/event-stream", "X-Oryxa-Run": "run_1" },
  })
}

/** A run that answers `text` as `author`, in two deltas and the finished message. */
export function answer(author: string, text: string): Item[] {
  const half = Math.ceil(text.length / 2)
  return [
    { type: "run_started", run_id: "run_1" },
    { type: "message", author: "member", text: "(the prompt)" },
    { type: "delta", author, text: text.slice(0, half) },
    { type: "delta", author, text: text.slice(half) },
    { type: "message", author, text },
    { type: "run_completed", run_id: "run_1" },
  ]
}

export class FakeGo {
  readonly calls: GoCall[] = []
  readonly requests = new Map<string, GoInteraction>()
  /** What each run answers, in turn: items to stream, or a response. */
  readonly runAnswers: Array<Item[] | Response> = []
  /** Cookies Go no longer takes. */
  readonly expired = new Set<string>()
  /** Answers the next call to this `METHOD path` instead, once. */
  readonly overrides = new Map<string, Response>()
  teamspace: Record<string, unknown> = teamspaceAnswer()
  private threads = 0
  private ids = 0
  now = () => new Date().toISOString()

  readonly fetch = vi.fn(
    async (input: URL | string, init: RequestInit = {}) => {
      const url = new URL(String(input))
      const headers = new Headers(init.headers)
      const method = init.method ?? "GET"
      let body: unknown
      if (typeof init.body === "string") body = JSON.parse(init.body)
      this.calls.push({
        method,
        path: url.pathname,
        search: url.searchParams,
        cookie: headers.get("Cookie"),
        origin: headers.get("Origin"),
        idempotencyKey: headers.get("Idempotency-Key"),
        body,
      })
      if (url.origin !== goOrigin)
        return goError(404, "not_found", "Not found.")
      const override = this.overrides.get(`${method} ${url.pathname}`)
      if (override) {
        this.overrides.delete(`${method} ${url.pathname}`)
        return override
      }
      const cookie = headers.get("Cookie") ?? ""
      const caller = people.find((person) => person.cookie === cookie)
      if (!caller || this.expired.has(cookie)) {
        return goError(401, "unauthorized", "access token is required")
      }
      return this.answer(method, url, caller, body)
    }
  )

  /** The calls to a path (and method), in order. */
  callsTo(path: string, method?: string): GoCall[] {
    return this.calls.filter(
      (call) =>
        call.path === path && (method === undefined || call.method === method)
    )
  }

  /** A request made outside the object, as an agent's tool would. */
  file(
    from: Person,
    to: { agentId: string; agentName: string; ownerId: string },
    overrides: Partial<GoInteraction> = {}
  ): GoInteraction {
    this.ids += 1
    const at = this.now()
    const request: GoInteraction = {
      id: `irq_${this.ids}`,
      organization_id: org,
      teamspace_id: teamspaceId,
      approval: "owner",
      requester: {
        user_id: from.userId,
        agent_id: from.agentId,
        agent_name: from.agentName,
      },
      target: {
        user_id: to.ownerId,
        agent_id: to.agentId,
        agent_name: to.agentName,
      },
      purpose: "Asked by an agent",
      task: "Do the thing",
      context: [],
      requested_tools: [],
      requested_permissions: [],
      status: "pending",
      revision: 1,
      expires_at: "2026-09-25T09:00:00.000Z",
      created_at: at,
      updated_at: at,
      ...overrides,
    }
    this.requests.set(request.id, request)
    return request
  }

  /** Go moves a request on, as a decision or a response does. */
  change(id: string, change: Partial<GoInteraction>): GoInteraction {
    const held = this.requests.get(id)
    if (!held) throw new Error(`no request ${id}`)
    const next = {
      ...held,
      ...change,
      revision: held.revision + 1,
      updated_at: this.now(),
    }
    this.requests.set(id, next)
    return next
  }

  /** The target's agent answers an approved request. */
  complete(id: string, summary: string, content = ""): GoInteraction {
    return this.change(id, {
      status: "completed",
      result: {
        status: "completed",
        summary,
        content,
        tools_used: [],
        artifacts: [],
        completed_at: this.now(),
      },
    })
  }

  private answer(
    method: string,
    url: URL,
    caller: Person,
    body: unknown
  ): Response {
    const base = `/v1/organizations/${org}`
    const path = url.pathname
    if (method === "POST" && path === `${base}/threads`) {
      this.threads += 1
      return json(
        {
          id: `thread-${caller.name.toLowerCase()}-${this.threads}`,
          title: "",
        },
        201
      )
    }
    if (method === "POST" && /\/threads\/[^/]+\/runs$/.test(path)) {
      const next = this.runAnswers.shift()
      if (!next) return goError(500, "internal", "no run was expected")
      return next instanceof Response ? next : runStream(next)
    }
    if (method === "GET" && path === `${base}/teamspaces/${teamspaceId}`) {
      return json(this.teamspace)
    }
    if (method === "POST" && path === `${base}/agent-requests`) {
      return this.create(caller, body as Record<string, unknown>)
    }
    if (method === "GET" && path === `${base}/agent-requests`) {
      const teamspace = url.searchParams.get("teamspace_id")
      const withUser = url.searchParams.get("with_user")
      const limit = Number(url.searchParams.get("limit") ?? "20")
      const listed = [...this.requests.values()]
        .filter((request) => {
          const parties = [request.requester.user_id, request.target.user_id]
          return (
            parties.includes(caller.userId) &&
            (!teamspace || request.teamspace_id === teamspace) &&
            (!withUser || parties.includes(withUser))
          )
        })
        .reverse()
        .slice(0, limit)
      return json({ requests: listed })
    }
    const one = new RegExp(`^${base}/agent-requests/([^/]+)(/decision)?$`).exec(
      path
    )
    if (one) {
      const request = this.requests.get(one[1])
      const parties = request
        ? [request.requester.user_id, request.target.user_id]
        : []
      if (!request || !parties.includes(caller.userId)) {
        return goError(404, "not_found", "agent request not found")
      }
      if (method === "GET" && !one[2]) return json(request)
      if (method === "POST" && one[2]) {
        if (request.target.user_id !== caller.userId) {
          return goError(403, "forbidden", "agent request is not available")
        }
        if (request.status !== "pending") {
          return goError(
            409,
            "invalid_state",
            "agent request cannot make that transition"
          )
        }
        const decision = body as { decision: string; message: string }
        return json(
          this.change(request.id, {
            status: decision.decision,
            decision: {
              decision: decision.decision,
              message: decision.message,
              allowed_tools: [],
              granted_permissions: [],
              decided_at: this.now(),
            },
          })
        )
      }
    }
    return goError(404, "not_found", `nothing at ${method} ${path}`)
  }

  private create(caller: Person, body: Record<string, unknown>): Response {
    const agentId = body.target_agent_id
    const person = people.find((each) => each.agentId === agentId)
    const specialist = [pricing, legal].find((each) => each.agentId === agentId)
    if (!person && !specialist) {
      return goError(
        400,
        "invalid_request",
        "that agent has no seat in this teamspace"
      )
    }
    const to = person
      ? {
          agentId: person.agentId,
          agentName: person.agentName,
          ownerId: person.userId,
        }
      : {
          agentId: specialist?.agentId ?? "",
          agentName: specialist?.name ?? "",
          ownerId: specialist?.ownerId ?? "",
        }
    const approval =
      specialist?.delegationMode === "automatic" ? "agent" : "owner"
    const request = this.file(caller, to, {
      approval,
      purpose: String(body.purpose),
      task: String(body.task),
      context: (body.context as GoInteraction["context"] | undefined) ?? [],
    })
    return json(request, 201)
  }
}
