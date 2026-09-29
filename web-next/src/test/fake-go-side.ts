// A stand-in for the Go API a side conversation works with
// (src/side-conversation-agent.ts): a member's threads and their runs (which
// a test may hold open), the answer guard's check of an agent's answer,
// agent requests in a teamspace, their decisions and their owners' reviews of
// answers the answer guard held, and the teamspace itself. It answers as the
// member whose cookie a request carries, showing each what Go shows them,
// keeps what it is asked, and records every call. Like Go, it takes a body
// with the fields it knows and no others, and a call that is given up on
// (its signal aborts) ends. Use its `fetch` as the global one.
import { vi } from "vitest"
import type { Item } from "@/lib/threads/client"
import { HELD_ANSWER_FIELDS } from "@/lib/side/asking"
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
  /** Absent in a test of a Go that knows nothing of tools. */
  requested_tools?: string[]
  tools_judged_by?: string
  requested_permissions: string[]
  status: string
  decision?: {
    decision: string
    message: string
    allowed_tools: string[]
    granted_permissions: string[]
    decided_at: string
    decided_by?: { kind: string; user_id?: string; agent_id?: string }
  }
  result?: GoResult
  guard?: { state: string; kinds: string[] }
  review?: {
    action: string
    decided_by: { kind: string; user_id?: string }
    decided_at: string
    reason?: string
  }
  revision: number
  expires_at: string
  created_at: string
  updated_at: string
}

/** An answer, as Go keeps a result. */
export type GoResult = {
  status: string
  summary: string
  content: string
  tools_used: string[]
  artifacts: string[]
  completed_at: string
}

/** An answer held for its owner's review, as Go shows it to them (`held`). */
export type GoHeld = { result: GoResult; masked_result?: GoResult }

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

/**
 * What the answer guard's check answers (`POST …/answer-guard/check`):
 * `checked` is false when no guardrail is set, and `shareable` holds one
 * text for each one asked, or none at all (null) when nothing is to be
 * shared.
 */
export type GuardVerdict = {
  checked: boolean
  state: "clear" | "masked" | "held"
  kinds: string[]
  shareable: string[] | null
}

/** One check the answer guard was asked, as Go took it. */
export type GuardCheck = {
  texts: string[]
  origin: string
  request: string
  sources: string[]
  context: string[]
  /** Whose cookie asked. */
  caller: Person
}

