import { describe, expect, it } from "vitest"
import {
  artifactFileName,
  buildTimeline,
  confirmationHint,
  humanizeToolName,
  isImageArtifact,
  pendingConfirmationIds,
  unseenLiveItems,
} from "./timeline"
import type { ToolEntry } from "./timeline"
import type { Item } from "./client"

function toolOf(items: Item[]): ToolEntry | undefined {
  return buildTimeline(items).find(
    (entry): entry is ToolEntry => entry.kind === "tool"
  )
}

describe("buildTimeline", () => {
  it("renders the member's message and the agent's finished message in order", () => {
    expect(
      buildTimeline([
        { type: "message", id: "i1", author: "member", text: "hi" },
        { type: "message", id: "i2", author: "Krude", text: "hello" },
      ])
    ).toEqual([
      {
        kind: "message",
        key: "message:i1",
        id: "i1",
        author: "member",
        role: "member",
        text: "hi",
        state: "complete",
      },
      {
        kind: "message",
        key: "message:i2",
        id: "i2",
        author: "Krude",
        role: "agent",
        text: "hello",
        state: "complete",
      },
    ])
  })

  it("folds deltas into a single growing draft", () => {
    expect(
      buildTimeline([
        { type: "delta", author: "Krude", text: "Hel" },
        { type: "delta", author: "Krude", text: "lo" },
      ])
    ).toEqual([
      {
        kind: "message",
        key: "draft:0",
        author: "Krude",
        role: "agent",
        text: "Hello",
        state: "streaming",
      },
    ])
  })

  it("replaces the draft with the finished message instead of duplicating it", () => {
    const entries = buildTimeline([
      { type: "delta", author: "Krude", text: "Hel" },
      { type: "delta", author: "Krude", text: "lo" },
      { type: "message", id: "i1", author: "Krude", text: "Hello" },
    ])
    expect(entries).toHaveLength(1)
    expect(entries[0]).toMatchObject({
      key: "message:i1",
      id: "i1",
      state: "complete",
      text: "Hello",
    })
  })

  it("shows a message's error as part of the entry", () => {
    expect(
      buildTimeline([
        {
          type: "message",
          id: "i1",
          author: "Krude",
          error: "MAX_TOKENS: the model stopped early",
        },
      ])[0]
    ).toMatchObject({ text: "", error: "MAX_TOKENS: the model stopped early" })
  })

  it("pairs a tool_call with its later tool_result by call_id", () => {
    expect(
      buildTimeline([
        {
          type: "tool_call",
          id: "e1",
          call_id: "call_1",
          tool: "sandbox_execute_code",
          arguments: { code: "print(42)" },
        },
        {
          type: "tool_result",
          id: "e2",
          call_id: "call_1",
          tool: "sandbox_execute_code",
        },
      ])
    ).toEqual([
      {
        kind: "tool",
        key: "tool:call_1",
        callId: "call_1",
        id: "e1",
        tool: "sandbox_execute_code",
        arguments: { code: "print(42)" },
        state: "done",
      },
    ])
  })

  it("marks a failed tool_result on its call", () => {
    expect(
      toolOf([
        { type: "tool_call", call_id: "call_1", tool: "researcher" },
        {
          type: "tool_result",
          call_id: "call_1",
          tool: "researcher",
          failed: true,
          error: "timed out",
        },
      ])
    ).toMatchObject({ error: "timed out", state: "failed" })
  })

  it("still shows a tool_result that arrives with no matching call", () => {
    expect(
      buildTimeline([
        { type: "tool_result", call_id: "call_9", tool: "knowledge_search" },
      ])
    ).toEqual([
      {
        kind: "tool",
        key: "tool:call_9",
        callId: "call_9",
        tool: "knowledge_search",
        state: "done",
      },
    ])
  })

  it("keeps multiple concurrent tool calls separate by call_id", () => {
    const entries = buildTimeline([
      { type: "tool_call", call_id: "call_1", tool: "a" },
      { type: "tool_call", call_id: "call_2", tool: "b" },
      { type: "tool_result", call_id: "call_2", tool: "b" },
    ])
    expect(entries).toHaveLength(2)
    expect(entries[0]).toMatchObject({ callId: "call_1", state: "running" })
    expect(entries[1]).toMatchObject({ callId: "call_2", state: "done" })
  })

  it("renders a confirmation with its hint and arguments", () => {
    expect(
      buildTimeline([
        {
          type: "confirmation",
          id: "e3",
          call_id: "confirm_1",
          tool_call_id: "call_1",
          tool: "slack_post_message",
          arguments: { channel: "#general" },
          hint: "This posts a message visible to the channel.",
          status: "pending",
        },
      ])
    ).toEqual([
      {
        kind: "confirmation",
        key: "confirmation:confirm_1",
        callId: "confirm_1",
        toolCallId: "call_1",
        id: "e3",
        tool: "slack_post_message",
        arguments: { channel: "#general" },
        hint: "This posts a message visible to the channel.",
        status: "pending",
      },
    ])
  })

  it("defaults a confirmation with no status to pending", () => {
    expect(
      buildTimeline([{ type: "confirmation", call_id: "confirm_1" }])[0]
    ).toMatchObject({ status: "pending" })
  })

  it("renders nothing for a run's start and end, and a notice for its failure", () => {
    expect(
      buildTimeline([
        { type: "run_started", run_id: "run_1" },
        { type: "run_completed", run_id: "run_1" },
        {
          type: "run_failed",
          run_id: "run_2",
          error: "Krude took longer than 10m0s and was stopped.",
        },
      ])
    ).toEqual([
      {
        kind: "run_failed",
        key: "run_failed:run_2",
        runId: "run_2",
        error: "Krude took longer than 10m0s and was stopped.",
      },
    ])
  })

  it("leaves out the Durable Object's session_expired signal", () => {
    expect(
      buildTimeline([
        { type: "run_failed", error: "expired", code: "session_expired" },
      ])
    ).toEqual([])
  })

  it("renders an artifact a run kept", () => {
    expect(
      buildTimeline([
        {
          type: "artifact",
          id: "item_1",
          at: "2026-09-18T10:00:00Z",
          author: "Krude",
          artifact: "summary.png",
          version: 2,
        },
      ])
    ).toEqual([
      {
        kind: "artifact",
        key: "artifact:item_1/summary.png@2",
        id: "item_1",
        at: "2026-09-18T10:00:00Z",
        author: "Krude",
        name: "summary.png",
        version: 2,
      },
    ])
  })

  it("tells apart the files one stored event kept", () => {
    const keys = buildTimeline([
      { type: "artifact", id: "event_1", artifact: "chart.png", version: 1 },
      { type: "artifact", id: "event_1", artifact: "data.csv", version: 1 },
    ]).map((entry) => entry.key)
    expect(new Set(keys).size).toBe(2)
  })

  it("leaves an artifact's version out when Go did, which reads the latest", () => {
    expect(
      buildTimeline([{ type: "artifact", artifact: "notes.txt" }])[0]
    ).toMatchObject({ name: "notes.txt", version: undefined })
  })

  it("marks a message from the member's apps as their own kind of notice", () => {
    expect(
      buildTimeline([
        {
          type: "message",
          id: "e1",
          author: "apps",
          text: "New calendar event",
        },
        { type: "message", id: "e2", author: "Jarvis", text: "Noted." },
      ]).map((entry) => entry.kind === "message" && entry.role)
    ).toEqual(["apps", "agent"])
  })

  it("shows a tool call waiting for approval while its confirmation is pending", () => {
    expect(
      toolOf([
        { type: "tool_call", call_id: "call_1", tool: "slack_post_message" },
        {
          type: "confirmation",
          call_id: "confirm_1",
          tool_call_id: "call_1",
          tool: "slack_post_message",
          status: "pending",
        },
      ])
    ).toMatchObject({ state: "waiting_approval" })
  })

  it("returns a waiting tool call to running once its confirmation is approved", () => {
    expect(
      toolOf([
        { type: "tool_call", call_id: "call_1", tool: "slack_post_message" },
        {
          type: "confirmation",
          call_id: "confirm_1",
          tool_call_id: "call_1",
          status: "approved",
        },
      ])
    ).toMatchObject({ state: "running" })
  })

  it("still resolves to done once approved and its tool_result arrives", () => {
    expect(
      toolOf([
        { type: "tool_call", call_id: "call_1", tool: "slack_post_message" },
        {
          type: "confirmation",
          call_id: "confirm_1",
          tool_call_id: "call_1",
          status: "approved",
        },
        { type: "tool_result", call_id: "call_1", tool: "slack_post_message" },
      ])
    ).toMatchObject({ state: "done" })
  })

  it("marks a declined tool call as declined when no result ever arrives", () => {
    expect(
      toolOf([
        { type: "tool_call", call_id: "call_1", tool: "slack_post_message" },
        {
          type: "confirmation",
          call_id: "confirm_1",
          tool_call_id: "call_1",
          status: "declined",
        },
      ])
    ).toMatchObject({ state: "declined" })
  })

  it("keeps a declined call declined when ADK reports the rejection as a failed result", () => {
    const tool = toolOf([
      { type: "tool_call", call_id: "call_1", tool: "slack_post_message" },
      {
        type: "confirmation",
        call_id: "confirm_1",
        tool_call_id: "call_1",
        status: "declined",
      },
      {
        type: "tool_result",
        call_id: "call_1",
        tool: "slack_post_message",
        failed: true,
        error: 'error tool "slack_post_message" call is rejected',
      },
    ])
    expect(tool?.state).toBe("declined")
    expect(tool?.error).toBeUndefined()
  })

  it("leaves a settled tool call's state alone if a confirmation still names it", () => {
    expect(
      toolOf([
        { type: "tool_call", call_id: "call_1", tool: "slack_post_message" },
        { type: "tool_result", call_id: "call_1", tool: "slack_post_message" },
        {
          type: "confirmation",
          call_id: "confirm_1",
          tool_call_id: "call_1",
          status: "approved",
        },
      ])
    ).toMatchObject({ state: "done" })
  })

  it("updates the first card when a confirmation's answer arrives later", () => {
    const entries = buildTimeline([
      { type: "tool_call", call_id: "call_1", tool: "send_email" },
      {
        type: "confirmation",
        id: "e3",
        call_id: "confirm_1",
        tool_call_id: "call_1",
        tool: "send_email",
        hint: "Sends an email.",
        status: "pending",
      },
      { type: "confirmation", call_id: "confirm_1", status: "approved" },
    ])
    expect(entries.map((entry) => entry.kind)).toEqual(["tool", "confirmation"])
    expect(entries[1]).toMatchObject({
      status: "approved",
      hint: "Sends an email.",
      tool: "send_email",
    })
    expect(entries[0]).toMatchObject({ state: "running" })
  })

  it("never lets two runs' deltas share a bubble", () => {
    const entries = buildTimeline([
      { type: "run_started", run_id: "run_1" },
      { type: "delta", author: "Krude", text: "Looking into" },
      {
        type: "run_failed",
        run_id: "run_1",
        error: "Krude ran into a problem and stopped. Try again.",
      },
      { type: "run_started", run_id: "run_2" },
      { type: "delta", author: "Krude", text: "Here it is" },
    ])
    expect(
      entries.map((entry) =>
        entry.kind === "message" ? [entry.text, entry.state] : entry.kind
      )
    ).toEqual([
      ["Looking into", "interrupted"],
      "run_failed",
      ["Here it is", "streaming"],
    ])
    expect(new Set(entries.map((entry) => entry.key)).size).toBe(3)
  })

  it("marks a draft interrupted when its run completes without its message", () => {
    expect(
      buildTimeline([
        { type: "delta", author: "Krude", text: "Partial" },
        { type: "run_completed", run_id: "run_1" },
      ])[0]
    ).toMatchObject({ state: "interrupted", text: "Partial" })
  })

  it("leaves a call with no result interrupted once its run completes", () => {
    const entries = buildTimeline([
      { type: "run_started", run_id: "run_1" },
      { type: "tool_call", id: "e1", call_id: "call_1", tool: "web_search" },
      { type: "tool_call", id: "e2", call_id: "call_2", tool: "read_file" },
      { type: "tool_result", id: "e3", call_id: "call_1", tool: "web_search" },
      { type: "run_completed", run_id: "run_1" },
    ])
    expect(
      entries.map((entry) => entry.kind === "tool" && entry.state)
    ).toEqual(["done", "interrupted"])
  })

  it("leaves a call with no result interrupted once its run fails, and still says the run failed", () => {
    const entries = buildTimeline([
      { type: "tool_call", id: "e1", call_id: "call_1", tool: "web_search" },
      { type: "run_failed", run_id: "run_1", error: "The run was cancelled." },
    ])
    expect(entries.map((entry) => entry.kind)).toEqual(["tool", "run_failed"])
    expect(entries[0]).toMatchObject({ state: "interrupted" })
  })

  it("finds a call with no call id to interrupt it", () => {
    expect(
      toolOf([
        { type: "tool_call", id: "e1", tool: "web_search" },
        { type: "run_completed", run_id: "run_1" },
      ])
    ).toMatchObject({ state: "interrupted" })
  })

  it("leaves a call running while its run has not ended", () => {
    expect(
      toolOf([
        { type: "run_started", run_id: "run_1" },
        { type: "tool_call", id: "e1", call_id: "call_1", tool: "web_search" },
      ])
    ).toMatchObject({ state: "running" })
  })

  it("does not interrupt what an earlier run's end says nothing of", () => {
    const entries = buildTimeline([
      { type: "run_started", run_id: "run_1" },
      { type: "tool_call", id: "e1", call_id: "call_1", tool: "web_search" },
      { type: "tool_result", id: "e2", call_id: "call_1", tool: "web_search" },
      { type: "run_completed", run_id: "run_1" },
      { type: "run_started", run_id: "run_2" },
      { type: "tool_call", id: "e3", call_id: "call_2", tool: "read_file" },
    ])
    expect(
      entries.map((entry) => entry.kind === "tool" && entry.state)
    ).toEqual(["done", "running"])
  })

  it("leaves a call waiting for approval waiting when its run completes", () => {
    expect(
      toolOf([
        { type: "run_started", run_id: "run_1" },
        { type: "tool_call", call_id: "call_1", tool: "slack_post_message" },
        {
          type: "confirmation",
          call_id: "confirm_1",
          tool_call_id: "call_1",
          tool: "slack_post_message",
          status: "pending",
        },
        { type: "run_completed", run_id: "run_1" },
      ])
    ).toMatchObject({ state: "waiting_approval" })
  })

  it("runs an approved call again in the next run, and interrupts it if that run ends without a result", () => {
    const asked: Item[] = [
      { type: "run_started", run_id: "run_1" },
      { type: "tool_call", call_id: "call_1", tool: "slack_post_message" },
      {
        type: "confirmation",
        call_id: "confirm_1",
        tool_call_id: "call_1",
        status: "pending",
      },
      { type: "run_completed", run_id: "run_1" },
      // The answer is echoed before Go's stream starts.
      { type: "confirmation", call_id: "confirm_1", status: "approved" },
      { type: "run_started", run_id: "run_2" },
    ]
    expect(toolOf(asked)).toMatchObject({ state: "running" })
    expect(
      toolOf([...asked, { type: "run_failed", error: "Cancelled." }])
    ).toMatchObject({ state: "interrupted" })
    expect(
      toolOf([
        ...asked,
        { type: "tool_result", call_id: "call_1", tool: "slack_post_message" },
        { type: "run_completed", run_id: "run_2" },
      ])
    ).toMatchObject({ state: "done" })
  })

  it("leaves a stopped run's call interrupted once the member speaks again, marker or none", () => {
    const entries = buildTimeline([
      { type: "message", id: "e1", author: "member", text: "Look it up" },
      { type: "run_started", run_id: "run_1" },
      { type: "tool_call", id: "e2", call_id: "call_1", tool: "web_search" },
      // Stopped here: no run_completed came.
      { type: "message", id: "e3", author: "member", text: "Never mind" },
      { type: "run_started", run_id: "run_2" },
      { type: "tool_call", id: "e4", call_id: "call_2", tool: "read_file" },
    ])
    expect(
      entries.map((entry) => entry.kind === "tool" && entry.state)
    ).toEqual([false, "interrupted", false, "running"])
  })

  it("does not read another author's message, or a draft, as the member speaking", () => {
    expect(
      toolOf([
        { type: "tool_call", id: "e1", call_id: "call_1", tool: "web_search" },
        { type: "message", id: "e2", author: "apps", text: "New email" },
        { type: "message", id: "e3", author: "Krude", text: "Working on it" },
        { type: "delta", author: "Krude", text: "More" },
      ])
    ).toMatchObject({ state: "running" })
  })

  it("lets a result that does arrive settle a call whatever the markers say", () => {
    expect(
      toolOf([
        { type: "tool_call", call_id: "call_1", tool: "web_search" },
        { type: "run_completed", run_id: "run_1" },
        { type: "tool_result", call_id: "call_1", tool: "web_search" },
      ])
    ).toMatchObject({ state: "done" })
  })

  it("starts a new bubble when another agent takes the turn", () => {
    const entries = buildTimeline([
      { type: "delta", author: "Krude", text: "Asking the researcher" },
      { type: "delta", author: "researcher", text: "Found three" },
      { type: "message", id: "e9", author: "researcher", text: "Found three." },
    ])
    expect(
      entries.map((entry) =>
        entry.kind === "message" ? [entry.author, entry.state] : entry.kind
      )
    ).toEqual([
      ["Krude", "interrupted"],
      ["researcher", "complete"],
    ])
  })

  it("never lets a member's message replace an agent's draft", () => {
    const entries = buildTimeline([
      { type: "delta", author: "Krude", text: "Hel" },
      { type: "message", id: "e2", author: "member", text: "Stop" },
    ])
    expect(entries).toMatchObject([
      { author: "Krude", text: "Hel", state: "interrupted" },
      { author: "member", text: "Stop", state: "complete" },
    ])
  })

  it("keeps every key unique even when the same items arrive twice", () => {
    const items: Item[] = [
      { type: "message", id: "e1", author: "member", text: "hi" },
      { type: "tool_call", id: "e2", call_id: "c1", tool: "search" },
      { type: "tool_result", id: "e3", call_id: "c1", tool: "search" },
      { type: "artifact", id: "e3", artifact: "a.png", version: 1 },
      { type: "message", author: "member", text: "no id" },
      { type: "message", author: "member", text: "no id either" },
    ]
    const entries = buildTimeline([...items, ...items])
    const keys = entries.map((entry) => entry.key)
    expect(new Set(keys).size).toBe(keys.length)
    // Items with an identity show once; the two id-less messages each twice.
    expect(entries).toHaveLength(7)
  })
})

