import { describe, expect, it } from "vitest"
import type { Item, ThreadDetail } from "@/lib/threads/client"
import {
  agentFilesOf,
  agoPhrase,
  batchToolkits,
  briefsOf,
  firstSentence,
  scheduleWords,
} from "./today-format"

const NOW = Date.parse("2026-10-13T12:00:00Z")
const ago = (minutes: number) => new Date(NOW - minutes * 60_000).toISOString()

const detail = (id: string, items: Item[]): ThreadDetail => ({
  id,
  title: id,
  origin: "wakeup",
  updated_at: ago(0),
  items,
  pending_confirmations: [],
})
const message = (
  id: string,
  author: string,
  text: string,
  at = ago(0)
): Item => ({ type: "message", id, author, text, at })
const batch = (id: string, at: string, ...toolkits: string[]): Item =>
  message(
    id,
    "apps",
    toolkits
      .map((toolkit, i) => `${i + 1}. ${toolkit} · event · 12:00`)
      .join("\n"),
    at
  )
const artifact = (
  id: string,
  name: string,
  version: number,
  at?: string
): Item => ({ type: "artifact", id, artifact: name, version, at })

describe("batchToolkits", () => {
  it("names each app a batch's numbered lines come from, once, lowercased, in order", () => {
    expect(
      batchToolkits(
        [
          "Your apps reported:",
          "1. GitHub · pull_request · 09:00",
          "2. linear · issue · 09:01",
          "3. github · push · 09:02",
          "4. an app · something · 09:03",
          "5. An App · something · 09:04",
        ].join("\n")
      )
    ).toEqual(["github", "linear"])
  })

  it("finds nothing in text without numbered event lines", () => {
    expect(batchToolkits("github · push · 09:00")).toEqual([])
    expect(batchToolkits("")).toEqual([])
  })
})

describe("firstSentence", () => {
  it("drops Markdown marks, links and code blocks and keeps the first sentence", () => {
    expect(
      firstSentence(
        "## **PR #12** was _merged_ into [main](https://x.test). Then more."
      )
    ).toBe("PR #12 was merged into main.")
    expect(firstSentence("```\ncode\n```\n- `Build` passed! Next.")).toBe(
      "Build passed!"
    )
    expect(firstSentence("> Is it   done? Yes.")).toBe("Is it done?")
  })

  it("keeps text without an end as it is", () => {
    expect(firstSentence("  v1.2 shipped  ")).toBe("v1.2 shipped")
  })

  it("truncates a long sentence to max characters with an ellipsis", () => {
    const long = `${"word ".repeat(40)}end.`
    const short = firstSentence(long, 20)
    expect(short).toHaveLength(20)
    expect(short.endsWith("…")).toBe(true)
    expect(short).toBe("word word word word…")
    expect(firstSentence("Exactly ten.", 12)).toBe("Exactly ten.")
  })
})

describe("briefsOf", () => {
  it("pairs each apps batch with the agent message that answers it, newest first", () => {
    const briefs = briefsOf(
      detail("wake", [
        batch("b1", ago(60), "github", "linear"),
        message("a1", "Krude", "**PR #12** merged. Details.", ago(59)),
        batch("b2", ago(10), "notion"),
        message("a2", "Krude", "A page changed! More.", ago(9)),
      ])
    )
    expect(briefs).toEqual([
      {
        key: expect.any(String),
        text: "A page changed!",
        at: ago(9),
        toolkits: ["notion"],
      },
      {
        key: expect.any(String),
        text: "PR #12 merged.",
        at: ago(59),
        toolkits: ["github", "linear"],
      },
    ])
  })

  it("ignores agent messages after a member message, and a second reply to one batch", () => {
    const briefs = briefsOf(
      detail("wake", [
        batch("b1", ago(30), "github"),
        message("m1", "member", "What about this?", ago(29)),
        message("a1", "Krude", "An answer to the member.", ago(28)),
        batch("b2", ago(20), "linear"),
        message("a2", "Krude", "A brief.", ago(19)),
        message("a3", "Krude", "A follow-up, not a brief.", ago(18)),
        message("a4", "Krude", "No batch before me.", ago(17)),
      ])
    )
    expect(briefs.map((brief) => brief.text)).toEqual(["A brief."])
  })

  it("skips an empty agent message and keeps waiting for the brief", () => {
    const briefs = briefsOf(
      detail("wake", [
        batch("b1", ago(30), "github"),
        message("a1", "Krude", "   ", ago(29)),
        message("a2", "Krude", "The real brief.", ago(28)),
      ])
    )
    expect(briefs.map((brief) => brief.text)).toEqual(["The real brief."])
  })

  it("keeps at most max briefs, the newest", () => {
    const items = [1, 2, 3, 4, 5, 6].flatMap((n) => [
      batch(`b${n}`, ago(100 - n * 2), "github"),
      message(`a${n}`, "Krude", `Brief ${n}.`, ago(99 - n * 2)),
    ])
    expect(briefsOf(detail("wake", items)).map((b) => b.text)).toEqual([
      "Brief 6.",
      "Brief 5.",
      "Brief 4.",
      "Brief 3.",
    ])
    expect(briefsOf(detail("wake", items), 2).map((b) => b.text)).toEqual([
      "Brief 6.",
      "Brief 5.",
    ])
  })
})

