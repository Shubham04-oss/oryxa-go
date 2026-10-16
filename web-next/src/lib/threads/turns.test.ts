import { describe, expect, it } from "vitest"
import {
  buildTimeline,
  groupTurns,
  openedLinks,
  runFailureWords,
  toolKeyArgument,
  turnDuration,
  turnFailureCode,
  turnStatus,
  turnTimes,
  turnUsage,
  unseenLiveItems,
} from "./timeline"
import type { Item } from "./client"

const task = (id: string, text: string, at?: string): Item => ({
  type: "message",
  id,
  author: "member",
  text,
  at,
})
const answer = (id: string, text: string, at?: string): Item => ({
  type: "message",
  id,
  author: "Leo",
  text,
  at,
})

function turnsOf(items: Item[]) {
  return groupTurns(buildTimeline(items), items)
}

describe("groupTurns", () => {
  it("makes each task a turn with the work that answered it", () => {
    const turns = turnsOf([
      task("ev_1", "Plan the launch"),
      { type: "tool_call", id: "ev_2", call_id: "c1", tool: "web_search" },
      { type: "tool_result", id: "ev_3", call_id: "c1", tool: "web_search" },
      answer("ev_4", "Here is the plan."),
      task("ev_5", "Shorter"),
      answer("ev_6", "Done."),
    ])
    expect(turns).toHaveLength(2)
    expect(turns[0].prompt?.text).toBe("Plan the launch")
    expect(turns[0].entries.map((entry) => entry.kind)).toEqual([
      "tool",
      "message",
    ])
    expect(turns[1].prompt?.text).toBe("Shorter")
    expect(turns[1].entries.map((entry) => entry.kind)).toEqual(["message"])
  })

  it("keeps work before any task as a turn without one", () => {
    const turns = turnsOf([answer("ev_1", "Morning."), task("ev_2", "Hi")])
    expect(turns[0].prompt).toBeUndefined()
    expect(turns[0].entries).toHaveLength(1)
    expect(turns[1].prompt?.text).toBe("Hi")
  })

  it("starts a turn at a batch from the member's apps", () => {
    const turns = turnsOf([
      { type: "message", id: "ev_1", author: "apps", text: "1. github · …" },
      answer("ev_2", "A PR needs you."),
    ])
    expect(turns).toHaveLength(1)
    expect(turns[0].prompt?.role).toBe("apps")
  })

  it("places each run's markers and usage in its turn", () => {
    const usage = { input_tokens: 900, output_tokens: 100, total_tokens: 1000 }
    const items: Item[] = [
      task("ev_1", "One"),
      { type: "run_started", run_id: "r1", at: "2026-10-15T09:00:00Z" },
      answer("ev_2", "First."),
      {
        type: "run_completed",
        run_id: "r1",
        at: "2026-10-15T09:00:12Z",
        usage,
      },
      task("ev_3", "Two"),
      { type: "run_started", run_id: "r2", at: "2026-10-15T09:01:00Z" },
      { type: "run_failed", run_id: "r2", at: "2026-10-15T09:01:03Z" },
    ]
    const [first, second] = turnsOf(items)
    expect(first.runs).toEqual([
      {
        runId: "r1",
        startedAt: "2026-10-15T09:00:00Z",
        endedAt: "2026-10-15T09:00:12Z",
        usage,
      },
    ])
    expect(turnUsage(first)).toEqual(usage)
    expect(turnTimes(first, { finished: true })).toEqual({
      start: Date.parse("2026-10-15T09:00:00Z"),
      end: Date.parse("2026-10-15T09:00:12Z"),
    })
    expect(second.runs[0].failed).toBe(true)
  })

  it("adds up the usage of a turn's runs, such as after an approval", () => {
    const usage = { input_tokens: 10, output_tokens: 5, total_tokens: 15 }
    const turns = turnsOf([
      task("ev_1", "Send it"),
      { type: "run_started", run_id: "r1" },
      { type: "run_completed", run_id: "r1", usage },
      { type: "run_started", run_id: "r2" },
      { type: "run_completed", run_id: "r2", usage },
    ])
    expect(turnUsage(turns[0])?.total_tokens).toBe(30)
  })

  it("has no usage, nor an end, where the items say nothing", () => {
    const [turn] = turnsOf([task("ev_1", "Hi"), answer("ev_2", "Hello")])
    expect(turnUsage(turn)).toBeUndefined()
    expect(turnTimes(turn, { finished: true })).toEqual({ start: undefined })
  })

  it("falls back to the items' own times for a history without runs", () => {
    const [turn] = turnsOf([
      task("ev_1", "Hi", "2026-10-15T09:00:00Z"),
      answer("ev_2", "Hello", "2026-10-15T09:00:04Z"),
    ])
    expect(turnTimes(turn, { finished: true })).toEqual({
      start: Date.parse("2026-10-15T09:00:00Z"),
      end: Date.parse("2026-10-15T09:00:04Z"),
    })
    expect(turnTimes(turn, { finished: false }).end).toBeUndefined()
  })
})