describe("buildTimeline with live items", () => {
  const member: Item = {
    type: "message",
    id: "e1",
    author: "member",
    text: "hi",
  }
  const call: Item = {
    type: "tool_call",
    id: "e2",
    call_id: "c1",
    tool: "knowledge_search",
  }
  const result: Item = {
    type: "tool_result",
    id: "e3",
    call_id: "c1",
    tool: "knowledge_search",
  }
  const answer: Item = {
    type: "message",
    id: "e4",
    author: "Krude",
    text: "Hello",
  }
  const deltas: Item[] = [
    { type: "delta", author: "Krude", text: "Hel" },
    { type: "delta", author: "Krude", text: "lo" },
  ]
  const run: Item[] = [
    { type: "run_started", run_id: "r1" },
    member,
    call,
    result,
    ...deltas,
    answer,
    { type: "run_completed", run_id: "r1" },
  ]

  it("shows a finished run once when the refetched history already holds it", () => {
    const timeline = buildTimeline([member, call, result, answer], run)
    expect(timeline.map((entry) => entry.kind)).toEqual([
      "message",
      "tool",
      "message",
    ])
    expect(
      timeline.some(
        (entry) => entry.kind === "message" && entry.state !== "complete"
      )
    ).toBe(false)
  })

  it("keeps what a thread refetched mid-run does not hold yet", () => {
    const timeline = buildTimeline([member, call], run.slice(0, 6))
    expect(timeline).toHaveLength(3)
    expect(timeline[1]).toMatchObject({ kind: "tool", state: "done" })
    expect(timeline[2]).toMatchObject({
      kind: "message",
      state: "streaming",
      text: "Hello",
    })
  })

  it("keeps a draft's key while the history under it is refetched", () => {
    const live = run.slice(0, 6)
    const before = buildTimeline([], live).at(-1)
    const after = buildTimeline([member, call], live).at(-1)
    expect(before?.key).toBe(after?.key)
  })
})

