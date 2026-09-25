import { describe, expect, it } from "vitest"
import {
  chatMessagesToItems,
  itemPart,
  itemsToChatMessages,
  parseThreadAgentName,
  reconcileChatMessages,
} from "@/lib/threads/chat-messages"
import type { ThreadChatMessage } from "@/lib/threads/chat-messages"
import type { Item } from "@/lib/threads/client"
import { buildTimeline } from "@/lib/threads/timeline"
import type { MessageEntry } from "@/lib/threads/timeline"
import {
  isProgressMarker,
  parseSpecialistAgentName,
  readableSpecialistItem,
  readableSpecialistItems,
  readableSpecialistStream,
  specialistAgentName,
  withoutProgressMarkers,
} from "./chat-messages"
import type { SpecialistResult } from "./result"

const result: SpecialistResult = {
  status: "needs_input",
  summary: "Which quarter should I review?",
  tools_used: ["crm_search"],
  artifacts: ["pipeline.csv"],
  handoffs: [{ target: "finance", request: "Check the forecast" }],
}
const answer: Item = {
  type: "message",
  id: "ev_2",
  at: "2026-09-23T10:00:02Z",
  author: "Sales",
  text: JSON.stringify(result),
}

/** What the page is shown of a run's items, in order. */
function shown(items: readonly Item[]): Item[] {
  const present = readableSpecialistStream()
  return items.flatMap((item) => {
    const seen = present(item)
    return seen ? [seen] : []
  })
}

describe("specialistAgentName", () => {
  it("names one Durable Object per specialist and reads the name back", () => {
    const name = specialistAgentName("org_1", "agt_m4M8CYOh3Su1k_rtSvFbjA")
    expect(name).toBe("org_1~agt_m4M8CYOh3Su1k_rtSvFbjA")
    expect(parseSpecialistAgentName(name)).toEqual({
      organizationId: "org_1",
      agentId: "agt_m4M8CYOh3Su1k_rtSvFbjA",
    })
  })

  it("refuses a name the Worker would not route", () => {
    for (const name of [
      "org_1",
      "org_1~",
      "~agt_1",
      "org_1~agt_1~thread_1",
      "org 1~agt_1",
      "org_1~agt/1",
      "org_1~agt%201",
    ]) {
      expect(parseSpecialistAgentName(name)).toBeUndefined()
    }
  })

  it("makes names in the thread chat's form, so each object's parser reads the other's", () => {
    // The Worker keeps them apart by path: `/agents/thread-chat-agent/...`
    // and `/agents/specialist-chat-agent/...` are different objects.
    expect(parseThreadAgentName(specialistAgentName("org_1", "agt_1"))).toEqual(
      { organizationId: "org_1", threadId: "agt_1" }
    )
  })
})

describe("readableSpecialistItem", () => {
  it("shows a work result's summary and keeps the result beside it", () => {
    expect(readableSpecialistItem(answer)).toEqual({
      ...answer,
      text: "Which quarter should I review?",
      result,
    })
  })

  it("reads a result in a code fence, and gives a status's words to an empty summary", () => {
    const fenced = readableSpecialistItem({
      ...answer,
      text: '```json\n{"status":"blocked","summary":"  "}\n```',
    })
    expect(fenced.text).toBe("I'm blocked and can't finish this.")
    expect(fenced.result?.status).toBe("blocked")
  })

  it("leaves everything else as it is", () => {
    const untouched: Item[] = [
      { ...answer, text: "Here is the review." },
      { ...answer, text: '{"summary":"no status"}' },
      { ...answer, author: "member" },
      { ...answer, author: "apps" },
      { type: "tool_call", id: "ev_1", call_id: "c1", tool: "crm_search" },
      { type: "delta", author: "Sales", text: '{"status":"completed"' },
    ]
    for (const item of untouched) {
      expect(readableSpecialistItem(item)).toBe(item)
    }
    const readable = readableSpecialistItem(answer)
    expect(readableSpecialistItem(readable)).toBe(readable)
  })
})

