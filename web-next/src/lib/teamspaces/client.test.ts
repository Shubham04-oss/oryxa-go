import { afterEach, describe, expect, it, vi } from "vitest"
import {
  addTeamspaceContribution,
  createTeamspace,
  createTeamspaceTask,
  deleteTeamspace,
  deleteTeamspaceContribution,
  deleteTeamspaceTask,
  getTeamspace,
  listTeamspaceEntries,
  listTeamspaceTasks,
  listTeamspaces,
  publishTeamspaceEntry,
  updateTeamspace,
  updateTeamspaceContribution,
  updateTeamspaceTask,
} from "./client"
import type { Teamspace, TeamspaceArtifact } from "./client"

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

const space = {
  id: "tsp_1",
  organization_id: "org-1",
  team_id: "team_eng",
  team_name: "Engineering",
  name: "Engineering Space",
  created_by_user_id: "user-1",
  whisperers: [],
  status: "active",
  revision: 1,
  contributions: [],
  can_manage: true,
  is_member: true,
  created_at: "2026-09-23T00:00:00Z",
  updated_at: "2026-09-23T00:00:00Z",
} as unknown as Teamspace

const base = "/v1/organizations/org-1/teamspaces"

afterEach(() => vi.unstubAllGlobals())

describe("teamspaces API", () => {
  it("lists, reads, creates, updates and deletes teamspaces", async () => {
    const fetchMock = stubFetch((url, init) => {
      if (init.method === "DELETE") return new Response(null, { status: 204 })
      if (url === base && init.method === "GET") {
        return json({ teamspaces: [space] })
      }
      return json(space, init.method === "POST" ? 201 : 200)
    })

    expect(await listTeamspaces("org-1")).toEqual([space])
    expect(await getTeamspace("org-1", "tsp 1")).toEqual(space)
    await createTeamspace("org-1", {
      team_id: "team_eng",
      name: "Engineering Space",
    })
    await updateTeamspace("org-1", "tsp_1", { status: "disabled" })
    await expect(deleteTeamspace("org-1", "tsp_1")).resolves.toBeUndefined()

    expect(sent(fetchMock)).toEqual([
      ["GET", base, undefined],
      ["GET", `${base}/tsp%201`, undefined],
      ["POST", base, '{"team_id":"team_eng","name":"Engineering Space"}'],
      ["PATCH", `${base}/tsp_1`, '{"status":"disabled"}'],
      ["DELETE", `${base}/tsp_1`, undefined],
    ])
  })

  it("answers every contribution change with the whole teamspace, delete included", async () => {
    const fetchMock = stubFetch((_url, init) =>
      json(space, init.method === "POST" ? 201 : 200)
    )

    expect(
      await addTeamspaceContribution("org-1", "tsp_1", {
        agent_id: "agt_reviewer",
        role: "Review architecture",
        delegation_mode: "automatic",
      })
    ).toEqual(space)
    expect(
      await updateTeamspaceContribution("org-1", "tsp_1", "agt reviewer", {
        delegation_mode: "request",
      })
    ).toEqual(space)
    // Unlike every other delete here, this one is 200 with a body.
    expect(
      await deleteTeamspaceContribution("org-1", "tsp_1", "agt_reviewer")
    ).toEqual(space)

    expect(sent(fetchMock)).toEqual([
      [
        "POST",
        `${base}/tsp_1/agents`,
        '{"agent_id":"agt_reviewer","role":"Review architecture","delegation_mode":"automatic"}',
      ],
      [
        "PATCH",
        `${base}/tsp_1/agents/agt%20reviewer`,
        '{"delegation_mode":"request"}',
      ],
      ["DELETE", `${base}/tsp_1/agents/agt_reviewer`, undefined],
    ])
  })

  it("reads the ledger with an optional limit", async () => {
    const entry = { id: "tse_1", title: "Architecture review" }
    const fetchMock = stubFetch(() => json({ entries: [entry] }))

    expect(await listTeamspaceEntries("org-1", "tsp_1")).toEqual([entry])
    await listTeamspaceEntries("org-1", "tsp_1", { limit: 25 })

    expect(fetchMock.mock.calls.map(([url]) => url)).toEqual([
      `${base}/tsp_1/entries`,
      `${base}/tsp_1/entries?limit=25`,
    ])
  })

  it("publishes an entry with only the fields the handler reads", async () => {
    const fetchMock = stubFetch(() => json({ id: "tse_2" }, 201))

    await publishTeamspaceEntry("org-1", "tsp_1", {
      author_agent_id: "agt_primary",
      kind: "result",
      title: "Architecture review",
      summary: "Ready to proceed.",
      tools_used: ["github.search"],
      artifacts: [
        {
          name: "Review",
          uri: "https://files.example/review",
          id: "art_1",
        } as TeamspaceArtifact,
      ],
    })
    await publishTeamspaceEntry("org-1", "tsp_1", {
      author_agent_id: "agt_primary",
      kind: "update",
      title: "Status",
      summary: "On track.",
    })

    expect(sent(fetchMock)).toEqual([
      [
        "POST",
        `${base}/tsp_1/entries`,
        '{"author_agent_id":"agt_primary","kind":"result","title":"Architecture review","summary":"Ready to proceed.","tools_used":["github.search"],"artifacts":[{"name":"Review","uri":"https://files.example/review"}]}',
      ],
      [
        "POST",
        `${base}/tsp_1/entries`,
        '{"author_agent_id":"agt_primary","kind":"update","title":"Status","summary":"On track."}',
      ],
    ])
  })

  it("keeps the service's codes for conflicts, pauses and refusals", async () => {
    const answers = [
      failure(409, "teamspace_conflict", "teamspace already exists"),
      failure(409, "teamspace_disabled", "teamspace is disabled"),
      failure(
        403,
        "forbidden",
        "teamspace is not available to this member: only a current team member can contribute an agent"
      ),
      failure(404, "not_found", "teamspace not found"),
    ]
    stubFetch(() => answers.shift() ?? json({}))

    await expect(
      createTeamspace("org-1", { team_id: "team_eng", name: "Again" })
    ).rejects.toMatchObject({ status: 409, code: "teamspace_conflict" })
    await expect(
      publishTeamspaceEntry("org-1", "tsp_1", {
        author_agent_id: "agt_primary",
        kind: "update",
        title: "t",
        summary: "s",
      })
    ).rejects.toMatchObject({ status: 409, code: "teamspace_disabled" })
    await expect(
      addTeamspaceContribution("org-1", "tsp_1", {
        agent_id: "agt_x",
        role: "r",
        delegation_mode: "request",
      })
    ).rejects.toMatchObject({ status: 403, code: "forbidden" })
    await expect(getTeamspace("org-1", "tsp_9")).rejects.toMatchObject({
      status: 404,
      code: "not_found",
    })
  })

  it("sets and clears a teamspace's target, sending null to clear", async () => {
    const fetchMock = stubFetch(() => json(space))

    await updateTeamspace("org-1", "tsp_1", {
      target_date: "2026-11-04",
      target_label: "Launch",
    })
    await updateTeamspace("org-1", "tsp_1", {
      target_date: null,
      target_label: null,
    })

    expect(sent(fetchMock)).toEqual([
      [
        "PATCH",
        `${base}/tsp_1`,
        JSON.stringify({ target_date: "2026-11-04", target_label: "Launch" }),
      ],
      [
        "PATCH",
        `${base}/tsp_1`,
        JSON.stringify({ target_date: null, target_label: null }),
      ],
    ])
  })

  it("lists, adds, changes and deletes checklist tasks", async () => {
    const task = { id: "tst_1", title: "Legal sign-off", done: false }
    const fetchMock = stubFetch((url, init) => {
      if (init.method === "DELETE") return new Response(null, { status: 204 })
      if (init.method === "GET") return json({ tasks: [task] })
      return json(task, init.method === "POST" ? 201 : 200)
    })

    expect(await listTeamspaceTasks("org-1", "tsp_1")).toEqual([task])
    await createTeamspaceTask("org-1", "tsp_1", {
      title: "Legal sign-off",
      owner_user_id: "user-2",
      due_date: "2026-10-30",
    })
    await updateTeamspaceTask("org-1", "tsp_1", "tst_1", {
      done: true,
      owner_user_id: null,
    })
    await deleteTeamspaceTask("org-1", "tsp_1", "tst_1")

    expect(sent(fetchMock)).toEqual([
      ["GET", `${base}/tsp_1/tasks`, undefined],
      [
        "POST",
        `${base}/tsp_1/tasks`,
        JSON.stringify({
          title: "Legal sign-off",
          owner_user_id: "user-2",
          due_date: "2026-10-30",
        }),
      ],
      [
        "PATCH",
        `${base}/tsp_1/tasks/tst_1`,
        JSON.stringify({ done: true, owner_user_id: null }),
      ],
      ["DELETE", `${base}/tsp_1/tasks/tst_1`, undefined],
    ])
  })
})
