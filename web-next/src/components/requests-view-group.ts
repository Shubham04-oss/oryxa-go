// Requests as conversations (F18, N12): a teammate who asks several things in
// a row, in one place, made one request for each ask, and the inbox showed
// each as a row of its own. Here asks from the same person (to the same
// person, in Sent), made in the same channel and teamspace, with no more
// than a short gap between one and the next, are one conversation: one row,
// with how many messages it holds, and a thread that reads them in order.
import { originOf } from "@/lib/agents/agent-requests"
import type { AgentRequest, AgentRequestBox } from "@/lib/agents/agent-requests"

/** The longest quiet between two asks that are still one conversation. */
export const CONVERSATION_GAP_MS = 60 * 60_000

export type RequestConversation = {
  /** Its first ask's id: it stays while later asks join it. */
  id: string
  /** Its asks, the newest first. */
  requests: AgentRequest[]
  /** The newest ask, which the row reads. */
  latest: AgentRequest
}

/** Who the conversation is with, from the box's side: the asker in the inbox, the asked in Sent. */
export function counterpartOf(
  request: AgentRequest,
  box: AgentRequestBox
): string {
  return box === "inbox" ? request.requester.user_id : request.target.user_id
}

function threadOf(request: AgentRequest, box: AgentRequestBox): string {
  return [
    counterpartOf(request, box),
    originOf(request),
    request.teamspace_id ?? "",
  ].join("\u0000")
}

/**
 * The requests as conversations, in the list's own order. An ask joins the conversation
 * the same person last had in the same place when it came at most `gapMs`
 * after that conversation's last ask; otherwise it starts one.
 */
export function conversationsOf(
  requests: readonly AgentRequest[],
  box: AgentRequestBox,
  gapMs = CONVERSATION_GAP_MS
): RequestConversation[] {
  const oldestFirst = [...requests].sort(
    (a, b) =>
      Date.parse(a.created_at) - Date.parse(b.created_at) ||
      a.id.localeCompare(b.id)
  )
  const open = new Map<string, AgentRequest[]>()
  const conversations: AgentRequest[][] = []
  for (const request of oldestFirst) {
    const thread = threadOf(request, box)
    const current = open.get(thread)
    const last = current?.at(-1)
    if (
      current &&
      last &&
      Date.parse(request.created_at) - Date.parse(last.created_at) <= gapMs
    ) {
      current.push(request)
      continue
    }
    const started = [request]
    open.set(thread, started)
    conversations.push(started)
  }
  // In the order the list came in (Go's: latest activity first), each
  // conversation where its first-listed ask was.
  const place = new Map(requests.map((request, index) => [request.id, index]))
  const firstListed = (asks: AgentRequest[]) =>
    Math.min(...asks.map((request) => place.get(request.id) ?? 0))
  return conversations
    .sort((a, b) => firstListed(a) - firstListed(b))
    .map((asks) => {
      const newestFirst = [...asks].reverse()
      return { id: asks[0].id, requests: newestFirst, latest: newestFirst[0] }
    })
}

/** The conversation one request belongs to. */
export function conversationOf(
  conversations: readonly RequestConversation[],
  requestId: string
): RequestConversation | undefined {
  return conversations.find((conversation) =>
    conversation.requests.some((request) => request.id === requestId)
  )
}