describe("unseenLiveItems", () => {
  const member: Item = {
    type: "message",
    id: "e1",
    author: "member",
    text: "hi",
  }
  const answer: Item = {
    type: "message",
    id: "e4",
    author: "Krude",
    text: "Hello",
  }
  const deltas: Item[] = [
    { type: "delta", author: "Krude", text: "Hel" },
    { type: "delta", author: "Krude", text: "lo" },
  ]

  it("keeps every item when the history holds none of the run", () => {
    const run: Item[] = [
      { type: "run_started", run_id: "r1" },
      member,
      ...deltas,
      answer,
      { type: "run_completed", run_id: "r1" },
    ]
    expect(
      unseenLiveItems(
        [{ type: "message", id: "e0", author: "Krude", text: "Earlier" }],
        run
      )
    ).toEqual(run)
  })

  it("drops what the history holds, with the deltas that drafted it", () => {
    expect(
      unseenLiveItems([member, answer], [member, ...deltas, answer])
    ).toEqual([])
  })

  it("drops a held message's deltas even when its event put the tool call first", () => {
    const toolFirst: Item = {
      type: "tool_call",
      id: "e4",
      call_id: "c2",
      tool: "search",
    }
    const text: Item = { ...answer, text: "Hello" }
    expect(
      unseenLiveItems([toolFirst, text], [...deltas, toolFirst, text])
    ).toEqual([])
  })

  it("moves a tool call ahead of the deltas drafting its event's text, as history orders it", () => {
    const toolFirst: Item = {
      type: "tool_call",
      id: "e4",
      call_id: "c2",
      tool: "search",
    }
    expect(unseenLiveItems([], [...deltas, toolFirst, answer])).toEqual([
      toolFirst,
      ...deltas,
      answer,
    ])
  })

  it("keeps a failed run's deltas before its failure", () => {
    const failed: Item = { type: "run_failed", run_id: "r1", error: "boom" }
    expect(unseenLiveItems([member], [member, ...deltas, failed])).toEqual([
      ...deltas,
      failed,
    ])
  })
})