describe("run brackets from history (#390)", () => {
  const usage = {
    input_tokens: 1200,
    output_tokens: 340,
    cached_tokens: 200,
    total_tokens: 1540,
  }
  const started = (run: string, at: string): Item => ({
    type: "run_started",
    id: `${run}.started`,
    run_id: run,
    at,
  })
  const completed = (run: string, at: string): Item => ({
    type: "run_completed",
    id: `${run}.ended`,
    run_id: run,
    at,
    usage,
  })
  // A thread's history as Go stores it: the start after the member's message,
  // the end after the run's own items.
  const history: Item[] = [
    task("ev_1", "Plan the launch", "2026-10-16T09:12:00Z"),
    started("run_a", "2026-10-16T09:12:03.123456Z"),
    { type: "tool_call", id: "ev_2", call_id: "c1", tool: "web_search" },
    { type: "tool_result", id: "ev_3", call_id: "c1", tool: "web_search" },
    answer("ev_4", "Here is the plan.", "2026-10-16T09:12:08Z"),
    completed("run_a", "2026-10-16T09:12:09.623456Z"),
  ]

  it("reads a past run's duration and usage from history alone", () => {
    const [turn] = turnsOf(history)
    expect(turn.runs).toEqual([
      {
        runId: "run_a",
        startedAt: "2026-10-16T09:12:03.123456Z",
        endedAt: "2026-10-16T09:12:09.623456Z",
        usage,
      },
    ])
    expect(turnDuration(turn)).toBe(6_500)
    expect(turnUsage(turn)).toEqual(usage)
    expect(turnStatus(turn, { live: false, waiting: new Set() })).toBe("done")
  })

  it("reads the same run from a stream alone", () => {
    const streamed: Item[] = [
      task("ev_1", "Plan the launch"),
      started("run_a", "2026-10-16T09:12:03Z"),
      { type: "delta", author: "Leo", text: "Here" },
      answer("ev_4", "Here is the plan."),
      completed("run_a", "2026-10-16T09:12:09Z"),
    ]
    const turns = turnsOf(streamed)
    expect(turns).toHaveLength(1)
    expect(turns[0].runs).toHaveLength(1)
    expect(turnDuration(turns[0])).toBe(6_000)
    expect(turnUsage(turns[0])?.total_tokens).toBe(1540)
  })

  it("shows a run that was streamed and is also in history once", () => {
    const live: Item[] = [
      started("run_a", "2026-10-16T09:12:03.123456Z"),
      answer("ev_4", "Here is the plan.", "2026-10-16T09:12:08Z"),
      completed("run_a", "2026-10-16T09:12:09.623456Z"),
    ]
    // Merged as buildTimeline merges a refetched history with what streamed.
    expect(unseenLiveItems(history, live)).toEqual([])
    const entries = buildTimeline(history, live)
    const turns = groupTurns(entries, [
      ...history,
      ...unseenLiveItems(history, live),
    ])
    expect(turns[0].runs).toHaveLength(1)
    // One list that holds both copies, such as a transcript a reconcile went
    // wrong on, still shows the run once.
    const doubled = [...history, ...live]
    const [turn] = groupTurns(buildTimeline(doubled), doubled)
    expect(turn.runs).toHaveLength(1)
    expect(turnDuration(turn)).toBe(6_500)
    expect(turnUsage(turn)?.total_tokens).toBe(1540)
    expect(
      buildTimeline(doubled).filter((entry) => entry.kind === "message")
    ).toHaveLength(2)
  })

  it("keeps a mid-run history's start once and pairs it with the streamed end", () => {
    const midRun = history.slice(0, 2)
    const live: Item[] = [
      started("run_a", "2026-10-16T09:12:03.123456Z"),
      answer("ev_4", "Done."),
      completed("run_a", "2026-10-16T09:12:05.123456Z"),
    ]
    const items = [...midRun, ...unseenLiveItems(midRun, live)]
    expect(items.filter((item) => item.type === "run_started")).toHaveLength(1)
    const [turn] = groupTurns(buildTimeline(items), items)
    expect(turn.runs).toHaveLength(1)
    expect(turnDuration(turn)).toBe(2_000)
  })

  it("names a failed run by its code, with its usage and duration", () => {
    const failed: Item[] = [
      task("ev_1", "Research everything"),
      started("run_b", "2026-10-16T10:00:00Z"),
      {
        type: "run_failed",
        id: "run_b.ended",
        run_id: "run_b",
        at: "2026-10-16T10:10:00Z",
        error: "Leo took longer than 10m0s and was stopped.",
        code: "timeout",
        usage,
      },
    ]
    const entries = buildTimeline(failed)
    expect(entries.filter((entry) => entry.kind === "run_failed")).toEqual([
      expect.objectContaining({
        code: "timeout",
        error: "Leo took longer than 10m0s and was stopped.",
      }),
    ])
    const [turn] = groupTurns(entries, failed)
    expect(turn.runs[0]).toMatchObject({ failed: true, code: "timeout" })
    expect(turnStatus(turn, { live: false, waiting: new Set() })).toBe("failed")
    expect(turnFailureCode(turn)).toBe("timeout")
    expect(runFailureWords(turnFailureCode(turn)).label).toBe("Timed out")
    expect(turnDuration(turn)).toBe(600_000)
    expect(turnUsage(turn)?.total_tokens).toBe(1540)
  })

  it("says each failure code in plain words, and anything else as Failed", () => {
    expect(runFailureWords("interrupted").label).toBe("Interrupted")
    expect(runFailureWords("stopped")).toEqual({
      label: "Stopped",
      words: "You stopped this run.",
    })
    expect(runFailureWords("cancelled").label).toBe("Cancelled")
    expect(runFailureWords("taken_over").label).toBe("Taken over")
    expect(runFailureWords("error").label).toBe("Failed")
    expect(runFailureWords("run_in_progress").label).toBe("Failed")
    expect(runFailureWords(undefined).words).toBe(
      "Your agent couldn't answer. Try again."
    )
  })

  it("lets Go's stored end replace the failure a broken stream guessed", () => {
    const items: Item[] = [
      task("ev_1", "Plan the launch"),
      started("run_a", "2026-10-16T09:12:00Z"),
      // The Durable Object's own words when the stream broke: no id.
      { type: "run_failed", run_id: "run_a", error: "The answer was cut off." },
      answer("ev_4", "Here is the plan."),
      completed("run_a", "2026-10-16T09:12:30Z"),
    ]
    const entries = buildTimeline(items)
    expect(entries.some((entry) => entry.kind === "run_failed")).toBe(false)
    const [turn] = groupTurns(entries, items)
    expect(turn.runs).toEqual([
      {
        runId: "run_a",
        startedAt: "2026-10-16T09:12:00Z",
        endedAt: "2026-10-16T09:12:30Z",
        usage,
      },
    ])
    expect(turnStatus(turn, { live: false, waiting: new Set() })).toBe("done")
  })

  it("keeps a stored end over a later guessed one", () => {
    const items: Item[] = [
      ...history,
      { type: "run_failed", run_id: "run_a", error: "The answer was cut off." },
    ]
    const entries = buildTimeline(items)
    expect(entries.some((entry) => entry.kind === "run_failed")).toBe(false)
    expect(groupTurns(entries, items)[0].runs).toHaveLength(1)
  })

  it("adds up only the runs' own time, not a wait between them", () => {
    const [turn] = turnsOf([
      task("ev_1", "Send it"),
      started("r1", "2026-10-16T09:00:00Z"),
      completed("r1", "2026-10-16T09:00:05Z"),
      started("r2", "2026-10-16T09:30:00Z"),
      completed("r2", "2026-10-16T09:00:00Z"),
    ])
    // r2 ends before it starts: not a duration to trust.
    expect(turnDuration(turn)).toBeUndefined()
    const [waited] = turnsOf([
      task("ev_1", "Send it"),
      started("r1", "2026-10-16T09:00:00Z"),
      completed("r1", "2026-10-16T09:00:05Z"),
      started("r2", "2026-10-16T09:30:00Z"),
      completed("r2", "2026-10-16T09:30:07Z"),
    ])
    expect(turnDuration(waited)).toBe(12_000)
    expect(turnUsage(waited)?.cached_tokens).toBe(400)
  })

  it("has no duration for a start that never ended, or a thread without brackets", () => {
    const [orphan] = turnsOf(history.slice(0, 2))
    expect(turnDuration(orphan)).toBeUndefined()
    const [old] = turnsOf([
      task("ev_1", "Hi", "2026-10-15T09:00:00Z"),
      answer("ev_2", "Hello", "2026-10-15T09:00:04Z"),
    ])
    expect(old.runs).toEqual([])
    expect(turnDuration(old)).toBeUndefined()
  })
})

