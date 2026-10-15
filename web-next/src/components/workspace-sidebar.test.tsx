import { describe, expect, it } from "vitest"
import { sectionArea, sidebarDirectMessages } from "./workspace-sidebar"
import type { Conversation } from "@/lib/dms"

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
