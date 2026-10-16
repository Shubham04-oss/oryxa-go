import { describe, expect, it } from "vitest"
import {
  sectionArea,
  sidebarDirectMessages,
  teamspaceRowCount,
} from "./workspace-sidebar"
import type { Conversation } from "@/lib/dms"
import type { Teamspace } from "@/lib/teamspaces/client"

function conversation(
  peerId: string,
  unread = 0,
  kind: Conversation["kind"] = "dm"
): Conversation {
  return {
    kind,
    name: `${kind}-${peerId}`,
    peerId,
    unread,
    updatedAt: "2026-10-15T10:00:00Z",
  }
}

describe("sidebarDirectMessages", () => {
  it("keeps the three latest DMs, then only unread ones, up to six", () => {
    const listed = sidebarDirectMessages([
      conversation("a"),
      conversation("side", 4, "side"),
      conversation("b"),
      conversation("c"),
      conversation("d"),
      conversation("e", 1),
      conversation("f", 2),
      conversation("g", 1),
      conversation("h", 1),
    ])
    expect(listed.map((item) => item.peerId)).toEqual([
      "a",
      "b",
      "c",
      "e",
      "f",
      "g",
    ])
  })
})

describe("sectionArea", () => {
  it("puts each page in the area that remembers it", () => {
    expect(sectionArea("overview")).toBe("home")
    expect(sectionArea("tools")).toBe("home")
    expect(sectionArea("people")).toBe("team")
    expect(sectionArea("teamspaces")).toBe("team")
    expect(sectionArea("requests")).toBe("agents")
    expect(sectionArea("multi-agents")).toBe("agents")
  })
})

describe("teamspaceRowCount", () => {
  const space = { id: "tsp_1", is_member: true } as unknown as Teamspace

  it("shows a teamspace's unread room messages as a strong count, capped", () => {
    expect(teamspaceRowCount({ ...space, unread_count: 3 })).toEqual({
      count: "3",
      countLabel: "3 unread",
      strong: true,
    })
    expect(teamspaceRowCount({ ...space, unread_count: 100 })).toMatchObject({
      count: "99+",
    })
  })

  it("shows nothing when all is read or the list doesn't say", () => {
    expect(teamspaceRowCount({ ...space, unread_count: 0 })).toEqual({})
    expect(teamspaceRowCount(space)).toEqual({})
  })
})