/** Nothing found: every text may be shared as it was given. */
export function passThrough({ texts }: { texts: string[] }): GuardVerdict {
  return { checked: true, state: "clear", kinds: [], shareable: texts }
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

/**
 * Go's answer to a body that is not an object, or that has a field it does
 * not know (it decodes every body with `DisallowUnknownFields`), or nothing
 * when it is one it takes.
 */
function unknownFields(body: unknown, known: string[]): Response | undefined {
  if (typeof body !== "object" || body === null || Array.isArray(body)) {
    return goError(
      400,
      "invalid_request",
      "invalid request: expected an object"
    )
  }
  const unknown = Object.keys(body).find((field) => !known.includes(field))
  return unknown === undefined
    ? undefined
    : goError(
        400,
        "invalid_request",
        `invalid request: json: unknown field "${unknown}"`
      )
}

/**
 * A run the test holds open, as Go does while an agent works: what `send`
 * writes streams to whoever reads it, and the stream ends when `finish`
 * says so. Queue its `response` on `FakeGo.runAnswers`.
 */
export function openRun(): {
  response: Response
  /** Streams items, as the run goes on. */
  send: (...items: Item[]) => void
  /** Streams `run_completed` and ends the stream. */
  finish: () => void
} {
  let stream!: ReadableStreamDefaultController<Uint8Array>
  const write = (item: Item) =>
    stream.enqueue(
      encoder.encode(`event: ${item.type}\ndata: ${JSON.stringify(item)}\n\n`)
    )
  const response = new Response(
    new ReadableStream<Uint8Array>({
      start(controller) {
        stream = controller
        controller.enqueue(encoder.encode(": keep-alive\n\n"))
      },
    }),
    {
      headers: { "Content-Type": "text/event-stream", "X-Oryxa-Run": "run_1" },
    }
  )
  return {
    response,
    send: (...items) => {
      for (const item of items) write(item)
    },
    finish: () => {
      write({ type: "run_completed", run_id: "run_1" })
      stream.close()
    },
  }
}

/** What Go answers, unless `signal` aborts first: a call given up on rejects with the signal's reason, as a fetch does. */
function unlessAborted<T>(
  pending: T | Promise<T>,
  signal: AbortSignal | null | undefined
): Promise<T> {
  if (!signal) return Promise.resolve(pending)
  return new Promise<T>((resolve, reject) => {
    if (signal.aborted) {
      reject(signal.reason)
      return
    }
    const abort = () => reject(signal.reason)
    signal.addEventListener("abort", abort, { once: true })
    Promise.resolve(pending)
      .then(resolve, reject)
      .finally(() => signal.removeEventListener("abort", abort))
  })
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
  /** What a new request asks to use when its requester names nothing, as Jev would judge it. */
  requestedTools: string[] = []
  /** The checks the answer guard was asked, in order; each is among `calls` too. */
  readonly checks: GuardCheck[] = []
  /**
   * What the answer guard answers a check: nothing found, every text shared
   * as it was given, unless a test sets its own. It answers a verdict, or a
   * response (a refusal, a body that is not JSON), or a promise of either,
   * which a test may keep waiting, or a call may be given up on meanwhile.
   */
  check: (
    check: GuardCheck
  ) => GuardVerdict | Response | Promise<GuardVerdict | Response> = passThrough
  /** The answers held for review, by request: Go shows them to the target's owner alone. */
  readonly held = new Map<string, GoHeld>()
  /** Requests that no longer stand in their teamspace, by id, and why (Go's lapsed words). */
  readonly lapsed = new Map<string, string>()
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
      return await unlessAborted(
        this.answer(method, url, caller, body),
        init.signal
      )
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

  /**
   * The target's agent answers an approved request (a failure, when
   * `status` says so), and the answer guard holds it for the owner's
   * review, as it was and fully masked: a masked version only when it could
   * check it, and the kinds it holds it for.
   */
  hold(
    id: string,
    held: {
      stored: string
      masked?: string
      kinds: string[]
      status?: "completed" | "failed"
    }
  ): GoInteraction {
    const result = (summary: string): GoResult => ({
      status: held.status ?? "completed",
      summary,
      content: "",
      tools_used: [],
      artifacts: [],
      completed_at: this.now(),
    })
    this.held.set(id, {
      result: result(held.stored),
      ...(held.masked !== undefined
        ? { masked_result: result(held.masked) }
        : {}),
    })
    return this.change(id, {
      status: "review",
      guard: { state: "held", kinds: held.kinds },
    })
  }

  private pastExpiry(request: GoInteraction): boolean {
    return Date.parse(request.expires_at) <= Date.parse(this.now())
  }

  /** Oryxa withholds an answer whose request expired while it waited in review (its sweep, at the time it runs). */
  withholdExpired(id: string): GoInteraction {
    this.held.delete(id)
    return this.change(id, {
      status: "withheld",
      review: {
        action: "withhold",
        decided_by: { kind: "system" },
        decided_at: this.now(),
        reason: "expired before review",
      },
    })
  }

  /**
   * A request as `caller` reads it (Go's `viewFor`): its owner's side reads
   * it all, an answer held for them included while it is in review; the
   * requester never reads what is held, and reads what the guard found only
   * beside a result, which they do not read in review or once withheld. An
   * answer still in review past its request's expiry reads as Oryxa's
   * withholding, at the expiry, as the next sweep records it (Go's `view`).
   */
  view(stored: GoInteraction, caller: Person): Record<string, unknown> {
    const request: GoInteraction =
      stored.status === "review" && this.pastExpiry(stored)
        ? {
            ...stored,
            status: "withheld",
            review: {
              action: "withhold",
              decided_by: { kind: "system" },
              decided_at: stored.expires_at,
              reason: "expired before review",
            },
          }
        : stored
    if (request.target.user_id === caller.userId) {
      const held = this.held.get(request.id)
      return request.status === "review" && held
        ? { ...request, [HELD_ANSWER_FIELDS.object]: held }
        : request
    }
    const { result, guard, ...shown } = request
    return request.status === "review" || request.status === "withheld"
      ? shown
      : {
          ...shown,
          ...(result ? { result } : {}),
          ...(guard ? { guard } : {}),
        }
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
  ): Response | Promise<Response> {
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
    if (method === "POST" && path === `${base}/answer-guard/check`) {
      return this.checkAnswer(caller, body)
    }
    if (method === "POST" && /\/threads\/[^/]+\/runs$/.test(path)) {
      const refused = unknownFields(body, [
        "message",
        "confirmations",
        "teamspace_id",
      ])
      if (refused) return refused
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
        .map((request) => this.view(request, caller))
      return json({ requests: listed })
    }
    const one = new RegExp(
      `^${base}/agent-requests/([^/]+)(/decision|/review)?$`
    ).exec(path)
    if (one) {
      const request = this.requests.get(one[1])
      const parties = request
        ? [request.requester.user_id, request.target.user_id]
        : []
      if (!request || !parties.includes(caller.userId)) {
        return goError(404, "not_found", "agent request not found")
      }
      if (method === "GET" && !one[2]) return json(this.view(request, caller))
      if (method === "POST" && one[2]) {
        if (request.target.user_id !== caller.userId) {
          return goError(403, "forbidden", "agent request is not available")
        }
        if (one[2] === "/review") return this.review(request, caller, body)
        if (request.status !== "pending") {
          return goError(
            409,
            "invalid_state",
            "agent request cannot make that transition"
          )
        }
        const decision = body as {
          decision: string
          message: string
          allowed_tools?: string[]
        }
        const allowed = decision.allowed_tools ?? []
        if (
          (decision.decision === "rejected" && allowed.length > 0) ||
          allowed.some(
            (tool) => !(request.requested_tools ?? []).includes(tool)
          )
        ) {
          return goError(
            400,
            "invalid_request",
            "approved tools and permissions must be requested subsets"
          )
        }
        return json(
          this.view(
            this.change(request.id, {
              status: decision.decision,
              decision: {
                decision: decision.decision,
                message: decision.message,
                allowed_tools: allowed,
                granted_permissions: [],
                decided_at: this.now(),
                decided_by: { kind: "member", user_id: caller.userId },
              },
            }),
            caller
          )
        )
      }
    }
    return goError(404, "not_found", `nothing at ${method} ${path}`)
  }

  /**
   * The owner's review of an answer held for them (Go's `ReviewAsOwner`, in
   * its order of checks): shared as it was held, fully masked, or not at
   * all. Shared, the answer completes the request, or fails it when it was
   * a failure.
   */
  private review(
    request: GoInteraction,
    caller: Person,
    body: unknown
  ): Response {
    const action = (body as { action?: unknown }).action
    if (
      action !== "send" &&
      action !== "send_masked" &&
      action !== "withhold"
    ) {
      return goError(
        400,
        "invalid_request",
        "invalid request: action must be send, send_masked or withhold"
      )
    }
    // Past its expiry first: it reads as withheld already.
    if (this.pastExpiry(request)) {
      return goError(409, "request_expired", "agent request expired")
    }
    if (request.status !== "review") {
      return goError(
        409,
        "invalid_state",
        "only an answer held for review is reviewed"
      )
    }
    const held = this.held.get(request.id)
    const delivered =
      action === "send"
        ? held?.result
        : action === "send_masked"
          ? held?.masked_result
          : undefined
    if (action !== "withhold" && !delivered) {
      return goError(
        409,
        "invalid_state",
        "this answer could not be checked, so it has no masked version to send"
      )
    }
    // Sent, the request must still stand in its teamspace: one that no
    // longer does has its answer withheld by Oryxa, saying why.
    const lapsed = this.lapsed.get(request.id)
    if (action !== "withhold" && lapsed) {
      this.held.delete(request.id)
      this.change(request.id, {
        status: "withheld",
        review: {
          action: "withhold",
          decided_by: { kind: "system" },
          decided_at: this.now(),
          reason: `Oryxa withheld this answer because ${lapsed}.`,
        },
      })
      return goError(
        409,
        "request_closed",
        `${lapsed}, so Oryxa withheld the answer`
      )
    }
    this.held.delete(request.id)
    const review = {
      action,
      decided_by: { kind: "member", user_id: caller.userId },
      decided_at: this.now(),
    }
    return json(
      this.view(
        this.change(request.id, {
          status: delivered?.status ?? "withheld",
          review,
          ...(delivered ? { result: delivered } : {}),
        }),
        caller
      )
    )
  }

  /**
   * The answer guard's check, as Go takes it: only `texts` (one to eight,
   * each at most 25,000 characters), `origin` (`owner`, an own agent's
   * answer) and `request` (what was asked, at most 25,000 characters).
   */
  private async checkAnswer(caller: Person, body: unknown): Promise<Response> {
    const invalid = (why: string) =>
      goError(400, "invalid_request", `invalid request: ${why}`)
    const refused = unknownFields(body, [
      "texts",
      "origin",
      "request",
      "sources",
      "context",
    ])
    if (refused) return refused
    const {
      texts,
      origin,
      request,
      sources = [],
      context = [],
    } = body as Record<string, unknown>
    const runes = (text: string) => Array.from(text).length
    if (
      !Array.isArray(texts) ||
      texts.length < 1 ||
      texts.length > 8 ||
      texts.some((text) => typeof text !== "string")
    ) {
      return invalid("texts must be from one to eight texts")
    }
    if ((texts as string[]).some((text) => runes(text) > 25_000)) {
      return invalid("a text is longer than 25,000 characters")
    }
    if (origin !== "owner") return invalid("origin must be owner")
    if (typeof request !== "string" || runes(request) > 25_000) {
      return invalid("request must be a text of at most 25,000 characters")
    }
    if (
      !Array.isArray(sources) ||
      sources.length > 64 ||
      sources.some(
        (source) => typeof source !== "string" || runes(source) > 128
      )
    ) {
      return invalid("sources must be up to 64 names of at most 128 characters")
    }
    if (
      !Array.isArray(context) ||
      context.length > 6 ||
      context.some((line) => typeof line !== "string" || runes(line) > 1_000)
    ) {
      return invalid(
        "context must be up to six lines of at most 1,000 characters"
      )
    }
    const check: GuardCheck = {
      texts: texts as string[],
      origin,
      request,
      sources: sources as string[],
      context: context as string[],
      caller,
    }
    this.checks.push(check)
    const answered = await this.check(check)
    return answered instanceof Response ? answered : json(answered)
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
    const named = body.requested_tools as string[] | undefined
    const request = this.file(caller, to, {
      approval,
      purpose: String(body.purpose),
      task: String(body.task),
      context: (body.context as GoInteraction["context"] | undefined) ?? [],
      requested_tools: named ?? [...this.requestedTools],
      tools_judged_by: named ? "requester" : "jev",
    })
    return json(request, 201)
  }
}
