import { QueryClient } from "@tanstack/react-query"
import type { QueryKey } from "@tanstack/react-query"
import { afterEach, describe, expect, it, vi } from "vitest"
import {
  TEAMSPACE_ENTRIES_LIMIT,
  afterCreateTeamspace,
  afterDeleteTeamspace,
  afterPublishTeamspaceEntry,
  afterTeamspaceContributionChange,
  afterUpdateTeamspace,
  teamspaceEntriesQueryOptions,
  teamspaceKeys,
  teamspaceQueryOptions,
  teamspacesQueryOptions,
} from "./queries"
import type { Teamspace } from "./client"

function json(body: unknown) {
  return new Response(JSON.stringify(body), {
    headers: { "Content-Type": "application/json" },
  })
}

afterEach(() => vi.unstubAllGlobals())

describe("teamspaceKeys", () => {
  it("keys each teamspace's ledger and room beneath the teamspace", () => {
    expect(teamspaceKeys.all("org-1")).toEqual(["teamspaces", "org-1"])
    expect(teamspaceKeys.list("org-1")).toEqual(["teamspaces", "org-1", "list"])
    expect(teamspaceKeys.teamspace("org-1", "tsp_1")).toEqual([
      "teamspaces",
      "org-1",
      "detail",
      "tsp_1",
    ])
    expect(teamspaceKeys.entries("org-1", "tsp_1", 25)).toEqual([
      ...teamspaceKeys.teamspace("org-1", "tsp_1"),
      "entries",
      25,
    ])
    expect(teamspaceKeys.room("org-1", "tsp_1")).toEqual([
      ...teamspaceKeys.teamspace("org-1", "tsp_1"),
      "room",
    ])
  })
})

describe("teamspace query options", () => {
  it("read the endpoint their key names, with the ledger's default limit", async () => {
    const fetchMock = vi.fn(async (_url: string, _init: RequestInit) =>
      json({ teamspaces: [], entries: [] })
    )
    vi.stubGlobal("fetch", fetchMock)
    const queryClient = new QueryClient()

    await queryClient.fetchQuery(teamspacesQueryOptions("org-1"))
    await queryClient.fetchQuery(teamspaceQueryOptions("org-1", "tsp_1"))
    const entries = teamspaceEntriesQueryOptions("org-1", "tsp_1")
    await queryClient.fetchQuery(entries)

    expect(TEAMSPACE_ENTRIES_LIMIT).toBe(100)
    expect(entries.queryKey).toEqual(
      teamspaceKeys.entries("org-1", "tsp_1", 100)
    )
    expect(fetchMock.mock.calls.map(([url]) => url)).toEqual([
      "/v1/organizations/org-1/teamspaces",
      "/v1/organizations/org-1/teamspaces/tsp_1",
      "/v1/organizations/org-1/teamspaces/tsp_1/entries?limit=100",
    ])
  })
})

describe("teamspace mutation helpers", () => {
  const org = "org-1"
  const keys = {
    list: teamspaceKeys.list(org),
    teamspace: teamspaceKeys.teamspace(org, "tsp_1"),
    entries: teamspaceKeys.entries(org, "tsp_1", 100),
    room: teamspaceKeys.room(org, "tsp_1"),
    otherTeamspace: teamspaceKeys.teamspace(org, "tsp_2"),
    otherRoom: teamspaceKeys.room(org, "tsp_2"),
  } satisfies Record<string, QueryKey>
  type Name = keyof typeof keys
  const names = Object.keys(keys) as Name[]

  function seeded() {
    const queryClient = new QueryClient()
    for (const key of Object.values(keys)) queryClient.setQueryData(key, "held")
    return queryClient
  }
  const invalidated = (queryClient: QueryClient) =>
    names.filter((name) => queryClient.getQueryState(keys[name])?.isInvalidated)
  const removed = (queryClient: QueryClient) =>
    names.filter((name) => queryClient.getQueryState(keys[name]) === undefined)
  const updated = { id: "tsp_1", name: "Renamed" } as Teamspace

  it("cache a created or changed teamspace and refresh the list", async () => {
    for (const after of [afterCreateTeamspace, afterUpdateTeamspace]) {
      const queryClient = seeded()
      await after(queryClient, org, updated)
      expect(queryClient.getQueryData(keys.teamspace)).toBe(updated)
      expect(invalidated(queryClient)).toEqual(["list"])
    }
  })

  it("drop a deleted teamspace with its ledger and room", async () => {
    const queryClient = seeded()
    await afterDeleteTeamspace(queryClient, org, "tsp_1")
    expect(removed(queryClient)).toEqual(["teamspace", "entries", "room"])
    expect(invalidated(queryClient)).toEqual(["list"])
  })

  it("re-read what the page holds of the room after a contribution changes", async () => {
    const queryClient = seeded()
    await afterTeamspaceContributionChange(queryClient, org, updated)
    expect(queryClient.getQueryData(keys.teamspace)).toBe(updated)
    expect(invalidated(queryClient)).toEqual(["list", "room"])
  })

  it("refresh the ledger after publishing", async () => {
    const queryClient = seeded()
    await afterPublishTeamspaceEntry(queryClient, org, "tsp_1")
    expect(invalidated(queryClient)).toEqual(["entries"])
  })
})
