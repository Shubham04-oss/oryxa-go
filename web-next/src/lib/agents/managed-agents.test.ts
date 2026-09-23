import { afterEach, describe, expect, it, vi } from "vitest"
import { ApiError } from "@/lib/api/client"
import {
  createAgent,
  deleteAgent,
  getAgent,
  listAgents,
  updateAgent,
} from "./managed-agents"
import type { CreateAgentInput, ManagedAgent } from "./managed-agents"

function json(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  })
}

function failure(status: number, code: string, message: string) {
  return json({ error: { code, message } }, status)
}

function stubFetch(respond: (url: string, init: RequestInit) => Response) {
  const fetchMock = vi.fn(async (url: string, init: RequestInit) =>
    respond(url, init)
  )
  vi.stubGlobal("fetch", fetchMock)
  return fetchMock
}

function sent(fetchMock: ReturnType<typeof stubFetch>) {
  return fetchMock.mock.calls.map(([url, init]) => [
    init.method,
    url,
    init.body,
  ])
}

const specialist: ManagedAgent = {
  id: "agt_sales",
  organization_id: "org-1",
  owner_user_id: "user-1",
  kind: "specialist",
  name: "Sales",
  description: "Own pipeline",
  instructions: "Review CRM.",
  status: "active",
  revision: 1,
  created_at: "2026-09-23T00:00:00Z",
  updated_at: "2026-09-23T00:00:00Z",
}

afterEach(() => vi.unstubAllGlobals())

describe("managed agents API", () => {
  it("lists the caller's agents in the selected organisation, encoded", async () => {
    const fetchMock = stubFetch(() => json({ agents: [specialist] }))

    expect(await listAgents("org/one")).toEqual([specialist])
    expect(fetchMock).toHaveBeenCalledWith(
      "/v1/organizations/org%2Fone/agents",
      expect.objectContaining({ method: "GET", credentials: "include" })
    )
  })

  it("reads one agent by id", async () => {
    const fetchMock = stubFetch(() => json(specialist))

    expect(await getAgent("org-1", "agt 1")).toEqual(specialist)
    expect(sent(fetchMock)).toEqual([
      ["GET", "/v1/organizations/org-1/agents/agt%201", undefined],
    ])
  })

  it("creates a specialist with only the fields the handler reads", async () => {
    const fetchMock = stubFetch(() => json(specialist, 201))

    // A page handing over a wider object must not send its extra fields:
    // the handler decodes strictly and would answer 400.
    const wider = {
      name: "Sales",
      description: "Own pipeline",
      instructions: "Review CRM.",
      kind: "specialist",
    } as CreateAgentInput
    expect(await createAgent("org-1", wider)).toEqual(specialist)
    await createAgent("org-1", { name: "Research", instructions: "Cite." })

    expect(sent(fetchMock)).toEqual([
      [
        "POST",
        "/v1/organizations/org-1/agents",
        '{"name":"Sales","description":"Own pipeline","instructions":"Review CRM."}',
      ],
      [
        "POST",
        "/v1/organizations/org-1/agents",
        '{"name":"Research","instructions":"Cite."}',
      ],
    ])
    expect(fetchMock.mock.calls[0][1].headers).toEqual({
      "Content-Type": "application/json",
    })
  })

  it("updates only what is set, even when handed a whole agent", async () => {
    const fetchMock = stubFetch(() => json(specialist))

    await updateAgent("org-1", "agt_sales", { status: "disabled" })
    await updateAgent("org-1", "agt_sales", specialist)

    expect(sent(fetchMock)).toEqual([
      [
        "PATCH",
        "/v1/organizations/org-1/agents/agt_sales",
        '{"status":"disabled"}',
      ],
      [
        "PATCH",
        "/v1/organizations/org-1/agents/agt_sales",
        '{"name":"Sales","description":"Own pipeline","instructions":"Review CRM.","status":"active"}',
      ],
    ])
  })

  it("deletes a specialist and resolves on 204", async () => {
    const fetchMock = stubFetch(() => new Response(null, { status: 204 }))

    await expect(deleteAgent("org-1", "agt_sales")).resolves.toBeUndefined()
    expect(sent(fetchMock)).toEqual([
      ["DELETE", "/v1/organizations/org-1/agents/agt_sales", undefined],
    ])
  })

  it("keeps the service's status and code on a refusal", async () => {
    const answers = [
      failure(409, "name_conflict", "agent name is already in use"),
      failure(
        409,
        "agent_in_use",
        "agent belongs to an agent team: remove the specialist from its teams and teamspaces first"
      ),
      failure(400, "invalid_request", "instructions are required"),
    ]
    stubFetch(() => answers.shift() ?? json({}))

    const conflict = await createAgent("org-1", {
      name: "Sales",
      instructions: "x",
    }).catch((error: unknown) => error)
    expect(conflict).toBeInstanceOf(ApiError)
    expect(conflict).toMatchObject({
      status: 409,
      code: "name_conflict",
      message: "agent name is already in use",
    })
    await expect(deleteAgent("org-1", "agt_sales")).rejects.toMatchObject({
      status: 409,
      code: "agent_in_use",
    })
    await expect(
      createAgent("org-1", { name: "Sales", instructions: "" })
    ).rejects.toMatchObject({
      status: 400,
      code: "invalid_request",
      message: "instructions are required",
    })
  })

  it("goes through the shared client: an expired session refreshes once and retries", async () => {
    let reads = 0
    const fetchMock = stubFetch((url) => {
      if (url === "/v1/auth/refresh") return new Response(null, { status: 204 })
      reads += 1
      return reads === 1
        ? failure(401, "unauthorized", "access token is required")
        : json({ agents: [] })
    })

    expect(await listAgents("org-1")).toEqual([])
    expect(fetchMock.mock.calls.map(([url]) => url)).toEqual([
      "/v1/organizations/org-1/agents",
      "/v1/auth/refresh",
      "/v1/organizations/org-1/agents",
    ])
  })
})