describe("turnStatus", () => {
  const none = new Set<string>()

  it("is working while the last turn's run may go on", () => {
    const [turn] = turnsOf([task("ev_1", "Hi")])
    expect(turnStatus(turn, { live: true, waiting: none })).toBe("working")
  })

  it("waits for the member before anything else", () => {
    const [turn] = turnsOf([
      task("ev_1", "Email Sam"),
      {
        type: "confirmation",
        id: "ev_2",
        call_id: "k1",
        tool: "send_email",
        status: "pending",
      },
    ])
    expect(turnStatus(turn, { live: true, waiting: new Set(["k1"]) })).toBe(
      "waiting"
    )
  })

  it("is done once answered, failed when its run failed, interrupted when cut short", () => {
    const [done] = turnsOf([task("ev_1", "Hi"), answer("ev_2", "Hello")])
    expect(turnStatus(done, { live: false, waiting: none })).toBe("done")

    const [failed] = turnsOf([
      task("ev_1", "Hi"),
      { type: "run_failed", run_id: "r1", error: "Model unavailable" },
    ])
    expect(turnStatus(failed, { live: false, waiting: none })).toBe("failed")

    const [cut] = turnsOf([
      task("ev_1", "Hi"),
      { type: "delta", author: "Leo", text: "Hel" },
    ])
    expect(turnStatus(cut, { live: false, waiting: none })).toBe("interrupted")

    const [unanswered] = turnsOf([task("ev_1", "Hi")])
    expect(turnStatus(unanswered, { live: false, waiting: none })).toBe(
      "interrupted"
    )
  })
})

