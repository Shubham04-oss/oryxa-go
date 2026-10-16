import type { UIMessage } from "ai"
import type { Item } from "./client"

/**
 * The `code` of a `run_failed` the thread's Durable Object writes itself when
 * Go answered 401 (or its socket holds no cookie at all): the cookie its
 * socket was opened with expired before the run started, so nothing ran. The
 * page refreshes the session, reopens the socket (`reopenSocket`) and sends
 * again, once. Go never sets it.
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
 * become one assistant message each, runs' starts and ends among them: since
 * #390 Go stores them, with the ids the streamed copies carry, so the run
 * cards can read a past run's duration and usage.
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
    if (!assistant) {
      assistant = { id: randomId(), role: "assistant", parts: [] }
      messages.push(assistant)
    }
    assistant.parts.push(itemPart(item))
  }
  return messages
}

/**
 * What the member said in a stored message, trimmed as Go stores it: nothing
 * for the placeholder that carried confirmation answers, or for an empty one.
 */
function memberText(message: ThreadChatMessage): string | undefined {
  if (message.role !== "user" || message.metadata?.oryxaConfirmation) {
    return undefined
  }
  const text = message.parts
    .flatMap((part) => (part.type === "text" ? [part.text] : []))
    .join("")
    .trim()
  return text || undefined
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
      const text = memberText(message)
      if (text) {
        items.push({ type: "message", id: message.id, author: "member", text })
      }
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
    item.request_id ?? "",
    item.artifact ?? "",
    item.version ?? "",
  ].join("\u0000")
}

/** When an item happened, in ms, or nothing when it says nothing readable. */
function itemTime(item: Item): number | undefined {
  if (!item.at) return undefined
  const time = Date.parse(item.at)
  return Number.isFinite(time) ? time : undefined
}

/**
 * When each stored message happened, for placing what is missing among them.
 * An answer's time is its first item's. The member's messages carry none in
 * the transcript, so they take the time Go stored the same words at (matched
 * by their trimmed text, in order, as the missing ones are found); a message
 * still without a time takes the one before it, and none goes back in time.
 */
function storedTimes(
  stored: readonly ThreadChatMessage[],
  backendItems: readonly Item[]
): number[] {
  const memberTimes = new Map<string, Array<number | undefined>>()
  for (const item of backendItems) {
    if (item.type !== "message" || item.author !== "member") continue
    const text = (item.text ?? "").trim()
    const times = memberTimes.get(text) ?? []
    times.push(itemTime(item))
    memberTimes.set(text, times)
  }
  let latest = Number.NEGATIVE_INFINITY
  return stored.map((message) => {
    let time: number | undefined
    if (message.role === "user") {
      const text = memberText(message)
      if (text !== undefined) time = memberTimes.get(text)?.shift()
    } else {
      for (const part of message.parts) {
        const item = partItem(part)
        const at = item && itemTime(item)
        if (at !== undefined && (time === undefined || at < time)) time = at
      }
    }
    latest = Math.max(latest, time ?? latest)
    return latest
  })
}

/**
 * The stored transcript with whatever Go's history has that it lacks: runs
 * started outside the socket (updates from the member's apps, a team or a
 * workflow), and confirmations answered elsewhere. The member's own messages
 * are matched by their trimmed text, since the transcript's copies carry chat
 * message ids rather than Go's; everything else by its stored event. What is
 * missing goes in by the time it happened (`at`), after what happened at the
 * same moment, so an update that landed overnight sits between the day's
 * conversations rather than below the latest message. What says nothing
 * readable of its time goes last, and what Go lists in an order keeps it.
 * Returns `stored` itself when nothing is missing, so a caller can compare by
 * reference.
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
      // Its answer, for the card that asked. It sorts by the card's time,
      // after the message that holds the card.
      missing.push({
        type: "confirmation",
        call_id: item.call_id,
        status: item.status,
        ...(item.at ? { at: item.at } : {}),
      })
    }
  }
  if (missing.length === 0) return stored

  // Each missing item goes before the first stored message that happened
  // after it, and never before the missing item ahead of it.
  const times = storedTimes(stored, backendItems)
  const slots: Item[][] = stored.map(() => [])
  slots.push([])
  let floor = 0
  for (const item of missing) {
    const time = itemTime(item)
    let slot = stored.length
    if (time !== undefined) {
      const after = times.findIndex((happened) => happened > time)
      slot = Math.max(after === -1 ? stored.length : after, floor)
      floor = slot
    }
    slots[slot].push(item)
  }
  const merged: ThreadChatMessage[] = []
  slots.forEach((items, index) => {
    if (items.length > 0) merged.push(...itemsToChatMessages(items))
    if (index < stored.length) merged.push(stored[index])
  })
  return merged
}
