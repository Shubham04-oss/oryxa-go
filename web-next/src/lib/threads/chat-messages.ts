import type { UIMessage } from "ai"
import type { Item } from "./client"

/**
 * The `code` of a `run_failed` the thread's Durable Object writes itself when
 * Go answered 401: the cookie its socket was opened with expired before the
 * run started, so nothing ran. The page refreshes the session, reopens the
 * socket (`reopenSocket`) and sends again, once. Go never sets it.
 */
export const sessionExpiredCode = "session_expired"

/**
 * What the thread's Durable Object stores and streams, in the AI SDK's shape:
 * the member's messages are `user` messages with a text part, and every item
 * Go streams travels as a `data-oryxa` part of an `assistant` message.
 * `metadata.oryxaConfirmation` marks the placeholder user message that
 * carries confirmation answers, which is not the member's own words.
 */
export type ThreadChatMessage = UIMessage<
  { oryxaConfirmation?: boolean },
  { oryxa: Item }
>

// The Worker routes `/agents/thread-chat-agent/{name}` only for ids made of
// these characters, so a name always splits back into the same two ids.
const agentNameId = /^[\w-]+$/

/** The thread's Durable Object name: one object per thread, `{organization}~{thread}`. */
export function threadAgentName(
  organizationId: string,
  threadId: string
): string {
  return `${organizationId}~${threadId}`
}

/**
 * The ids a Durable Object name was made from, or nothing for a name
 * `threadAgentName` could not have made, as the Worker checks it before
 * routing (web/src/server.ts).
 */
export function parseThreadAgentName(
  name: string
): { organizationId: string; threadId: string } | undefined {
  const parts = name.split("~")
  if (parts.length !== 2) return undefined
  const [organizationId, threadId] = parts
  if (!agentNameId.test(organizationId) || !agentNameId.test(threadId)) {
    return undefined
  }
  return { organizationId, threadId }
}

/** An item as the part the Durable Object streams and stores. */
export function itemPart(item: Item): { type: "data-oryxa"; data: Item } {
  return { type: "data-oryxa", data: item }
}

function partItem(part: UIMessage["parts"][number]): Item | undefined {
  return part.type === "data-oryxa" ? (part.data as Item) : undefined
}

/**
 * Leaves out the `delta` parts a finished `message` part has made redundant,
 * for what the Durable Object stores. Go streams a reply as deltas and then
 * sends it whole, so once it lands the deltas are only weight: a long answer
 * is hundreds of them. The deltas before a finished message go; those after
 * the last one are a reply still being written (or one a failed run cut
 * short, which the timeline shows as interrupted), and stay.
 */
export function withoutSupersededDeltas<TMessage extends UIMessage>(
  message: TMessage
): TMessage {
  const superseded = new Set<number>()
  let drafting: number[] = []
  message.parts.forEach((part, index) => {
    const item = partItem(part)
    if (item?.type === "delta") {
      drafting.push(index)
    } else if (item?.type === "message") {
      for (const draftIndex of drafting) superseded.add(draftIndex)
      drafting = []
    }
  })
  if (superseded.size === 0) return message
  return {
    ...message,
    parts: message.parts.filter((_, index) => !superseded.has(index)),
  }
}

// crypto.randomUUID needs a secure context, which a page served over plain
// http on a LAN address does not have; getRandomValues works everywhere.
function randomId(): string {
  const bytes = new Uint8Array(16)
  crypto.getRandomValues(bytes)
  return Array.from(bytes, (byte) => byte.toString(16).padStart(2, "0")).join(
    ""
  )
}

/**
 * Go's items as chat messages: for a conversation from before the Durable
 * Object held it, and for what `reconcileChatMessages` finds missing. A
 * member's message keeps its stored event's id; the agent's items in between
 * become one assistant message each.
 */
