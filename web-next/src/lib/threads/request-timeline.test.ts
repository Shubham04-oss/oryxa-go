import { describe, expect, it } from "vitest"
import {
  chatMessagesToItems,
  itemsToChatMessages,
  reconcileChatMessages,
} from "./chat-messages"
import type { Item } from "./client"
import { buildTimeline } from "./timeline"

describe("request timeline pointers", () => {
  it("updates duplicate request pointers in place between the surrounding messages", () => {
    const entries = buildTimeline([
      { type: "message", id: "ask", author: "member", text: "Ask Nia" },
      { type: "request", id: "create", request_id: "irq_1", status: "pending" },
      { type: "message", id: "reply", author: "Leo", text: "I asked." },
      { type: "request", id: "note", request_id: "irq_1", status: "answered" },
      { type: "request", id: "invalid", status: "asked" },
    ])
    expect(entries).toHaveLength(3)
    expect(entries[1]).toMatchObject({
      kind: "request",
      key: "request:irq_1",
      id: "create",
      status: "answered",
    })
  })

  it("keeps separate pointers from one event through DO storage and reconciliation", () => {
    const items: Item[] = [
      { type: "request", id: "create", request_id: "irq_1", status: "pending" },
      { type: "request", id: "create", request_id: "irq_2", status: "pending" },
    ]
    const messages = itemsToChatMessages(items)
    expect(chatMessagesToItems(messages)).toEqual(items)
    const reconciled = reconcileChatMessages(
      itemsToChatMessages(items.slice(0, 1)),
      items
    )
    expect(chatMessagesToItems(reconciled)).toEqual(items)
    expect(
      buildTimeline(
        chatMessagesToItems(reconcileChatMessages(reconciled, items))
      )
    ).toHaveLength(2)
  })
})
