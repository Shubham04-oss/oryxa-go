// A routed `fetch` stand-in for the tests of the pages, and test data shaped
// as the agents service answers for a teamspace (agents/primary/internal/
// teamspaces), as far as the teamspace room's Durable Object reads it: its
// people, their agents and the specialists contributed.
import { beforeEach, vi } from "vitest"
import { resumeSessionAfterSignIn } from "@/lib/api/client"
import type { RoomSeat } from "@/lib/rooms/client"

// Each independent scenario starts authenticated. Keep the same browser
// session across calls inside a test, including after a terminal refresh 401.
beforeEach(resumeSessionAfterSignIn)

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

/** A teammate's primary agent as the teamspace sees it. */
type Whisperer = {
  user_id: string
  user_display_name?: string
  agent_id: string
  agent_name: string
  mode: "whisperer"
}

/** A specialist its owner made available to the teamspace. */
type TeamspaceContribution = {
  agent_id: string
  owner_user_id: string
  name: string
  description?: string
  status: "active" | "disabled"
  role: string
  delegation_mode: "request" | "automatic"
  position: number
  created_at: string
}

type Teamspace = {
  id: string
  organization_id: string
  team_id: string
  team_name: string
  name: string
  status: "active" | "disabled"
  whisperers: Whisperer[]
  contributions: TeamspaceContribution[]
}

function whisperer(
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

const memberWhisperer = whisperer(viewerId, "Member", "agt_leo", "Leo")
const adaWhisperer = whisperer("user-ada", "Ada", "agt_juno", "Juno")

export function teamspace(overrides: Partial<Teamspace> = {}): Teamspace {
  return {
    id: "tsp_launch",
    organization_id: org,
    team_id: "team_launch",
    team_name: "Launch",
    name: "Launch room",
    whisperers: [memberWhisperer, adaWhisperer],
    status: "active",
    contributions: [],
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