describe("pendingConfirmationIds", () => {
  const asked: Item = {
    type: "confirmation",
    id: "e3",
    call_id: "confirm_1",
    status: "pending",
  }

  it("starts from what Go last reported", () => {
    expect(
      pendingConfirmationIds({
        items: [asked],
        pending_confirmations: ["confirm_1"],
      })
    ).toEqual(["confirm_1"])
    expect(pendingConfirmationIds(undefined)).toEqual([])
  })

  it("adds confirmations asked since, and drops ones answered since", () => {
    expect(
      pendingConfirmationIds(
        { items: [asked], pending_confirmations: ["confirm_1"] },
        [
          { type: "confirmation", call_id: "confirm_1", status: "approved" },
          {
            type: "confirmation",
            id: "e7",
            call_id: "confirm_2",
            status: "pending",
          },
        ]
      )
    ).toEqual(["confirm_2"])
  })

  it("does not bring back an answered confirmation from a stale streamed copy", () => {
    expect(
      pendingConfirmationIds(
        {
          items: [{ ...asked, status: "approved" }],
          pending_confirmations: [],
        },
        [asked]
      )
    ).toEqual([])
  })
})

describe("humanizeToolName", () => {
  it("turns a snake_case tool name into a sentence-cased phrase", () => {
    expect(humanizeToolName("sandbox_execute_code")).toBe(
      "Sandbox execute code"
    )
    expect(humanizeToolName("apps_run_write_tool")).toBe("Apps run write tool")
    expect(humanizeToolName("researcher")).toBe("Researcher")
  })
})

