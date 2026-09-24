import type { UIMessage } from "ai"
import type { Item } from "@/lib/threads/client"
import { parseSpecialistResult } from "./result"
import type { SpecialistResult } from "./result"

// The specialist chat's Durable Object (src/specialist-chat-agent.ts): its
// name, the state it syncs to the page, and what the page is shown of the
// items a specialist's run streams. Its transcript has the same shape as the
// thread chat's (`ThreadChatMessage`, lib/threads/chat-messages.ts).

/**
 * What the specialist chat's Durable Object syncs to every socket it holds
 * (`useAgent`'s `onStateUpdate`): the Go conversation it continues. Only the
 * object changes it; a page's `setState` is refused.
 */
export type SpecialistChatState = {
  /**
   * The Go thread the next message continues, for `getSpecialistThread`.
   * Null until the first run names one (`X-Oryxa-Thread`), and again once
   * the chat starts over.
   */
  threadId: string | null
}

// The Worker routes `/agents/specialist-chat-agent/{name}` only for ids made
// of these characters, so a name always splits back into the same two ids.
const agentNameId = /^[\w-]+$/

/**
 * The specialist chat's Durable Object name: one object per specialist,
 * `{organization}~{agent}`. Go runs a specialist only for the member who
 * owns it, so this is one member's chat with it.
 */
export function specialistAgentName(
  organizationId: string,
  agentId: string
): string {
  return `${organizationId}~${agentId}`
}

/**
 * The ids a Durable Object name was made from, or nothing for a name
 * `specialistAgentName` could not have made, as the Worker checks it before
 * routing (src/server.ts).
 */
export function parseSpecialistAgentName(
  name: string
): { organizationId: string; agentId: string } | undefined {
  const parts = name.split("~")
  if (parts.length !== 2) return undefined
  const [organizationId, agentId] = parts
  if (!agentNameId.test(organizationId) || !agentNameId.test(agentId)) {
    return undefined
  }
  return { organizationId, agentId }
}

/** Words for a work result that left its summary empty, by its status. */
const emptySummary: Record<SpecialistResult["status"], string> = {
  completed: "Done.",
  needs_input: "I need more from you to continue.",
  blocked: "I'm blocked and can't finish this.",
}

/**
 * A specialist's message as the timeline shows it. A specialist answers with
 * a JSON work result (`parseSpecialistResult`); its message becomes readable:
 * `text` is the result's summary, the deliverable itself, and `result` keeps
 * the status, tools, files and suggested hand-offs for the page to show. A
 * message that is not a work result, and every other item, is left as it is.
 * Apply it to Go's history too (`readableSpecialistItems`) before merging it
 * with a stored transcript, which holds messages already made readable.
 */
export function readableSpecialistItem(item: Item): Item {
  if (
    item.type !== "message" ||
    !item.text ||
    item.result ||
    item.author === "member" ||
    item.author === "apps"
  ) {
    return item
  }
  const result = parseSpecialistResult(item.text)
  if (!result) return item
  return {
    ...item,
    text: result.summary.trim() || emptySummary[result.status],
    result,
  }
}

/** `readableSpecialistItem` for a list, such as `getSpecialistThread`'s history. */
export function readableSpecialistItems(items: readonly Item[]): Item[] {
  return items.map(readableSpecialistItem)
}

/**
 * Whether an item is the specialist chat's progress marker: a `delta` with
 * no text, which Go never streams (it sends a delta only for text).
 */
export function isProgressMarker(item: Item): boolean {
  return item.type === "delta" && !item.text
}

/**
 * What the page is shown of each item a specialist's run streams, in order,
 * for one run: the function it returns keeps track of the answer being
 * drafted.
 *
 * A specialist writes its answer as a JSON work result, so its deltas are
 * raw JSON growing a piece at a time, which is no use to read. The first
 * delta of each draft becomes a progress marker instead (a delta with no
 * text: `isProgressMarker`) and the rest of that draft's deltas are left
 * out. `buildTimeline` shows the marker as the specialist's draft, still
 * `streaming` and empty, until the finished message replaces it in place;
 * the object stores neither (`withoutProgressMarkers`). A finished message
 * is made readable (`readableSpecialistItem`); everything else passes as it
 * is. A draft ends where the timeline ends one: at a message, at the run's
 * start or end, or at a delta from another author.
 */
export function readableSpecialistStream(): (item: Item) => Item | undefined {
  let drafting: string | undefined
  return (item) => {
    switch (item.type) {
      case "delta": {
        const author = item.author ?? ""
        if (drafting === author) return undefined
        drafting = author
        return { type: "delta", at: item.at, author: item.author, text: "" }
      }
      case "message":
      case "run_started":
      case "run_completed":
      case "run_failed":
        drafting = undefined
        return readableSpecialistItem(item)
      default:
        return item
    }
  }
}

/**
 * Leaves out the progress markers of a message, for what the specialist
 * chat stores: a finished answer replaces its marker, and one a failed run
 * cut short leaves only the failure worth keeping.
 */
export function withoutProgressMarkers<TMessage extends UIMessage>(
  message: TMessage
): TMessage {
  const parts = message.parts.filter(
    (part) => part.type !== "data-oryxa" || !isProgressMarker(part.data as Item)
  )
  return parts.length === message.parts.length ? message : { ...message, parts }
}
