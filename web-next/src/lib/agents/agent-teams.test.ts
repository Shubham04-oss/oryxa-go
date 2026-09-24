import { afterEach, describe, expect, it, vi } from "vitest"
import {
  createAgentTeam,
  deleteAgentTeam,
  getAgentTeam,
  getAgentTeamThreads,
  listAgentTeamMessages,
  listAgentTeams,
  mergeAgentTeamMessages,
  postAgentTeamMessage,
  updateAgentTeam,
} from "./agent-teams"
import type { AgentTeam, AgentTeamMessage } from "./agent-teams"

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

const team: AgentTeam = {
  id: "atm_launch",
  organization_id: "org-1",
  owner_user_id: "user-1",
  name: "Launch",
  description: "Ship the launch",
  coordinator_agent_id: "agt_primary",
  coordinator_name: "Nia",
  status: "active",
  revision: 2,
  members: [
    {
      agent_id: "agt_research",
      name: "Research",
      description: "Checks claims",
      status: "active",
      role: "Verify claims",
      position: 0,
    },
  ],
  created_at: "2026-09-23T00:00:00Z",
  updated_at: "2026-09-23T00:00:00Z",
}

function message(id: string, seq: number, body = id): AgentTeamMessage {
  return {
    id,
    team_id: "atm_launch",
    seq,
    kind: "agent",
    author_name: "Nia",
    body,
    created_at: "2026-09-23T00:00:00Z",
  }
}

afterEach(() => vi.unstubAllGlobals())

describe("agent teams API", () => {
  it("lists, reads, creates, updates and deletes the caller's teams", async () => {
    const fetchMock = stubFetch((url, init) => {
      if (init.method === "DELETE") return new Response(null, { status: 204 })
      if (url.endsWith("/agent-teams") && init.method === "GET") {
        return json({ teams: [team] })
      }
      return json(team, init.method === "POST" ? 201 : 200)
    })

    expect(await listAgentTeams("org-1")).toEqual([team])
    expect(await getAgentTeam("org-1", "atm_launch")).toEqual(team)
    await createAgentTeam("org-1", {
      name: "Launch",
      description: "Ship the launch",
      members: [{ agent_id: "agt_research", role: "Verify claims" }],
    })
    await updateAgentTeam("org-1", "atm_launch", { status: "disabled" })
    await expect(
      deleteAgentTeam("org-1", "atm_launch")
    ).resolves.toBeUndefined()

    expect(sent(fetchMock)).toEqual([
      ["GET", "/v1/organizations/org-1/agent-teams", undefined],
      ["GET", "/v1/organizations/org-1/agent-teams/atm_launch", undefined],
      [
        "POST",
        "/v1/organizations/org-1/agent-teams",
        '{"name":"Launch","description":"Ship the launch","members":[{"agent_id":"agt_research","role":"Verify claims"}]}',
      ],
      [
        "PATCH",
        "/v1/organizations/org-1/agent-teams/atm_launch",
        '{"status":"disabled"}',
      ],
      ["DELETE", "/v1/organizations/org-1/agent-teams/atm_launch", undefined],
    ])
  })

  it("sends a roster as agent ids and roles only, even from the team's own members", async () => {
    const fetchMock = stubFetch(() => json(team))

    // team.members carry name, status and position, which the handler's
    // strict decoder would refuse.
    await updateAgentTeam("org-1", "atm_launch", {
      name: "Launch v2",
      members: team.members,
    })

    expect(sent(fetchMock)).toEqual([
      [
        "PATCH",
        "/v1/organizations/org-1/agent-teams/atm_launch",
        '{"name":"Launch v2","members":[{"agent_id":"agt_research","role":"Verify claims"}]}',
      ],
    ])
  })

  it("reads the latest conversation, or what followed a sequence", async () => {
    const fetchMock = stubFetch(() => json({ messages: [message("m1", 1)] }))

    expect(await listAgentTeamMessages("org-1", "atm launch")).toEqual([
      message("m1", 1),
    ])
    await listAgentTeamMessages("org-1", "atm_launch", { afterSeq: 12 })

    expect(fetchMock.mock.calls.map(([url]) => url)).toEqual([
      "/v1/organizations/org-1/agent-teams/atm%20launch/messages",
      "/v1/organizations/org-1/agent-teams/atm_launch/messages?after_seq=12",
    ])
  })

  it("posts a line and returns it with every reply", async () => {
    const said = [message("m2", 2, "@research check this"), message("m3", 3)]
    const fetchMock = stubFetch(() => json({ messages: said }, 201))

    expect(
      await postAgentTeamMessage("org-1", "atm_launch", "@research check this")
    ).toEqual(said)
    expect(sent(fetchMock)).toEqual([
      [
        "POST",
        "/v1/organizations/org-1/agent-teams/atm_launch/messages",
        '{"body":"@research check this"}',
      ],
    ])
  })

  it("refuses a post to a paused team with its own code", async () => {
    stubFetch(() =>
      json(
        { error: { code: "team_disabled", message: "agent team is disabled" } },
        409
      )
    )

    await expect(
      postAgentTeamMessage("org-1", "atm_launch", "hello")
    ).rejects.toMatchObject({ status: 409, code: "team_disabled" })
  })

  it("reads which thread each agent answers the team in", async () => {
    const fetchMock = stubFetch(() =>
      json({ threads: { agt_research: "thread-1" } })
    )

    expect(await getAgentTeamThreads("org-1", "atm_launch")).toEqual({
      agt_research: "thread-1",
    })
    expect(sent(fetchMock)).toEqual([
      [
        "GET",
        "/v1/organizations/org-1/agent-teams/atm_launch/agent-threads",
        undefined,
      ],
    ])
  })
})

describe("mergeAgentTeamMessages", () => {
  it("keeps one message per id, the newer copy, in sequence order", () => {
    const merged = mergeAgentTeamMessages(
      [message("m1", 1), message("m2", 2, "old")],
      [message("m3", 3), message("m2", 2, "new")]
    )
    expect(merged.map((item) => [item.id, item.body])).toEqual([
      ["m1", "m1"],
      ["m2", "new"],
      ["m3", "m3"],
    ])
  })
})
