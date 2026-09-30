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

  describe("placing what Go added by the time it happened", () => {
    const at = (day: string, time: string) => `2026-09-${day}T${time}Z`
    const evening = (time: string) => at("29", time)
    const morning = (time: string) => at("30", time)
    const texts = (messages: ThreadChatMessage[]) =>
      chatMessagesToItems(messages).map((item) => item.text)
    const update = (id: string, when?: string): Item => ({
      type: "message",
      id,
      author: "apps",
      text: id,
      at: when,
    })

    /** Two exchanges, the evening's and the next morning's, as Go lists them. */
    const history: Item[] = [
      {
        type: "message",
        id: "go-u1",
        author: "member",
        text: "Hello",
        at: evening("17:00:00"),
      },
      {
        type: "message",
        id: "a1",
        author: "Krude",
        text: "Hi",
        at: evening("17:00:05"),
      },
      {
        type: "message",
        id: "go-u2",
        author: "member",
        text: "Morning",
        at: morning("09:00:00"),
      },
      {
        type: "message",
        id: "a2",
        author: "Krude",
        text: "Good morning",
        at: morning("09:00:05"),
      },
    ]
    // The transcript holds the member's words under chat message ids, with
    // no time for them.
    const stored = itemsToChatMessages(
      history.map((item) =>
        item.author === "member" ? { ...item, at: undefined } : item
      )
    )

    it("puts an update that landed overnight between the two exchanges, not below the latest message", () => {
      const overnight = update("overnight", morning("03:00:00"))
      const backend = [
        history[0],
        history[1],
        overnight,
        history[2],
        history[3],
      ]
      const merged = reconcileChatMessages(stored, backend)
      expect(texts(merged)).toEqual([
        "Hello",
        "Hi",
        "overnight",
        "Morning",
        "Good morning",
      ])
      // Merged once: a second look finds nothing missing.
      expect(reconcileChatMessages(merged, backend)).toBe(merged)
    })

    it("puts one that landed between a question and its answer before the answer", () => {
      const between = update("between", evening("17:00:03"))
      expect(
        texts(reconcileChatMessages(stored, [...history, between]))
      ).toEqual(["Hello", "between", "Hi", "Morning", "Good morning"])
    })

    it("puts what happened after everything at the end, and before everything at the start", () => {
      const late = update("late", morning("23:00:00"))
      const early = update("early", evening("08:00:00"))
      expect(
        texts(reconcileChatMessages(stored, [early, ...history, late]))
      ).toEqual(["early", "Hello", "Hi", "Morning", "Good morning", "late"])
    })

    it("keeps Go's order among several updates, across the messages they fall between", () => {
      const updates = [
        update("a", evening("18:00:00")),
        update("b", evening("19:00:00")),
        update("c", morning("10:00:00")),
      ]
      expect(
        texts(reconcileChatMessages(stored, [...history, ...updates]))
      ).toEqual(["Hello", "Hi", "a", "b", "Morning", "Good morning", "c"])
    })

    it("puts what happened at the same moment after what is already there", () => {
      const same = update("same", evening("17:00:05"))
      expect(texts(reconcileChatMessages(stored, [...history, same]))).toEqual([
        "Hello",
        "Hi",
        "same",
        "Morning",
        "Good morning",
      ])
    })

    it("never reorders what Go lists, even when its times disagree", () => {
      const first = update("first", evening("19:00:00"))
      const second = update("second", evening("18:00:00"))
      expect(
        texts(reconcileChatMessages(stored, [...history, first, second]))
      ).toEqual(["Hello", "Hi", "first", "second", "Morning", "Good morning"])
    })

    it("puts what says nothing of its time last, in Go's order", () => {
      const untimed = [update("no time one"), update("no time two")]
      const timed = update("timed", evening("19:00:00"))
      expect(
        texts(reconcileChatMessages(stored, [...history, ...untimed, timed]))
      ).toEqual([
        "Hello",
        "Hi",
        "timed",
        "Morning",
        "Good morning",
        "no time one",
        "no time two",
      ])
      // An unreadable time says nothing either.
      expect(
        texts(
          reconcileChatMessages(stored, [
            ...history,
            { ...untimed[0], at: "not a time" },
          ])
        ).at(-1)
      ).toBe("no time one")
    })

    it("keeps what the transcript holds in its own order, times or not", () => {
      const untimed = itemsToChatMessages([
        { type: "message", id: "u1", author: "member", text: "One" },
        { type: "message", id: "a1", author: "Krude", text: "Two" },
        { type: "message", id: "u2", author: "member", text: "Three" },
      ])
      const merged = reconcileChatMessages(untimed, [
        update("new", evening("08:00:00")),
      ])
      expect(texts(merged)).toEqual(["One", "Two", "Three", "new"])
    })

    it("keeps an answered confirmation's status on its card, wherever the card sits", () => {
      const card: Item = {
        type: "confirmation",
        id: "c1",
        call_id: "confirm-1",
        tool: "send_email",
        status: "pending",
        at: evening("17:00:03"),
      }
      const withCard = [history[0], card, ...history.slice(1)]
      const storedWithCard = itemsToChatMessages(
        withCard.map((item) =>
          item.author === "member" ? { ...item, at: undefined } : item
        )
      )
      const merged = reconcileChatMessages(
        storedWithCard,
        withCard.map((item) =>
          item.id === "c1" ? { ...item, status: "approved" as const } : item
        )
      )
      const items = chatMessagesToItems(merged)
      expect(items.map((item) => item.text ?? item.type)).toEqual([
        "Hello",
        "confirmation",
        "Hi",
        "Morning",
        "Good morning",
      ])
      expect(items[1]).toMatchObject({
        call_id: "confirm-1",
        status: "approved",
      })
    })
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