describe("readableSpecialistStream", () => {
  it("shows one progress marker for a draft of raw JSON, then the readable answer", () => {
    const run: Item[] = [
      { type: "run_started", run_id: "run_1" },
      { type: "delta", at: "t1", author: "Sales", text: '{"status":' },
      { type: "delta", at: "t2", author: "Sales", text: '"needs_input",' },
      { type: "delta", at: "t3", author: "Sales", text: '"summary":"Which' },
      answer,
      { type: "run_completed", run_id: "run_1" },
    ]
    expect(shown(run)).toEqual([
      { type: "run_started", run_id: "run_1" },
      { type: "delta", at: "t1", author: "Sales", text: "" },
      readableSpecialistItem(answer),
      { type: "run_completed", run_id: "run_1" },
    ])
  })

  it("starts a marker for each draft, as the timeline starts a bubble", () => {
    const run: Item[] = [
      { type: "delta", author: "Sales", text: "{" },
      { type: "tool_call", id: "ev_1", call_id: "c1", tool: "crm_search" },
      { type: "delta", author: "Sales", text: '"a"' },
      { type: "delta", author: "helper", text: "{" },
      { type: "message", id: "ev_3", author: "helper", text: "{}" },
      { type: "delta", author: "Sales", text: "{" },
      answer,
    ]
    expect(
      shown(run).map((item) =>
        isProgressMarker(item) ? `marker:${item.author}` : item.type
      )
    ).toEqual([
      "marker:Sales",
      "tool_call",
      "marker:helper",
      "message",
      "marker:Sales",
      "message",
    ])
  })

  it("keeps each run's own draft: a new stream starts with none", () => {
    const delta: Item = { type: "delta", author: "Sales", text: "{" }
    expect(shown([delta])).toHaveLength(1)
    expect(shown([delta])).toHaveLength(1)
  })

  it("renders as the specialist working, then its answer in the same place", () => {
    const live = shown([
      { type: "run_started", run_id: "run_1" },
      { type: "delta", author: "Sales", text: "{" },
    ])
    const [working] = buildTimeline(live)
    expect(working).toMatchObject({
      kind: "message",
      author: "Sales",
      text: "",
      state: "streaming",
    })

    const finished = buildTimeline([
      ...live,
      ...shown([answer, { type: "run_completed", run_id: "run_1" }]),
    ])
    // The finished message takes the draft's place: still the only entry.
    expect(finished).toHaveLength(1)
    const message = finished[0] as MessageEntry
    expect(message).toMatchObject({
      kind: "message",
      id: "ev_2",
      author: "Sales",
      text: "Which quarter should I review?",
      state: "complete",
      result,
    })
  })
})

describe("withoutProgressMarkers", () => {
  function assistant(items: Item[]): ThreadChatMessage {
    return { id: "a1", role: "assistant", parts: items.map(itemPart) }
  }

  it("stores a finished answer without its marker", () => {
    const live = assistant(
      shown([
        { type: "run_started", run_id: "run_1" },
        { type: "delta", author: "Sales", text: "{" },
        answer,
        { type: "run_completed", run_id: "run_1" },
      ])
    )
    expect(
      chatMessagesToItems([withoutProgressMarkers(live)]).map(
        (item) => item.type
      )
    ).toEqual(["run_started", "message", "run_completed"])
  })

  it("stores a run cut short as its failure alone, with no empty draft", () => {
    const cut = assistant([
      { type: "run_started", run_id: "run_1" },
      { type: "delta", author: "Sales", text: "" },
      { type: "run_failed", run_id: "run_1", error: "Cut off." },
    ])
    expect(
      buildTimeline(chatMessagesToItems([withoutProgressMarkers(cut)]))
    ).toEqual([
      {
        kind: "run_failed",
        key: "run_failed:run_1",
        runId: "run_1",
        error: "Cut off.",
      },
    ])
  })

  it("leaves a message with no marker as it was", () => {
    const member: ThreadChatMessage = {
      id: "m1",
      role: "user",
      parts: [{ type: "text", text: "Hello" }],
    }
    expect(withoutProgressMarkers(member)).toBe(member)
  })
})

describe("the transcript beside Go's history", () => {
  it("matches a stored readable answer with Go's raw one, and imports what it lacks readable", () => {
    const member: Item = {
      type: "message",
      id: "ev_1",
      author: "member",
      text: "Review the pipeline",
    }
    const stored = itemsToChatMessages(
      readableSpecialistItems([member, answer])
    )
    const later: Item = {
      type: "message",
      id: "ev_4",
      author: "Sales",
      text: JSON.stringify({ ...result, status: "completed", summary: "Q3." }),
    }
    const history = readableSpecialistItems([member, answer, later])

    expect(reconcileChatMessages(stored, history.slice(0, 2))).toBe(stored)
    const merged = reconcileChatMessages(stored, history)
    expect(chatMessagesToItems(merged).map((item) => item.text)).toEqual([
      "Review the pipeline",
      "Which quarter should I review?",
      "Q3.",
    ])
  })
})
