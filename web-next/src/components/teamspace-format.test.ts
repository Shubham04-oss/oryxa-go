import { describe, expect, it } from "vitest"
import {
  dayLabel,
  daysAway,
  daysUntil,
  lastMessageLine,
  parseDay,
  targetChip,
  targetLine,
  unreadCount,
  unreadLabel,
  unreadWords,
} from "./teamspace-format"
import type { TeamspaceLastMessage } from "@/lib/teamspaces/client"

// 14 Oct 2026, mid-afternoon on the member's clock.
const now = new Date(2026, 9, 14, 15, 30).getTime()

describe("a teamspace's target", () => {
  it("reads a calendar day and nothing else", () => {
    expect(parseDay("2026-11-04")?.getDate()).toBe(4)
    expect(parseDay("2026-02-30")).toBeUndefined()
    expect(parseDay("4 Nov")).toBeUndefined()
    expect(parseDay(null)).toBeUndefined()
    expect(parseDay(undefined)).toBeUndefined()
  })

  it("counts whole days on the member's calendar", () => {
    const day = (value: string) => parseDay(value) as Date
    expect(daysUntil(day("2026-11-04"), now)).toBe(21)
    expect(daysUntil(day("2026-10-14"), now)).toBe(0)
    expect(daysUntil(day("2026-10-11"), now)).toBe(-3)
    expect(daysAway(21)).toBe("in 21 days")
    expect(daysAway(1)).toBe("tomorrow")
    expect(daysAway(0)).toBe("today")
    expect(daysAway(-1)).toBe("yesterday")
    expect(daysAway(-3)).toBe("3 days ago")
  })

  it("names the year only when it isn't this one", () => {
    const thisYear = dayLabel(parseDay("2026-11-04") as Date, { now })
    const nextYear = dayLabel(parseDay("2027-11-04") as Date, { now })
    expect(thisYear).not.toContain("2026")
    expect(nextYear).toContain("2027")
  })

  it("says the target only when it has a date", () => {
    const line = targetLine(
      { target_date: "2026-11-04", target_label: "Launch" },
      now
    )
    expect(line).toMatch(/^Launch on .+ · in 21 days$/)
    expect(
      targetLine({ target_date: "2026-11-04", target_label: null }, now)
    ).toMatch(/^Target on /)
    expect(targetLine({ target_label: "Launch" }, now)).toBeUndefined()
    expect(targetLine({}, now)).toBeUndefined()
    expect(
      targetChip({ target_date: "2026-11-04", target_label: "Launch" }, now)
    ).toMatch(/^Launch \S+/)
    expect(targetChip({ target_date: null }, now)).toBeUndefined()
  })
})

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