describe("toolKeyArgument", () => {
  it("names what a call was about", () => {
    expect(toolKeyArgument({ query: "EU VAT rates", limit: 5 })).toBe(
      "EU VAT rates"
    )
    expect(toolKeyArgument({ channel: "#eu-launch" })).toBe("#eu-launch")
    expect(toolKeyArgument({ limit: 5 })).toBeUndefined()
    expect(toolKeyArgument(undefined)).toBeUndefined()
  })

  it("reads an app call's own arguments first", () => {
    expect(
      toolKeyArgument({
        toolkit: "slack",
        tool: "SLACK_SEARCH_MESSAGES",
        arguments: { query: "launch date" },
      })
    ).toBe("launch date")
  })

  it("keeps it to one short line", () => {
    const words = toolKeyArgument({ query: `a\n${"b".repeat(200)}` })
    expect(words?.includes("\n")).toBe(false)
    expect(words?.length).toBe(80)
  })
})

describe("openedLinks", () => {
  it("lists the web addresses calls were given, once each", () => {
    const entries = buildTimeline([
      {
        type: "tool_call",
        call_id: "c1",
        tool: "browse",
        arguments: { url: "https://example.com/a" },
      },
      {
        type: "tool_call",
        call_id: "c2",
        tool: "browse",
        arguments: {
          urls: ["https://example.com/a", "javascript:alert(1)", "ftp://x"],
        },
      },
    ])
    expect(openedLinks(entries).map((url) => url.href)).toEqual([
      "https://example.com/a",
    ])
  })
})
