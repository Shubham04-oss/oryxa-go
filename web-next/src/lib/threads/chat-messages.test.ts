import { afterEach, describe, expect, it, vi } from "vitest"
import {
  chatMessagesToItems,
  itemPart,
  itemsToChatMessages,
  parseThreadAgentName,
  reconcileChatMessages,
  sessionExpiredCode,
  threadAgentName,
  withoutSupersededDeltas,
} from "./chat-messages"
import type { ThreadChatMessage } from "./chat-messages"
import { buildTimeline } from "./timeline"
import type { Item } from "./client"

afterEach(() => vi.unstubAllGlobals())

describe("Durable Object transcript", () => {
  it("imports an older conversation without losing tools or artifacts", () => {
    const items: Item[] = [
      { type: "message", id: "user-1", author: "member", text: "Make a chart" },
      {
        type: "tool_call",
        id: "tool-1",
        call_id: "call-1",
        tool: "make_chart",
      },
      {
        type: "tool_result",
        id: "tool-2",
        call_id: "call-1",
        tool: "make_chart",
      },
      { type: "artifact", id: "tool-2", artifact: "chart.png", version: 1 },
      { type: "message", id: "answer-1", author: "Krude", text: "Done" },
    ]
    expect(chatMessagesToItems(itemsToChatMessages(items))).toEqual(items)
  })

  it("keeps a member's message under its stored event's id", () => {
    const [member, answer] = itemsToChatMessages([
      { type: "message", id: "e1", author: "member", text: "Hi" },
      { type: "message", id: "e2", author: "Krude", text: "Hello" },
    ])
    expect(member).toEqual({
      id: "e1",
      role: "user",
      parts: [{ type: "text", text: "Hi" }],
    })
    expect(answer.role).toBe("assistant")
    expect(answer.id).toMatch(/^[0-9a-f]{32}$/)
  })

  it("names messages without crypto.randomUUID, which plain http on a LAN address lacks", () => {
    const getRandomValues = crypto.getRandomValues.bind(crypto)
    vi.stubGlobal("crypto", { getRandomValues })
    expect(() =>
      itemsToChatMessages([
        { type: "message", author: "member", text: "Hi" },
        { type: "message", id: "e2", author: "Krude", text: "Hello" },
      ])
    ).not.toThrow()
  })

  it("keeps the latest approval state in the original card", () => {
    const messages = itemsToChatMessages([
      {
        type: "confirmation",
        call_id: "approve-1",
        tool: "send_email",
        status: "pending",
        arguments: { to: "a@example.com" },
      },
    ])
    messages.push({
      id: "approval",
      role: "assistant",
      parts: [
        itemPart({
          type: "confirmation",
          call_id: "approve-1",
          status: "approved",
        }),
      ],
    })
    expect(chatMessagesToItems(messages)).toEqual([
      {
        type: "confirmation",
        call_id: "approve-1",
        tool: "send_email",
        status: "approved",
        arguments: { to: "a@example.com" },
      },
    ])
  })

  it("leaves out the message that carried confirmation answers, and trims the member's text", () => {
    const messages: ThreadChatMessage[] = [
      { id: "m1", role: "user", parts: [{ type: "text", text: "  Hello\n" }] },
      {
        id: "m2",
        role: "user",
        metadata: { oryxaConfirmation: true },
        parts: [{ type: "text", text: " " }],
      },
    ]
    expect(chatMessagesToItems(messages)).toEqual([
      { type: "message", id: "m1", author: "member", text: "Hello" },
    ])
  })

  it("imports later Go events once without duplicating the member's text", () => {
    const stored = itemsToChatMessages([
      { type: "message", id: "old-user", author: "member", text: "Hello" },
      { type: "message", id: "old-answer", author: "Krude", text: "Hi" },
    ])
    const backend: Item[] = [
      { type: "message", id: "go-user", author: "member", text: "Hello" },
      { type: "message", id: "old-answer", author: "Krude", text: "Hi" },
      {
        type: "message",
        id: "app-update",
        author: "apps",
        text: "New calendar event",
      },
    ]
    const merged = reconcileChatMessages(stored, backend)
    expect(chatMessagesToItems(merged).map((item) => item.text)).toEqual([
      "Hello",
      "Hi",
      "New calendar event",
    ])
    expect(reconcileChatMessages(merged, backend)).toBe(merged)
    expect(reconcileChatMessages([], [])).toEqual([])
  })

  it("matches the member's words the way Go stored them, trimmed", () => {
    const stored: ThreadChatMessage[] = [
      { id: "m1", role: "user", parts: [{ type: "text", text: "Hello \n" }] },
    ]
    expect(
      reconcileChatMessages(stored, [
        { type: "message", id: "e1", author: "member", text: "Hello" },
      ])
    ).toBe(stored)
  })

  it("brings in the answer to a confirmation given outside the socket", () => {
    const asked: Item = {
      type: "confirmation",
      id: "e3",
      call_id: "confirm-1",
      tool: "send_email",
      status: "pending",
    }
    const stored = itemsToChatMessages([asked])
    const merged = reconcileChatMessages(stored, [
      { ...asked, status: "declined" },
    ])
    expect(chatMessagesToItems(merged)).toEqual([
      { ...asked, status: "declined" },
    ])
    expect(
      reconcileChatMessages(merged, [{ ...asked, status: "declined" }])
    ).toBe(merged)
  })

  it("renders a retried run once, without the session_expired signal before it", () => {
    const messages: ThreadChatMessage[] = [
      { id: "m1", role: "user", parts: [{ type: "text", text: "hi" }] },
      {
        id: "a1",
        role: "assistant",
        parts: [
          itemPart({
            type: "run_failed",
            error: "Your session has ended. Sign in again to continue.",
            code: sessionExpiredCode,
          }),
        ],
      },
      {
        id: "a2",
        role: "assistant",
        parts: [
          itemPart({ type: "run_started", run_id: "run_2" }),
          itemPart({ type: "delta", author: "Krude", text: "Hel" }),
          itemPart({
            type: "message",
            id: "e2",
            author: "Krude",
            text: "Hello",
          }),
          itemPart({ type: "run_completed", run_id: "run_2" }),
        ],
      },
    ]
    expect(
      buildTimeline(chatMessagesToItems(messages)).map((entry) =>
        entry.kind === "message" ? [entry.role, entry.text] : entry.kind
      )
    ).toEqual([
      ["member", "hi"],
      ["agent", "Hello"],
    ])
  })
})

