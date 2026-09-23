import { afterEach, describe, expect, it, vi } from "vitest"
import {
  getOrganizationAgent,
  getOrganizationContext,
  listOrganizationFollowUps,
  updateOrganizationAgent,
} from "./organization-agent"
import type {
  OrganizationAgent,
  UpdateOrganizationAgentInput,
} from "./organization-agent"

function json(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  })
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

const agent: OrganizationAgent = {
  id: "oag_1",
  organization_id: "org-1",
  name: "Atlas",
  instructions: "Synthesize shared work.",
  status: "active",
  revision: 1,
  can_manage: true,
  created_at: "2026-09-23T00:00:00Z",
  updated_at: "2026-09-23T00:00:00Z",
}

afterEach(() => vi.unstubAllGlobals())

describe("organization agent API", () => {
  it("reads and changes the organization agent", async () => {
    const fetchMock = stubFetch(() => json(agent))

    expect(await getOrganizationAgent("org / one")).toEqual(agent)
    await updateOrganizationAgent("org / one", {
      name: "Atlas",
      status: "disabled",
      can_manage: true,
    } as UpdateOrganizationAgentInput)

    expect(sent(fetchMock)).toEqual([
      [
        "GET",
        "/v1/organizations/org%20%2F%20one/organization-agent",
        undefined,
      ],
      [
        "PATCH",
        "/v1/organizations/org%20%2F%20one/organization-agent",
        '{"name":"Atlas","status":"disabled"}',
      ],
    ])
  })

  it("reads the synthesis and its follow-ups", async () => {
    const empty = {
      organization_id: "org-1",
      agent_id: "",
      revision: 0,
      summary: "",
      decisions: [],
      risks: [],
      open_questions: [],
    }
    const followUp = {
      id: "ofu_1",
      organization_id: "org-1",
      synthesis_revision: 2,
      source_entry_id: "tse_1",
      teamspace_id: "tsp_1",
      task: "Check the launch date",
      rationale: "Two entries disagree",
      status: "proposed",
      created_at: "2026-09-23T00:00:00Z",
    }
    const fetchMock = stubFetch((url) =>
      url.includes("/context") ? json(empty) : json({ follow_ups: [followUp] })
    )

    expect(await getOrganizationContext("org-1")).toEqual(empty)
    expect(await listOrganizationFollowUps("org-1", { limit: 20 })).toEqual([
      followUp,
    ])
    await listOrganizationFollowUps("org-1")

    expect(fetchMock.mock.calls.map(([url]) => url)).toEqual([
      "/v1/organizations/org-1/organization-agent/context",
      "/v1/organizations/org-1/organization-agent/follow-ups?limit=20",
      "/v1/organizations/org-1/organization-agent/follow-ups",
    ])
  })

  it("refuses a member who is not an owner or admin", async () => {
    stubFetch(() =>
      json(
        {
          error: {
            code: "forbidden",
            message: "teamspace is not available to this member",
          },
        },
        403
      )
    )

    await expect(
      updateOrganizationAgent("org-1", { name: "Atlas" })
    ).rejects.toMatchObject({ status: 403, code: "forbidden" })
  })
})
