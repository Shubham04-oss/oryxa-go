// Test helper, never imported by the app: an in-memory agents service
// behind a mocked `fetch`, so Agents-mode tests run the real lib/agents
// clients. It keeps the rules of agents/primary/internal/httpserver that
// the UI depends on: strict bodies, organization-wide agent names, team
// rules (an active primary, active specialists, 1–16 members), deletes
// refused while in use, and a team post that answers only after the
// agents have (it stores the member's line first).
import type { AgentRequest } from "@/lib/agents/agent-requests"
import type { AgentTeam, AgentTeamMessage } from "@/lib/agents/agent-teams"
import type {
  AgentWorkflow,
  WorkflowExecution,
} from "@/lib/agents/agent-workflows"
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

export function team(
  fields: Partial<AgentTeam> & Pick<AgentTeam, "id" | "name">,
  members: Array<{ agent: ManagedAgent; role: string }> = []
): AgentTeam {
  return {
    organization_id: ORG,
    owner_user_id: VIEWER,
    description: "",
    coordinator_agent_id: primary.id,
    coordinator_name: primary.name,
    status: "active",
    revision: 1,
    created_at: NOW,
    updated_at: NOW,
    members: members.map((member, position) => ({
      agent_id: member.agent.id,
      name: member.agent.name,
      description: member.agent.description,
      status: member.agent.status,
      role: member.role,
      position,
    })),
    ...fields,
  }
}

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
  teams?: AgentTeam[]
  messages?: Record<string, AgentTeamMessage[]>
  workflows?: AgentWorkflow[]
  executions?: Record<string, WorkflowExecution[]>
  inbox?: AgentRequest[]
}

