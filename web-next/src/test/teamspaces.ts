// Test data shaped as the agents service answers (agents/primary/internal/
// teamspaces and rooms), and a routed `fetch` stand-in, for the Team mode
// tests.
import { vi } from "vitest"
import type { RoomMessage, RoomSeat } from "@/lib/rooms/client"
import type {
  Teamspace,
  TeamspaceContribution,
  TeamspaceEntry,
  Whisperer,
} from "@/lib/teamspaces/client"

export const org = "org_sim"
export const viewerId = "user-member"

export function json(body: unknown, status = 200): Response {
  return new Response(status === 204 ? null : JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  })
}

export function apiError(status: number, code: string, message: string) {
  return json({ error: { code, message } }, status)
}

export function whisperer(
  userId: string,
  name: string,
  agentId: string,
  agentName: string
): Whisperer {
  return {
    user_id: userId,
    user_display_name: name,
    agent_id: agentId,
    agent_name: agentName,
    mode: "whisperer",
  }
}

export const memberWhisperer = whisperer(viewerId, "Member", "agt_leo", "Leo")
export const adaWhisperer = whisperer("user-ada", "Ada", "agt_juno", "Juno")

export function contribution(
  overrides: Partial<TeamspaceContribution> = {}
): TeamspaceContribution {
  return {
    agent_id: "agt_pricing",
    owner_user_id: "user-ada",
    name: "Pricing",
    description: "Checks prices",
    status: "active",
    role: "Checks the launch pricing",
    delegation_mode: "request",
    position: 0,
    created_at: "2026-09-20T09:00:00Z",
    ...overrides,
  }
}

export function teamspace(overrides: Partial<Teamspace> = {}): Teamspace {
  return {
    id: "tsp_launch",
    organization_id: org,
    team_id: "team_launch",
    team_name: "Launch",
    name: "Launch room",
    description: "Plans, owners and updates for the next release.",
    created_by_user_id: viewerId,
    organization_agent: {
      id: "oag_1",
      organization_id: org,
      name: "Simulated Org Agent",
      instructions: "Keep the organization's context.",
      status: "active",
      revision: 1,
      can_manage: false,
      created_at: "2026-09-01T09:00:00Z",
      updated_at: "2026-09-01T09:00:00Z",
    },
    whisperers: [memberWhisperer, adaWhisperer],
    status: "active",
    revision: 1,
    contributions: [],
    can_manage: true,
    is_member: true,
    created_at: "2026-09-20T09:00:00Z",
    updated_at: "2026-09-20T09:00:00Z",
    ...overrides,
  }
}

/** The seats the room derives from `space`, as rooms.seatsOf does. */
export function seatsFor(
  space: Teamspace,
  present: readonly string[] = [viewerId]
): RoomSeat[] {
  const seats: RoomSeat[] = []
  for (const item of space.whisperers) {
    const name = item.user_display_name ?? "Someone"
    seats.push({
      handle: name.toLowerCase(),
      kind: "person",
      user_id: item.user_id,
      display_name: name,
      present: present.includes(item.user_id),
    })
    seats.push({
      handle: item.agent_name.toLowerCase(),
      kind: "agent",
      user_id: item.user_id,
      agent_id: item.agent_id,
      agent_name: item.agent_name,
      speaks_for: item.user_id,
      display_name: item.agent_name,
      role: `${name}'s own agent`,
      present: false,
    })
  }
  for (const item of space.contributions) {
    if (item.status !== "active") continue
    seats.push({
      handle: item.name.toLowerCase(),
      kind: "agent",
      user_id: item.owner_user_id,
      agent_id: item.agent_id,
      agent_name: item.name,
      display_name: item.name,
      role: item.role,
      present: false,
    })
  }
  return seats
}

export function roomMessage(
  seq: number,
  author: RoomSeat,
  body: string
): RoomMessage {
  return {
    id: `rmg_${seq}`,
    room_id: "tsp_launch",
    seq,
    author_handle: author.handle,
    author_kind: author.kind,
    author_user_id: author.user_id,
    ...(author.agent_id ? { author_agent_id: author.agent_id } : {}),
    body,
    created_at: "2026-09-23T09:1" + (seq % 10) + ":00Z",
  }
}

export function entry(overrides: Partial<TeamspaceEntry> = {}): TeamspaceEntry {
  return {
    id: "tse_1",
    teamspace_id: "tsp_launch",
    organization_id: org,
    author_user_id: viewerId,
    author_agent_id: "agt_leo",
    author_agent_name: "Leo",
    kind: "decision",
    title: "Ship on Thursday",
    summary: "Ship on Thursday. Support is staffed for the weekend.",
    tools_used: [],
    artifacts: [],
    created_at: "2026-09-23T08:00:00Z",
    ...overrides,
  }
}

export type ApiCall = {
  method: string
  path: string
  body: unknown
}

type Route = (
  call: ApiCall & { url: URL }
) => Response | undefined | Promise<Response | undefined>

/**
 * Stubs `fetch` with `route`, which answers by method and path; an
 * unrouted call answers 404. Returns every call made, in order.
 */
export function mockApi(route: Route) {
  const calls: ApiCall[] = []
  const fetchMock = vi.fn(async (input: string, init: RequestInit = {}) => {
    const url = new URL(input, "https://app.invalid")
    const method = init.method ?? "GET"
    const body =
      typeof init.body === "string"
        ? (JSON.parse(init.body) as unknown)
        : undefined
    const call = { method, path: `${url.pathname}${url.search}`, body }
    calls.push(call)
    const answer = await route({ ...call, url })
    return (
      answer ??
      apiError(404, "not_found", `no route for ${method} ${call.path}`)
    )
  })
  vi.stubGlobal("fetch", fetchMock)
  return { calls, fetchMock }
}

/** A promise the test settles by hand, for a post the room is still answering. */
export function deferred<T>() {
  let resolve!: (value: T) => void
  const promise = new Promise<T>((settle) => {
    resolve = settle
  })
  return { promise, resolve }
}

export const orgPath = `/v1/organizations/${org}`