export function itemsToChatMessages(
  items: readonly Item[]
): ThreadChatMessage[] {
  const messages: ThreadChatMessage[] = []
  let assistant: ThreadChatMessage | undefined
  for (const item of items) {
    if (item.type === "message" && item.author === "member") {
      messages.push({
        id: item.id ?? randomId(),
        role: "user",
        parts: [{ type: "text", text: item.text ?? "" }],
      })
      assistant = undefined
      continue
    }
    if (item.type === "run_started" || item.type === "run_completed") continue
    if (!assistant) {
      assistant = { id: randomId(), role: "assistant", parts: [] }
      messages.push(assistant)
    }
    assistant.parts.push(itemPart(item))
  }
  return messages
}

/**
 * A stored transcript back as items, for `buildTimeline`. The member's
 * messages become `message` items with the chat message's id (not a Go event
 * id) and trimmed text, as Go stores it; the placeholder that carried
 * confirmation answers is left out. A later confirmation item with the same
 * `call_id`, such as the status the Durable Object writes when the member
 * answers, updates the first one in place.
 */
export function chatMessagesToItems(
  messages: readonly ThreadChatMessage[]
): Item[] {
  const items: Item[] = []
  const confirmationIndex = new Map<string, number>()
  for (const message of messages) {
    if (message.role === "user") {
      if (message.metadata?.oryxaConfirmation) continue
      const text = message.parts
        .flatMap((part) => (part.type === "text" ? [part.text] : []))
        .join("")
        .trim()
      if (!text) continue
      items.push({ type: "message", id: message.id, author: "member", text })
      continue
    }
    for (const part of message.parts) {
      const item = partItem(part)
      if (!item) continue
      if (item.type === "confirmation" && item.call_id) {
        const previous = confirmationIndex.get(item.call_id)
        if (previous !== undefined) {
          items[previous] = { ...items[previous], ...item }
          continue
        }
        confirmationIndex.set(item.call_id, items.length)
      }
      items.push(item)
    }
  }
  return items
}

function storedItemKey(item: Item): string {
  return [
    item.id ?? "",
    item.type,
    item.call_id ?? "",
    item.artifact ?? "",
    item.version ?? "",
  ].join("\u0000")
}

/**
 * The stored transcript with whatever Go's history has that it lacks: runs
 * started outside the socket (updates from the member's apps, a team or a
 * workflow), and confirmations answered elsewhere. The member's own messages
 * are matched by their trimmed text, since the transcript's copies carry chat
 * message ids rather than Go's; everything else by its stored event. What is
 * missing goes at the end, in Go's order. Returns `stored` itself when
 * nothing is missing, so a caller can compare by reference.
 */
export function reconcileChatMessages(
  stored: ThreadChatMessage[],
  backendItems: readonly Item[]
): ThreadChatMessage[] {
  if (stored.length === 0) {
    return backendItems.length > 0 ? itemsToChatMessages(backendItems) : stored
  }
  const byKey = new Map<string, Item>()
  const memberCounts = new Map<string, number>()
  for (const item of chatMessagesToItems(stored)) {
    if (item.type === "message" && item.author === "member") {
      const text = (item.text ?? "").trim()
      memberCounts.set(text, (memberCounts.get(text) ?? 0) + 1)
    } else if (item.id) {
      byKey.set(storedItemKey(item), item)
    }
  }
  const missing: Item[] = []
  for (const item of backendItems) {
    if (item.type === "message" && item.author === "member") {
      const text = (item.text ?? "").trim()
      const count = memberCounts.get(text) ?? 0
      if (count > 0) memberCounts.set(text, count - 1)
      else missing.push(item)
      continue
    }
    const existing = item.id ? byKey.get(storedItemKey(item)) : undefined
    if (!existing) {
      missing.push(item)
    } else if (
      item.type === "confirmation" &&
      existing.status !== item.status
    ) {
      missing.push({
        type: "confirmation",
        call_id: item.call_id,
        status: item.status,
      })
    }
  }
  return missing.length > 0
    ? [...stored, ...itemsToChatMessages(missing)]
    : stored
}