describe("agentFilesOf", () => {
  it("lists each thread's file once at its latest version, newest first", () => {
    const files = agentFilesOf([
      detail("one", [
        artifact("e1", "report.pdf", 1, ago(30)),
        artifact("e2", "report.pdf", 2, ago(10)),
      ]),
      undefined,
      detail("two", [
        artifact("e3", "report.pdf", 1, ago(20)),
        artifact("e4", "notes.md", 1, ago(5)),
      ]),
    ])
    expect(files).toEqual([
      { key: "two/notes.md", name: "notes.md", threadId: "two", at: ago(5) },
      {
        key: "one/report.pdf",
        name: "report.pdf",
        threadId: "one",
        at: ago(10),
      },
      {
        key: "two/report.pdf",
        name: "report.pdf",
        threadId: "two",
        at: ago(20),
      },
    ])
  })

  it("keeps the later version even when it came first in the list", () => {
    const files = agentFilesOf([
      detail("one", [
        artifact("e2", "plan.md", 2, ago(1)),
        artifact("e1", "plan.md", 1, ago(50)),
      ]),
    ])
    expect(files).toEqual([
      { key: "one/plan.md", name: "plan.md", threadId: "one", at: ago(1) },
    ])
  })

  it("keeps at most max files", () => {
    const items = [1, 2, 3, 4, 5].map((n) =>
      artifact(`e${n}`, `file-${n}.txt`, 1, ago(10 - n))
    )
    expect(agentFilesOf([detail("one", items)]).map((f) => f.name)).toEqual([
      "file-5.txt",
      "file-4.txt",
      "file-3.txt",
      "file-2.txt",
    ])
    expect(agentFilesOf([detail("one", items)], 1)).toHaveLength(1)
  })
})

describe("agoPhrase", () => {
  it("says how long ago, from just now to a date", () => {
    expect(agoPhrase(undefined, NOW)).toBe("")
    expect(agoPhrase("not a time", NOW)).toBe("")
    expect(agoPhrase(ago(0), NOW)).toBe("Just now")
    // A time a little ahead of the clock is not in the future.
    expect(agoPhrase(ago(-2), NOW)).toBe("Just now")
    expect(agoPhrase(ago(1), NOW)).toBe("1 min ago")
    expect(agoPhrase(ago(59), NOW)).toBe("59 min ago")
    expect(agoPhrase(ago(60), NOW)).toBe("1 h ago")
    expect(agoPhrase(ago(23 * 60), NOW)).toBe("23 h ago")
    expect(agoPhrase(ago(30 * 60), NOW)).toBe("Yesterday")
    const older = ago(5 * 24 * 60)
    expect(agoPhrase(older, NOW)).toBe(
      new Intl.DateTimeFormat(undefined, {
        month: "short",
        day: "numeric",
      }).format(Date.parse(older))
    )
  })
})

describe("scheduleWords", () => {
  it("reads rates", () => {
    expect(scheduleWords("rate(1 day)", "UTC")).toBe("Every day")
    expect(scheduleWords("rate(1 hours)", "UTC")).toBe("Every hour")
    expect(scheduleWords("rate(15 minutes)", "UTC")).toBe("Every 15 minutes")
    expect(scheduleWords(" rate(2 days) ", "UTC")).toBe("Every 2 days")
  })

  it("reads daily, weekday and named-day crons at a padded time in the zone", () => {
    expect(scheduleWords("cron(0 9 * * ? *)", "UTC")).toBe("Daily at 09:00 UTC")
    expect(scheduleWords("cron(5 7 ? * * *)", "Asia/Kolkata")).toBe(
      "Daily at 07:05 Asia/Kolkata"
    )
    expect(scheduleWords("cron(30 17 ? * MON-FRI *)", "UTC")).toBe(
      "Weekdays at 17:30 UTC"
    )
    expect(scheduleWords("cron(0 8 ? * MON *)", "UTC")).toBe(
      "Mondays at 08:00 UTC"
    )
    expect(scheduleWords("cron(0 8 ? * mon,thu *)", "UTC")).toBe(
      "Mondays and Thursdays at 08:00 UTC"
    )
    expect(scheduleWords("cron(0 8 ? * 2#1 *)", "UTC")).toBe("2#1 at 08:00 UTC")
  })

  it("reads a one-off time", () => {
    expect(scheduleWords("at(2026-10-20T09:00:00)", "UTC")).toBe(
      "Once, 2026-10-20 09:00:00 UTC"
    )
  })

  it("falls back to the expression it cannot read", () => {
    expect(scheduleWords("cron(*/5 * * * ? *)", "UTC")).toBe(
      "cron(*/5 * * * ? *)"
    )
    expect(scheduleWords("rate(1 week)", "UTC")).toBe("rate(1 week)")
    expect(scheduleWords("whenever", "UTC")).toBe("whenever")
  })
})
