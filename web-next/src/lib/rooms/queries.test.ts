import { QueryClient } from "@tanstack/react-query"
import { afterEach, describe, expect, it, vi } from "vitest"
import { teamspaceKeys } from "@/lib/teamspaces/queries"
import { roomCatchUpQueryOptions, roomKeys } from "./queries"

const org = "org-1"
const teamspace = "tsp_1"
const path = `/v1/organizations/${org}/teamspaces/${teamspace}`

afterEach(() => vi.unstubAllGlobals())

describe("roomKeys", () => {
  it("keeps the room under its teamspace, so deleting the teamspace drops it", () => {
    expect(roomKeys.room(org, teamspace)).toEqual([
      ...teamspaceKeys.teamspace(org, teamspace),
      "room",
    ])
  })
})

describe("roomCatchUpQueryOptions", () => {
  it("keys the catch-up by where the member left off and asks from there", async () => {
    const fetchMock = vi.fn(
      async (_url: string, _init: RequestInit) =>
        new Response(
          JSON.stringify({
            brief: { room_id: teamspace, revision: 1, through_seq: 2 },
            messages: [4, 5].map((seq) => ({ seq })),
          }),
          { headers: { "Content-Type": "application/json" } }
        )
    )
    vi.stubGlobal("fetch", fetchMock)
    const options = roomCatchUpQueryOptions(org, teamspace, 3)

    expect(options.queryKey).toEqual([
      ...roomKeys.room(org, teamspace),
      "catch-up",
      3,
    ])
    expect(options.refetchInterval).toBeUndefined()
    const caught = await new QueryClient().fetchQuery(options)
    expect(fetchMock.mock.calls.map(([url]) => url)).toEqual([
      `${path}/catchup?after=3`,
    ])
    expect(caught.messages.map((item) => item.seq)).toEqual([4, 5])
  })
})