describe("withoutSupersededDeltas", () => {
  function answer(items: Item[]): ThreadChatMessage {
    return { id: "answer", role: "assistant", parts: items.map(itemPart) }
  }

  it("stores a finished reply without the deltas that drafted it", () => {
    const streamed = answer([
      { type: "run_started", run_id: "run-1" },
      { type: "delta", author: "Krude", text: "Rotation " },
      { type: "delta", author: "Krude", text: "is every " },
      { type: "delta", author: "Krude", text: "ninety days." },
      {
        type: "message",
        id: "reply-1",
        author: "Krude",
        text: "Rotation is every ninety days.",
      },
      { type: "run_completed", run_id: "run-1" },
    ])
    const stored = withoutSupersededDeltas(streamed)
    expect(chatMessagesToItems([stored]).map((item) => item.type)).toEqual([
      "run_started",
      "message",
      "run_completed",
    ])
    // What the conversation shows is unchanged by leaving them out.
    expect(buildTimeline(chatMessagesToItems([stored]))).toEqual(
      buildTimeline(chatMessagesToItems([streamed]))
    )
  })

  it("keeps the deltas of a reply still being written", () => {
    const streamed = answer([
      { type: "delta", author: "Krude", text: "Looking" },
      { type: "message", id: "reply-1", author: "Krude", text: "Looking." },
      { type: "tool_call", id: "call-1", call_id: "call-1", tool: "search" },
      { type: "delta", author: "Krude", text: "Found " },
      { type: "delta", author: "Krude", text: "it" },
    ])
    expect(
      chatMessagesToItems([withoutSupersededDeltas(streamed)]).map(
        (item) => item.text ?? item.type
      )
    ).toEqual(["Looking.", "tool_call", "Found ", "it"])
  })

  it("leaves a message with nothing to drop as it was", () => {
    const member: ThreadChatMessage = {
      id: "member",
      role: "user",
      parts: [{ type: "text", text: "Hello" }],
    }
    expect(withoutSupersededDeltas(member)).toBe(member)
  })
})

describe("threadAgentName", () => {
  it("names one Durable Object per thread and reads the name back", () => {
    const name = threadAgentName(
      "org_1",
      "8f14e45f-ceea-4e1f-a3a0-7f2a1c6a0b11"
    )
    expect(name).toBe("org_1~8f14e45f-ceea-4e1f-a3a0-7f2a1c6a0b11")
    expect(parseThreadAgentName(name)).toEqual({
      organizationId: "org_1",
      threadId: "8f14e45f-ceea-4e1f-a3a0-7f2a1c6a0b11",
    })
  })

  it("refuses a name the Worker would not route", () => {
    for (const name of [
      "org_1",
      "org_1~",
      "~t1",
      "a~b~c",
      "org 1~t1",
      "o/1~t1",
    ]) {
      expect(parseThreadAgentName(name)).toBeUndefined()
    }
  })
})
