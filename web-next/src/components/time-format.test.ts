import { describe, expect, it } from "vitest"
import { clockTime, dayLabel, fullTime } from "./time-format"

/** A local time, as the services write it. */
function at(
  year: number,
  month: number,
  day: number,
  hours = 12,
  minutes = 0,
  seconds = 0
): string {
  return new Date(year, month - 1, day, hours, minutes, seconds).toISOString()
}

const now = Date.parse(at(2026, 9, 25, 15, 30))

describe("dayLabel", () => {
  it("says Today, Yesterday, the day this year, and the date before it", () => {
    expect(dayLabel(at(2026, 9, 25, 0, 1), now)).toBe("Today")
    expect(dayLabel(at(2026, 9, 24, 23, 59), now)).toBe("Yesterday")
    expect(dayLabel(at(2026, 9, 3), now)).toBe(
      new Intl.DateTimeFormat(undefined, {
        weekday: "short",
        day: "numeric",
        month: "short",
      }).format(new Date(at(2026, 9, 3)))
    )
    expect(dayLabel(at(2025, 12, 31), now)).toBe(
      new Intl.DateTimeFormat(undefined, {
        day: "numeric",
        month: "short",
        year: "numeric",
      }).format(new Date(at(2025, 12, 31)))
    )
    expect(dayLabel("not a time", now)).toBe("")
  })
})

describe("clockTime", () => {
  it("is the time of day in the viewer's locale, and nothing for a value that is not a time", () => {
    const moment = at(2026, 9, 25, 14, 2)
    expect(clockTime(moment)).toBe(
      new Intl.DateTimeFormat(undefined, {
        hour: "numeric",
        minute: "2-digit",
      }).format(new Date(moment))
    )
    expect(clockTime("not a time")).toBe("")
  })
})

describe("fullTime", () => {
  it("is the date and the time, and a value that is not a time as it came", () => {
    const moment = at(2026, 9, 25, 14, 2)
    expect(fullTime(moment)).toBe(
      new Intl.DateTimeFormat(undefined, {
        dateStyle: "medium",
        timeStyle: "short",
      }).format(new Date(moment))
    )
    expect(fullTime("not a time")).toBe("not a time")
  })
})
