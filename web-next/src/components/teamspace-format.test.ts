import { describe, expect, it } from "vitest"
import {
  lastMessageLine,
  unreadCount,
  unreadLabel,
  unreadWords,
} from "./teamspace-format"
import type { TeamspaceLastMessage } from "@/lib/teamspaces/client"

const message: TeamspaceLastMessage = {
  seq: 42,
  author_kind: "agent",
  author_id: "agt_krude",
  author_handle: "krude",
  author_name: "Krude",
  preview: "suggested an edit to the pricing sheet",
  created_at: "2026-10-16T09:12:44Z",
}

describe("unread counts", () => {
  it("is 0 when the server doesn't say", () => {
    expect(unreadCount({})).toBe(0)
    expect(unreadCount({ unread_count: 3 })).toBe(3)
  })

  it("shows the server's cap as 99+", () => {
    expect(unreadLabel(7)).toBe("7")
    expect(unreadLabel(99)).toBe("99")
    expect(unreadLabel(100)).toBe("99+")
    expect(unreadWords(100)).toBe("99+ unread")
  })
})

describe("lastMessageLine", () => {
  it("names the author before the preview", () => {
    expect(lastMessageLine(message)).toBe(
      "Krude: suggested an edit to the pricing sheet"
    )
  })

  it("falls back to the handle, and says You for the member's own", () => {
    expect(lastMessageLine({ ...message, author_name: undefined })).toBe(
      "krude: suggested an edit to the pricing sheet"
    )
    expect(
      lastMessageLine(
        { ...message, author_kind: "person", author_id: "usr_bo" },
        "usr_bo"
      )
    ).toBe("You: suggested an edit to the pricing sheet")
  })

  it("calls a parley's notes Parley notes", () => {
    expect(lastMessageLine({ ...message, kind: "parley_notes" })).toBe(
      "Parley notes"
    )
  })
})
