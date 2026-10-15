import { describe, expect, it } from "vitest"
import {
  buildTimeline,
  groupTurns,
  openedLinks,
  toolKeyArgument,
  turnStatus,
  turnTimes,
  turnUsage,
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
