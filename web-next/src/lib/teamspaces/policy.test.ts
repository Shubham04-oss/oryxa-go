import { describe, expect, it } from "vitest"
import {
  canChangeContribution,
  canContribute,
  canPublishSharedWork,
  canRemoveContribution,
  canUseRoom,
  contributionCandidates,
  publicationAuthors,
  safeArtifactHref,
} from "./policy"
import type { Teamspace, TeamspaceContribution } from "./client"
import type { ManagedAgent } from "@/lib/agents/managed-agents"

function contribution(
  overrides: Partial<TeamspaceContribution>
): TeamspaceContribution {
  return {
    agent_id: "agt_reviewer",
    owner_user_id: "user-1",
    name: "Reviewer",
    status: "active",
    role: "Review architecture",
    delegation_mode: "request",
    position: 0,
    created_at: "2026-09-23T00:00:00Z",
    ...overrides,
  }
}

function space(
  overrides: Omit<Partial<Teamspace>, "organization_agent"> & {
    organization_agent?: Partial<Teamspace["organization_agent"]>
  } = {}
): Teamspace {
  const { organization_agent: organizationAgent, ...rest } = overrides
  return {
    id: "tsp_1",
    organization_id: "org-1",
    team_id: "team_eng",
    team_name: "Engineering",
    name: "Engineering Space",
    created_by_user_id: "user-1",
    organization_agent: {
      id: "oag_1",
      organization_id: "org-1",
      name: "Atlas",
      instructions: "Synthesize shared work.",
      status: "active",
      revision: 1,
      can_manage: false,
      created_at: "2026-09-23T00:00:00Z",
      updated_at: "2026-09-23T00:00:00Z",
      ...organizationAgent,
    },
    whisperers: [
      {
        user_id: "user-1",
        user_display_name: "Ada",
        agent_id: "agt_ada",
        agent_name: "Nia",
        mode: "whisperer",
      },
      {
        user_id: "user-2",
        user_display_name: "Bo",
        agent_id: "agt_bo",
        agent_name: "Kai",
        mode: "whisperer",
      },
    ],
    status: "active",
    revision: 1,
    contributions: [
      contribution({}),
      contribution({
        agent_id: "agt_paused",
        name: "Paused",
        status: "disabled",
      }),
      contribution({
        agent_id: "agt_bos",
        owner_user_id: "user-2",
        name: "Bo's",
      }),
    ],
    can_manage: false,
    is_member: true,
    created_at: "2026-09-23T00:00:00Z",
    updated_at: "2026-09-23T00:00:00Z",
    ...rest,
  }
}

function specialist(id: string, status: ManagedAgent["status"] = "active") {
  return {
    id,
    kind: "specialist",
    status,
  } as ManagedAgent
}

describe("publishing shared work", () => {
  it("offers the member's whisperer and their own active contributions only", () => {
    expect(publicationAuthors(space(), "user-1")).toEqual([
      { agent_id: "agt_ada", agent_name: "Nia", source: "whisperer" },
      {
        agent_id: "agt_reviewer",
        agent_name: "Reviewer",
        source: "contribution",
      },
    ])
    expect(publicationAuthors(space(), "user-3")).toEqual([])
  })

  it("needs a member, an active space and organization agent, and an author", () => {
    expect(canPublishSharedWork(space(), "user-1")).toBe(true)
    expect(canPublishSharedWork(space({ is_member: false }), "user-1")).toBe(
      false
    )
    expect(canPublishSharedWork(space({ status: "disabled" }), "user-1")).toBe(
      false
    )
    expect(
      canPublishSharedWork(
        space({ organization_agent: { status: "disabled" } }),
        "user-1"
      )
    ).toBe(false)
    // A member with no agent in the space has nobody to publish as.
    expect(canPublishSharedWork(space(), "user-3")).toBe(false)
  })
})

describe("contributions", () => {
  it("lets a member of an active space contribute", () => {
    expect(canContribute(space())).toBe(true)
    expect(canContribute(space({ is_member: false, can_manage: true }))).toBe(
      false
    )
    expect(canContribute(space({ status: "disabled" }))).toBe(false)
  })

  it("offers active specialists that are not there yet", () => {
    const agents = [
      { id: "agt_primary", kind: "primary", status: "active" } as ManagedAgent,
      specialist("agt_reviewer"),
      specialist("agt_new"),
      specialist("agt_off", "disabled"),
    ]
    expect(
      contributionCandidates(agents, space()).map((agent) => agent.id)
    ).toEqual(["agt_new"])
  })

  it("lets only the owner change one, and the owner or a manager remove it", () => {
    const own = contribution({})
    const theirs = contribution({ owner_user_id: "user-2" })

    expect(canChangeContribution(own, "user-1")).toBe(true)
    expect(canChangeContribution(theirs, "user-1")).toBe(false)
    expect(canRemoveContribution(space(), own, "user-1")).toBe(true)
    expect(canRemoveContribution(space(), theirs, "user-1")).toBe(false)
    expect(
      canRemoveContribution(space({ can_manage: true }), theirs, "user-1")
    ).toBe(true)
  })
})

describe("canUseRoom", () => {
  it("opens the room to members of an active space", () => {
    expect(canUseRoom(space())).toBe(true)
    expect(canUseRoom(space({ status: "disabled" }))).toBe(false)
    expect(canUseRoom(space({ is_member: false, can_manage: true }))).toBe(
      false
    )
  })
})

describe("safeArtifactHref", () => {
  it.each([
    "javascript:alert(1)",
    "data:text/html,<script>alert(1)</script>",
    "http://example.test/report",
    "https://user:secret@files.example/report",
    "//evil.example/report",
    "/\\evil.example/report",
    "/\t/evil.example/report",
    "/.//evil.example/report",
    "not a url",
    "",
  ])("keeps an untrusted address as text: %j", (uri) => {
    expect(safeArtifactHref(uri)).toBeUndefined()
  })

  it("links HTTPS and paths on this origin, normalized", () => {
    expect(safeArtifactHref("https://files.example/report")).toBe(
      "https://files.example/report"
    )
    expect(safeArtifactHref(" /v1/files/report?version=2#top ")).toBe(
      "/v1/files/report?version=2#top"
    )
    expect(safeArtifactHref("/v1/files/a b")).toBe("/v1/files/a%20b")
  })
})