type Call = { method: string; path: string; body?: unknown }
type Failure = { status: number; code: string; message: string }

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
    teams: seed.teams ?? [],
    messages: new Map(Object.entries(seed.messages ?? {})),
    workflows: seed.workflows ?? [],
    executions: new Map(Object.entries(seed.executions ?? {})),
    inbox: seed.inbox ?? [],
  }
  const calls: Call[] = []
  const failures: Array<{
    method: string
    pattern: RegExp
    failure: Failure
  }> = []
  const unavailable = new Set<string>()
  let gate: Promise<void> | undefined
  let next = 1

  function hydrate(stored: AgentTeam): AgentTeam {
    return {
      ...stored,
      coordinator_name:
        state.agents.find((entry) => entry.id === stored.coordinator_agent_id)
          ?.name ?? stored.coordinator_name,
      members: stored.members.map((member) => {
        const current = state.agents.find(
          (entry) => entry.id === member.agent_id
        )
        return current
          ? {
              ...member,
              name: current.name,
              description: current.description,
              status: current.status,
            }
          : member
      }),
    }
  }

  function nameTaken(name: string, except?: string) {
    return state.agents.some(
      (entry) =>
        entry.id !== except && entry.name.toLowerCase() === name.toLowerCase()
    )
  }

  function resolveMembers(members: Array<{ agent_id: string; role: string }>) {
    if (members.length === 0)
      return "invalid agent team: a team needs at least one specialist"
    if (members.length > 16)
      return "invalid agent team: a team holds at most 16 specialists"
    for (const member of members) {
      const found = state.agents.find((entry) => entry.id === member.agent_id)
      if (!found)
        return `invalid agent team: agent "${member.agent_id}" does not belong to this member`
      if (found.kind !== "specialist")
        return `invalid agent team: team member "${member.agent_id}" must be a specialist`
      if (found.status !== "active")
        return `invalid agent team: team member "${member.agent_id}" is disabled`
      if (!member.role.trim())
        return "invalid agent team: every team member needs a role"
    }
    return undefined
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
        resource === "agent-teams"
          ? "agent_teams_unavailable"
          : resource === "agent-workflows"
            ? "agent_workflows_unavailable"
            : resource === "agent-requests"
              ? "interactions_unavailable"
              : "agents_unavailable"
      return refuse(503, code, `${resource} are not configured`)
    }

    if (resource === "agents") {
      if (method === "GET" && !id) return json({ agents: state.agents })
      if (method === "POST" && !id) {
        const extra = unknownField(body, [
          "name",
          "description",
          "instructions",
        ])
        if (extra)
          return refuse(400, "invalid_request", `unknown field "${extra}"`)
        const name = String(input.name ?? "").trim()
        if (!name) return refuse(400, "invalid_request", "name is required")
        if (!String(input.instructions ?? "").trim())
          return refuse(400, "invalid_request", "instructions are required")
        if (nameTaken(name))
          return refuse(409, "name_conflict", "agent name is already in use")
        const created = agent({
          id: `agt_new_${next++}`,
          name,
          description: String(input.description ?? ""),
          instructions: String(input.instructions),
        })
        state.agents = [...state.agents, created]
        return json(created, 201)
      }
      const found = state.agents.find((entry) => entry.id === id)
      if (!found) return refuse(404, "not_found", "agent not found")
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
      if (method === "DELETE") {
        if (found.kind === "primary")
          return refuse(
            400,
            "invalid_request",
            "the primary agent cannot be deleted"
          )
        if (
          state.teams.some((entry) =>
            entry.members.some((member) => member.agent_id === id)
          )
        )
          return refuse(
            409,
            "agent_in_use",
            "agent belongs to an agent team: remove the specialist from its teams and teamspaces first"
          )
        state.agents = state.agents.filter((entry) => entry.id !== id)
        return new Response(null, { status: 204 })
      }
    }

    if (resource === "agent-teams") {
      if (method === "GET" && !id)
        return json({ teams: state.teams.map(hydrate) })
      if (method === "POST" && !id) {
        const extra = unknownField(body, ["name", "description", "members"])
        if (extra)
          return refuse(400, "invalid_request", `unknown field "${extra}"`)
        const coordinator = state.agents.find(
          (entry) => entry.kind === "primary" && entry.status === "active"
        )
        if (!coordinator)
          return refuse(
            400,
            "invalid_request",
            "invalid agent team: an active primary agent is required"
          )
        const members = (input.members ?? []) as Array<{
          agent_id: string
          role: string
        }>
        const invalid = resolveMembers(members)
        if (invalid) return refuse(400, "invalid_request", invalid)
        const name = String(input.name ?? "").trim()
        if (
          state.teams.some(
            (entry) => entry.name.toLowerCase() === name.toLowerCase()
          )
        )
          return refuse(
            409,
            "name_conflict",
            "agent team name is already in use"
          )
        const created = team(
          {
            id: `atm_new_${next++}`,
            name,
            description: String(input.description ?? ""),
          },
          members.map((member) => ({
            agent: state.agents.find((entry) => entry.id === member.agent_id)!,
            role: member.role,
          }))
        )
        state.teams = [...state.teams, created]
        return json(hydrate(created), 201)
      }
      const found = state.teams.find((entry) => entry.id === id)
      if (!found) return refuse(404, "not_found", "agent team not found")
      if (sub === "messages" && method === "GET")
        return json({ messages: state.messages.get(found.id) ?? [] })
      if (sub === "messages" && method === "POST") {
        const extra = unknownField(body, ["body"])
        if (extra)
          return refuse(400, "invalid_request", `unknown field "${extra}"`)
        if (found.status !== "active")
          return refuse(409, "team_disabled", "agent team is disabled")
        const held = state.messages.get(found.id) ?? []
        const said: AgentTeamMessage = {
          id: `msg_${next++}`,
          team_id: found.id,
          seq: (held.at(-1)?.seq ?? 0) + 1,
          kind: "member",
          author_name: "You",
          body: String(input.body).trim(),
          created_at: NOW,
        }
        state.messages.set(found.id, [...held, said])
        await gate
        const reply: AgentTeamMessage = {
          id: `msg_${next++}`,
          team_id: found.id,
          seq: said.seq + 1,
          kind: "agent",
          author_name: found.coordinator_name,
          author_agent_id: found.coordinator_agent_id,
          body: `On it: **${said.body}**`,
          in_reply_to_seq: said.seq,
          created_at: NOW,
        }
        state.messages.set(found.id, [
          ...(state.messages.get(found.id) ?? []),
          reply,
        ])
        return json({ messages: [said, reply] }, 201)
      }
      if (method === "PATCH") {
        const extra = unknownField(body, [
          "name",
          "description",
          "status",
          "members",
        ])
        if (extra)
          return refuse(400, "invalid_request", `unknown field "${extra}"`)
        let updated = { ...found, revision: found.revision + 1 }
        if (typeof input.name === "string") {
          const name = input.name
          if (
            state.teams.some(
              (entry) =>
                entry.id !== found.id &&
                entry.name.toLowerCase() === name.toLowerCase()
            )
          )
            return refuse(
              409,
              "name_conflict",
              "agent team name is already in use"
            )
          updated = { ...updated, name }
        }
        if (typeof input.description === "string")
          updated = { ...updated, description: input.description }
        if (input.status === "active" || input.status === "disabled")
          updated = { ...updated, status: input.status }
        if (input.members) {
          const members = input.members as Array<{
            agent_id: string
            role: string
          }>
          const invalid = resolveMembers(members)
          if (invalid) return refuse(400, "invalid_request", invalid)
          updated = {
            ...updated,
            members: team(
              { id: found.id, name: found.name },
              members.map((member) => ({
                agent: state.agents.find(
                  (entry) => entry.id === member.agent_id
                )!,
                role: member.role,
              }))
            ).members,
          }
        }
        state.teams = state.teams.map((entry) =>
          entry.id === id ? updated : entry
        )
        return json(hydrate(updated))
      }
      if (method === "DELETE") {
        if (state.workflows.some((entry) => entry.team_id === id))
          return refuse(
            409,
            "team_in_use",
            "agent team is in use: delete the workflows that run this team first"
          )
        state.teams = state.teams.filter((entry) => entry.id !== id)
        return new Response(null, { status: 204 })
      }
    }

    if (resource === "agent-workflows") {
      if (method === "GET" && !id) return json({ workflows: state.workflows })
      if (method === "GET" && sub === "executions")
        return json({ executions: state.executions.get(id) ?? [] })
    }

    if (resource === "agent-requests" && method === "GET" && id === "inbox")
      return json({ requests: state.inbox })

    return refuse(404, "not_found", "not found")
  }

  const fetch = async (url: string, init: RequestInit = {}) => {
    const method = init.method ?? "GET"
    const body =
      typeof init.body === "string" ? JSON.parse(init.body) : undefined
    const path = url.replace(/^https?:\/\/[^/]+/, "")
    calls.push({ method, path, body })
    const failure = failures.findIndex(
      (entry) => entry.method === method && entry.pattern.test(path)
    )
    if (failure >= 0) {
      const [{ failure: answer }] = failures.splice(failure, 1)
      return refuse(answer.status, answer.code, answer.message)
    }
    return route(method, path, body)
  }

  return {
    state,
    calls,
    fetch,
    /** Every call but the reads, as `METHOD path` with its body. */
    writes: () => calls.filter((call) => call.method !== "GET"),
    /** The next matching call answers with this refusal instead. */
    failNext(method: string, pattern: RegExp, failure: Failure) {
      failures.push({ method, pattern, failure })
    },
    /** Makes a resource answer 503 as a server without it would. */
    unavailable(resource: string) {
      unavailable.add(resource)
    },
    /** Team posts store the member's line, then wait until released. */
    holdPosts() {
      let release = () => {}
      gate = new Promise<void>((resolve) => {
        release = resolve
      })
      return () => {
        gate = undefined
        release()
      }
    },
  }
}

export type FakeAgentsBackend = ReturnType<typeof fakeAgentsBackend>
