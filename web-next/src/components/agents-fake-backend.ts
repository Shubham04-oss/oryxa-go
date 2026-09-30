// Test helper, never imported by the app: an in-memory agents service
// behind a mocked `fetch`, so the tests of the shell, the primary chat, the
// desk and Requests run the real lib/agents clients. It keeps the
// rules of agents/primary/internal/httpserver that the UI depends on: strict
// bodies and organization-wide agent names. It answers the member's agents,
// their request inbox, and an empty list of the primary agent's threads.
import type { AgentRequest } from "@/lib/agents/agent-requests"
import type { ManagedAgent } from "@/lib/agents/managed-agents"

export const ORG = "org-1"
export const VIEWER = "user-member"
const NOW = "2026-09-20T10:00:00Z"

export function agent(
  fields: Partial<ManagedAgent> & Pick<ManagedAgent, "id" | "name">
): ManagedAgent {
  return {
    organization_id: ORG,
    owner_user_id: VIEWER,
    kind: "specialist",
    description: "",
    instructions: "Do the work.",
    status: "active",
    revision: 1,
    created_at: NOW,
    updated_at: NOW,
    ...fields,
  }
}

export const primary = agent({
  id: "agt_primary",
  name: "Leo",
  kind: "primary",
  description: "Your primary Oryxa agent.",
  instructions: "",
})

export function request(
  fields: Partial<AgentRequest> & Pick<AgentRequest, "id" | "status">
): AgentRequest {
  return {
    organization_id: ORG,
    requester: {
      user_id: "user-owner",
      agent_id: "agt_owner",
      agent_name: "Nia",
    },
    target: { user_id: VIEWER, agent_id: primary.id, agent_name: primary.name },
    purpose: "Quarterly numbers",
    task: "Share the pipeline total.",
    context: [],
    requested_tools: [],
    requested_permissions: [],
    revision: 1,
    expires_at: "2999-01-01T00:00:00Z",
    created_at: NOW,
    updated_at: NOW,
    ...fields,
  }
}

type Seed = {
  agents?: ManagedAgent[]
  inbox?: AgentRequest[]
}

type Call = { method: string; path: string; body?: unknown }

function json(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  })
}

function refuse(status: number, code: string, message: string) {
  return json({ error: { code, message } }, status)
}

// DisallowUnknownFields: a field the handler does not read is a 400.
function unknownField(body: unknown, allowed: readonly string[]) {
  if (!body || typeof body !== "object") return undefined
  return Object.keys(body).find((key) => !allowed.includes(key))
}

export function fakeAgentsBackend(seed: Seed = {}) {
  const state = {
    agents: seed.agents ?? [primary],
    inbox: seed.inbox ?? [],
  }
  const calls: Call[] = []
  const unavailable = new Set<string>()

  function nameTaken(name: string, except?: string) {
    return state.agents.some(
      (entry) =>
        entry.id !== except && entry.name.toLowerCase() === name.toLowerCase()
    )
  }

  async function route(method: string, path: string, body: unknown) {
    const prefix = `/v1/organizations/${ORG}/`
    if (!path.startsWith(prefix)) return refuse(404, "not_found", "not found")
    const [resource, id, sub] = path
      .slice(prefix.length)
      .split("?")[0]
      .split("/")
      .map(decodeURIComponent)
    const input = (body ?? {}) as Record<string, unknown>

    if (unavailable.has(resource)) {
      const code =
        resource === "agent-requests"
          ? "interactions_unavailable"
          : "agents_unavailable"
      return refuse(503, code, `${resource} are not configured`)
    }

    if (resource === "agents") {
      if (method === "GET" && !id) return json({ agents: state.agents })
      const found = state.agents.find((entry) => entry.id === id)
      if (!found) return refuse(404, "not_found", "agent not found")
      if (method === "GET" && !sub) return json(found)
      if (method === "PATCH") {
        const extra = unknownField(body, [
          "name",
          "description",
          "instructions",
          "status",
        ])
        if (extra)
          return refuse(400, "invalid_request", `unknown field "${extra}"`)
        if (Object.keys(input).length === 0)
          return refuse(
            400,
            "invalid_request",
            "send at least one field to change"
          )
        if (typeof input.name === "string" && nameTaken(input.name, found.id))
          return refuse(409, "name_conflict", "agent name is already in use")
        const updated = {
          ...found,
          ...(input as Partial<ManagedAgent>),
          revision: found.revision + 1,
          updated_at: "2026-09-21T10:00:00Z",
        }
        state.agents = state.agents.map((entry) =>
          entry.id === id ? updated : entry
        )
        return json(updated)
      }
    }

    if (resource === "agent-requests" && method === "GET" && id === "inbox")
      return json({ requests: state.inbox })

    // The primary agent's conversations, for its chat in the center.
    if (resource === "threads" && method === "GET" && !id)
      return json({ threads: [] })

    return refuse(404, "not_found", "not found")
  }

  const fetch = async (url: string, init: RequestInit = {}) => {
    const method = init.method ?? "GET"
    const body =
      typeof init.body === "string" ? JSON.parse(init.body) : undefined
    const path = url.replace(/^https?:\/\/[^/]+/, "")
    calls.push({ method, path, body })
    // Reading the session, as the chats do to refresh an expired cookie.
    if (path === "/v1/auth/me") {
      return json({
        user: { id: VIEWER, email: "member@example.com" },
        organizations: [],
      })
    }
    return route(method, path, body)
  }

  return {
    state,
    calls,
    fetch,
    /** Every call but the reads, as `METHOD path` with its body. */
    writes: () => calls.filter((call) => call.method !== "GET"),
    /** Makes a resource answer 503 as a server without it would. */
    unavailable(resource: string) {
      unavailable.add(resource)
    },
  }
}

export type FakeAgentsBackend = ReturnType<typeof fakeAgentsBackend>
