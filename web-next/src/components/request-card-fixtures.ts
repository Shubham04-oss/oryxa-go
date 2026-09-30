// Requests and a fake Go for the tests of the request card, the Requests view
// and Slack's Activity: one Slack ask between two members, and the routes
// they read and write, over a stubbed `fetch`.
import { vi } from "vitest"
import type {
  AgentActivityEntry,
  AgentLease,
  AgentRequest,
} from "@/lib/agents/agent-requests"
import type { Reader } from "./request-card-format"

export const ORG = "org-1"
/** The member a request is addressed to. */
export const OWNER = "user-ada"
/** The member who asked, in Slack. */
export const ASKER = "user-raya"

export const members = [
  {
    organization_id: ORG,
    user_id: OWNER,
    email: "ada@example.com",
    display_name: "Ada Lovelace",
    role: "member",
    joined_at: "2026-09-01T00:00:00Z",
  },
  {
    organization_id: ORG,
    user_id: ASKER,
    email: "raya@example.com",
    display_name: "Raya Patel",
    role: "member",
    joined_at: "2026-09-01T00:00:00Z",
  },
]

/** A fixed "now" for wording that depends on the time. */
export const NOW = Date.parse("2026-09-29T12:30:00Z")

/** A Slack ask to Ada from Raya, pending: what the Slack app files for the member who mentioned it. */
export function slackRequest(
  overrides: Partial<AgentRequest> = {}
): AgentRequest {
  return {
    id: "irq_slack",
    organization_id: ORG,
    approval: "owner",
    requester: { user_id: ASKER, agent_id: "agt_nia", agent_name: "Nia" },
    target: {
      user_id: OWNER,
      agent_id: "agt_krude",
      agent_name: "Krude",
      user_email: "ada@example.com",
      user_display_name: "Ada Lovelace",
    },
    purpose: "Named in a message a member sent in Slack.",
    task: "What is the plan for the EU launch?",
    context: [],
    requested_tools: ["knowledge"],
    tools_judged_by: "requester",
    requested_permissions: ["consent:summary", "consent:access"],
    status: "pending",
    origin: "slack",
    ask_id: "iak_1",
    revision: 1,
    expires_at: "2026-09-30T12:00:00Z",
    created_at: "2026-09-29T12:00:00Z",
    updated_at: "2026-09-29T12:00:00Z",
    ...overrides,
  }
}

/** The same ask once Ada chose `consent` in Slack. */
export function approvedRequest(
  consent: "summary" | "access" = "summary",
  overrides: Partial<AgentRequest> = {}
): AgentRequest {
  return slackRequest({
    status: "approved",
    revision: 2,
    decision: {
      decision: "approved",
      message: "Approved.",
      allowed_tools: ["knowledge"],
      granted_permissions:
        consent === "access"
          ? ["consent:summary", "consent:access"]
          : ["consent:summary"],
      decided_at: "2026-09-29T12:02:00Z",
      decided_by: { kind: "member", user_id: OWNER, surface: "slack" },
      consent,
    },
    ...overrides,
  })
}

/** An ordinary request an agent filed: no consent choices, and decided with Approve or Decline. */
export function agentRequest(
  overrides: Partial<AgentRequest> = {}
): AgentRequest {
  return slackRequest({
    id: "irq_agent",
    origin: "agent",
    ask_id: undefined,
    purpose: "Architecture",
    task: "Explain the service boundary.",
    requested_tools: ["knowledge"],
    requested_permissions: [],
    ...overrides,
  })
}

/** An answer, as a request's result holds it. */
export function answer(
  summary: string,
  content = "",
  status: "completed" | "failed" = "completed"
) {
  return {
    status,
    summary,
    content,
    tools_used: ["knowledge"],
    artifacts: [],
    completed_at: "2026-09-29T12:10:00Z",
  }
}

export const lease: AgentLease = {
  id: "ils_1",
  organization_id: ORG,
  asker_user_id: ASKER,
  owner_user_id: OWNER,
  channel_id: "C123",
  thread_ts: "1727600000.000100",
  request_id: "irq_slack",
  created_at: "2026-09-29T12:02:00Z",
  expires_at: "2026-09-29T13:02:00Z",
}

/** The people a card names, and the clock it reads. */
export function reader(
  viewerId: string,
  overrides: Partial<Reader> = {}
): Reader {
  const names = new Map(
    members.map((member) => [member.user_id, member.display_name])
  )
  return { viewerId, nameOf: (id) => names.get(id), now: NOW, ...overrides }
}

// ---------------------------------------------------------------------------
// A fake Go
// ---------------------------------------------------------------------------

export function json(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  })
}

export function failure(status: number, code: string, message = code) {
  return json({ error: { code, message } }, status)
}

export type Call = { method: string; path: string; body?: unknown }

type Answer = Response | object | undefined

export type Route = (call: Call, url: URL) => Answer | Promise<Answer>

/**
 * Stubs `fetch` with routes keyed "METHOD /path" (no query string). A route
 * answers a `Response`, or a plain object, which is sent as JSON with a 200.
 * A call no route knows is a 404, so a test that misses one fails visibly.
 * The members list is answered unless a route says otherwise.
 */
export function fakeGo(routes: Record<string, Route>) {
  const calls: Call[] = []
  const table: Partial<Record<string, Route>> = {
    [`GET /v1/organizations/${ORG}/members`]: () => ({ members }),
    ...routes,
  }
  const fetchMock = vi.fn(async (input: string, init: RequestInit = {}) => {
    const url = new URL(input, "http://localhost")
    const method = init.method ?? "GET"
    const call: Call = {
      method,
      path: url.pathname,
      body: init.body ? JSON.parse(String(init.body)) : undefined,
    }
    calls.push(call)
    const answered = await table[`${method} ${url.pathname}`]?.(call, url)
    if (answered instanceof Response) return answered
    if (answered !== undefined) return json(answered)
    return failure(404, "not_found", "resource not found")
  })
  vi.stubGlobal("fetch", fetchMock)
  return {
    calls,
    /** The calls to routes other than the members list, as "METHOD path". */
    sent: () =>
      calls
        .filter((call) => !call.path.endsWith("/members"))
        .map((call) => `${call.method} ${call.path}`),
    count: (method: string, path: string) =>
      calls.filter((call) => call.method === method && call.path === path)
        .length,
  }
}

export const inboxPath = `/v1/organizations/${ORG}/agent-requests/inbox`
export const sentPath = `/v1/organizations/${ORG}/agent-requests/sent`
export const activityPath = `/v1/organizations/${ORG}/agent-requests/activity`
export const requestPath = (id: string) =>
  `/v1/organizations/${ORG}/agent-requests/${id}`

export function activityEntry(
  overrides: Partial<AgentActivityEntry> & Pick<AgentActivityEntry, "kind">
): AgentActivityEntry {
  return {
    at: "2026-09-29T12:00:00Z",
    role: "owner",
    with_user_id: ASKER,
    request_id: "irq_slack",
    ask_id: "iak_1",
    ...overrides,
  }
}