describe("confirmationHint", () => {
  it("keeps a hint the backend already wrote", () => {
    expect(
      confirmationHint("slack_post_message", "This posts to #general.")
    ).toBe("This posts to #general.")
  })

  it("writes its own words when the backend left the developer-facing default out", () => {
    const words =
      "Your agent wants to use Slack post message. It only runs if you approve."
    expect(confirmationHint("slack_post_message", undefined)).toBe(words)
    expect(confirmationHint("slack_post_message", "")).toBe(words)
  })

  it("still says something sensible with no tool name at all", () => {
    expect(confirmationHint(undefined, undefined)).toBe(
      "Your agent wants to use a tool. It only runs if you approve."
    )
  })
})

describe("artifactFileName", () => {
  it("keeps a plain name as is", () => {
    expect(artifactFileName("screenshot.png")).toBe("screenshot.png")
  })

  it("drops the user: prefix, as Go does when it names the download", () => {
    expect(artifactFileName("user:notes.txt")).toBe("notes.txt")
  })

  it("takes the trailing segment of a path-shaped name", () => {
    expect(artifactFileName("reports/2026/summary.csv")).toBe("summary.csv")
  })
})

describe("isImageArtifact", () => {
  it("recognizes every inline-rendered image extension", () => {
    for (const name of [
      "a.png",
      "a.jpg",
      "a.jpeg",
      "a.gif",
      "a.webp",
      "A.PNG",
    ]) {
      expect(isImageArtifact(name)).toBe(true)
    }
  })

  it("treats everything else as a download", () => {
    for (const name of ["a.pdf", "a.txt", "a.csv", "noextension"]) {
      expect(isImageArtifact(name)).toBe(false)
    }
  })

  it("looks at the file name, not a user: prefix, for the extension", () => {
    expect(isImageArtifact("user:screenshot.png")).toBe(true)
  })
})
