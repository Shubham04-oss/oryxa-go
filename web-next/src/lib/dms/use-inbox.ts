// The seam between a member's inbox (src/inbox-agent.ts) and what reads it:
// the right panel's DMs tab, Team's recent conversations and their badges.
// The list arrives over the inbox object's socket and stays current as the
// member's conversations change; a page only listens.
import { useEffect, useReducer, useRef } from "react"
import { useAgent } from "agents/react"
import { ApiError } from "@/lib/api/client"
import { checkConversationSocket, inboxPath } from "./client"
import { INBOX_AGENT, inboxName, isNameId } from "./names"
import { conversationGoneCloseCode, parseInboxServerMessage } from "./protocol"
import type { InboxServerMessage } from "./protocol"
import type { Conversation } from "./types"

/** Where the member stands with their inbox. */
export type InboxStatus =
  /** Nothing to open: no organization or member yet. */
  | "closed"
  /** The socket is opening, or has not been sent the list yet. */
  | "connecting"
  /** The list is here and stays current. */
  | "live"
  /** The socket dropped and is trying again; or, for good, the organization is not the member's any more. */
  | "error"
  /** The session ended and could not be refreshed. Sign in again. */
  | "session_expired"

/** A member's conversations, the latest first, and how many messages in them they have not read. */
export type Inbox = {
  conversations: Conversation[]
  unreadTotal: number
  status: InboxStatus
}

type Live = {
  name: string
  conversations: Conversation[]
  unreadTotal: number
  open: boolean
  dropped: boolean
  /** The socket will not open again for this inbox. */
  settled?: "error" | "session_expired"
}

type Socket = { readyState: number; OPEN: number }

function initialLive(name: string): Live {
  return {
    name,
    conversations: [],
    unreadTotal: 0,
    open: false,
    dropped: false,
  }
}

/** The latest first, as the inbox lists them. */
function latestFirst(left: Conversation, right: Conversation): number {
  if (left.updatedAt !== right.updatedAt) {
    return left.updatedAt < right.updatedAt ? 1 : -1
  }
  return left.name < right.name ? -1 : left.name > right.name ? 1 : 0
}

/**
 * Whether an event came from the socket of the conversation the view shows
 * now. When a view moves to another conversation, the SDK hands it the new
 * socket only in the render after (it swaps sockets in an effect), so for a
 * moment the old socket's events reach the new conversation's handlers.
 * Each PartySocket carries the object's name as its `room`; a stand-in with
 * none is judged by identity.
 */
function fromSocketOf(name: string, socket: unknown, target: unknown): boolean {
  if (target === undefined || target === null) return true
  const room = (target as { room?: unknown }).room
  if (typeof room === "string") return room === name
  return target === socket
}

/** One view's inbox: what its socket said. */
class InboxView {
  live: Live = initialLive("")
  socket?: Socket
  mounted = false
  private organizationId = ""
  private userId = ""
  private checking = false

  constructor(private readonly render: () => void) {}

  use(name: string, organizationId: string, userId: string): void {
    if (this.live.name === name) return
    this.live = initialLive(name)
    this.organizationId = organizationId
    this.userId = userId
  }

  status(): InboxStatus {
    const { live } = this
    if (!live.name) return "closed"
    if (live.settled) return live.settled
    if (live.open) return "live"
    return live.dropped ? "error" : "connecting"
  }

  /** A message on the socket; one from a socket the view has since replaced is not this view's. */
  heard(data: unknown, target?: unknown): void {
    if (!fromSocketOf(this.live.name, this.socket, target)) return
    const message = parseInboxServerMessage(data)
    if (!message || !this.live.name || this.live.settled) return
    this.apply(message)
    this.render()
  }

  closed(event: { code: number; target?: unknown }): void {
    if (
      !this.mounted ||
      !this.live.name ||
      this.live.settled ||
      !fromSocketOf(this.live.name, this.socket, event.target)
    ) {
      return
    }
    this.live = { ...this.live, open: false, dropped: true }
    if (event.code === conversationGoneCloseCode) {
      this.live = { ...this.live, settled: "error" }
      this.render()
      return
    }
    this.render()
    this.check()
  }

  private apply(message: InboxServerMessage): void {
    switch (message.type) {
      case "inbox-snapshot":
        this.live = {
          ...this.live,
          conversations: [...message.conversations].sort(latestFirst),
          unreadTotal: message.unreadTotal,
          open: true,
          dropped: false,
        }
        return
      case "inbox-conversation":
        this.live = {
          ...this.live,
          conversations: [
            ...this.live.conversations.filter(
              (item) => item.name !== message.conversation.name
            ),
            message.conversation,
          ].sort(latestFirst),
          unreadTotal: message.unreadTotal,
        }
        return
      case "inbox-removed":
        this.live = {
          ...this.live,
          conversations: this.live.conversations.filter(
            (item) => item.name !== message.name
          ),
          unreadTotal: message.unreadTotal,
        }
        return
      case "inbox-gone":
        this.live = { ...this.live, open: false, settled: "error" }
        return
    }
  }

  /** Why the socket closed: the Worker makes the socket's own check with Go. */
  private check(): void {
    if (this.checking) return
    this.checking = true
    const name = this.live.name
    checkConversationSocket(inboxPath(this.organizationId, this.userId))
      .catch((error: unknown) => {
        if (this.live.name !== name || !(error instanceof ApiError)) return
        if (error.status === 401) {
          this.live = { ...this.live, settled: "session_expired" }
        } else if (error.status === 403 || error.status === 404) {
          this.live = { ...this.live, settled: "error" }
        } else {
          // Anything else may pass: the socket keeps trying.
          return
        }
        this.render()
      })
      .finally(() => {
        this.checking = false
      })
  }
}

/**
 * The member's inbox in `organizationId`, over its object's socket, while
 * the calling component is mounted. Several on one page each open their
 * own socket.
 */
export function useInbox(organizationId?: string, viewerId?: string): Inbox {
  const name =
    isNameId(organizationId) && isNameId(viewerId)
      ? inboxName(organizationId, viewerId)
      : ""
  const [, render] = useReducer((count: number) => count + 1, 0)
  const held = useRef<InboxView>(undefined)
  held.current ??= new InboxView(render)
  const view = held.current
  view.use(name, organizationId ?? "", viewerId ?? "")

  const socket = useAgent({
    agent: INBOX_AGENT,
    name: name || "closed",
    enabled: Boolean(name) && view.live.settled === undefined,
    onMessage: (event: MessageEvent) => view.heard(event.data, event.target),
    onClose: (event: CloseEvent) => view.closed(event),
  })
  view.socket = socket

  useEffect(() => {
    view.mounted = true
    return () => {
      view.mounted = false
    }
  }, [view])

  return {
    conversations: view.live.conversations,
    unreadTotal: view.live.unreadTotal,
    status: view.status(),
  }
}
