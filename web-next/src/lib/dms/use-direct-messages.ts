// The seam between a DM and a view of it: the main canvas's conversation,
// or the right panel's DMs tab. Both are the same live object (REALTIME.md,
// src/direct-message-agent.ts); each view opens its own socket on it, so
// two views on one page each keep their own state and both stay current.
import { useEffect, useReducer, useRef } from "react"
import { useAgent } from "agents/react"
import { ApiError } from "@/lib/api/client"
import { checkConversationSocket, directMessagePath } from "./client"
import { DIRECT_MESSAGE_AGENT, directMessageName, isNameId } from "./names"
import {
  DM_MAX_BODY_LENGTH,
  DM_PAGE_SIZE,
  DM_TYPING_REFRESH_MS,
  DM_TYPING_TTL_MS,
  codePoints,
  conversationGoneCloseCode,
  parseDmServerMessage,
} from "./protocol"
import type {
  DmClientMessage,
  DmServerMessage,
  DmWireMessage,
} from "./protocol"
import { socketProtocol } from "@/lib/socket-protocol"
import type { DmMessage } from "./types"

/** Where the viewer stands with the DM. */
export type DirectMessagesStatus =
  /** Nothing to open: no organization, viewer or peer yet, or the viewer is the peer. */
  | "closed"
  /** The socket is opening, or has not been sent the conversation yet. */
  | "connecting"
  /** The conversation is open and arrives as it is said. */
  | "live"
  /** The Worker refused the viewer (403, or 404: the other person, or the viewer, is not in the organization), or the conversation was purged. For good. */
  | "not-allowed"
  /** The socket dropped and is trying again, or the Worker could not check it. */
  | "error"
  /** The session ended and could not be refreshed. Sign in again. */
  | "session_expired"

/**
 * A DM as a view sees it. `messages` is the conversation oldest first: the
 * object's messages, then the viewer's own not confirmed yet (`status`).
 * `peer` is the other person: online (a visible tab on the DM), typing, and
 * how far they have read.
 */
export type DirectMessages = {
  messages: DmMessage[]
  hasOlder: boolean
  /** Loads the page before the oldest message held; resolves once it arrives, or the socket closed first. */
  loadOlder: () => Promise<void>
  /** Sends a message: it shows at once as `sending`, becomes a normal message when the object confirms it, or `failed`. */
  send: (body: string) => void
  /** Sends a `failed` message again, under the same page id, so it is never stored twice. */
  retry: (clientId: string) => void
  /** Reads the conversation through its latest message, while the tab is visible. */
  markRead: () => void
  peer: { online: boolean; typing: boolean; lastReadSeq: number }
  /** Says the viewer is typing (again every few seconds while true), or stopped. */
  setTyping: (typing: boolean) => void
  /** Opens the socket again now rather than at its next try, while it is not open (`connecting` or `error`); what is waiting to send goes once it opens. */
  reconnect: () => void
  status: DirectMessagesStatus
}

/** How long a message may wait for the object before it shows as failed. */
export const DM_SEND_TIMEOUT_MS = 15_000

// How long a history page may take before `loadOlder` gives up on it.
const historyTimeoutMs = 15_000

type Refusal = "not-allowed" | "session_expired"

/** A message the viewer sent that the object has not confirmed. */
type Outgoing = {
  clientId: string
  body: string
  createdAt: string
  status: "sending" | "failed"
}

/** What the socket has said since the view asked for this DM. */
type Live = {
  name: string
  /** The conversation's run, from its snapshot: a purge starts a new one. */
  conversation?: string
  /** The object's messages, by seq. */
  messages: DmWireMessage[]
  hasOlder: boolean
  reads: ReadonlyMap<string, number>
  online: ReadonlySet<string>
  typing: ReadonlySet<string>
  outgoing: Outgoing[]
  /** The socket has been sent the conversation since it last opened. */
  open: boolean
  /** It closed since it was last sent the conversation. */
  dropped: boolean
  refusal?: Refusal
}

/** The part of the SDK's socket the view uses. */
type Socket = {
  readyState: number
  OPEN: number
  send: (data: string) => void
  reconnect: () => void
}

function initialLive(name: string): Live {
  return {
    name,
    messages: [],
    hasOlder: false,
    reads: new Map(),
    online: new Set(),
    typing: new Set(),
    outgoing: [],
    open: false,
    dropped: false,
  }
}

function tabVisible(): boolean {
  return (
    typeof document === "undefined" || document.visibilityState === "visible"
  )
}

/** A page id: random, from `getRandomValues`, which (unlike `randomUUID`) works outside a secure context too. */
function newId(prefix: string): string {
  const bytes = new Uint8Array(12)
  crypto.getRandomValues(bytes)
  return `${prefix}-${Array.from(bytes, (byte) =>
    byte.toString(16).padStart(2, "0")
  ).join("")}`
}

/** Folds messages into held ones: one per seq, the newer copy winning, in order. */
function merged(
  held: readonly DmWireMessage[],
  arrived: readonly DmWireMessage[]
): DmWireMessage[] {
  const bySeq = new Map(held.map((message) => [message.seq, message]))
  for (const message of arrived) bySeq.set(message.seq, message)
  return [...bySeq.values()].sort((left, right) => left.seq - right.seq)
}

function statusOf(live: Live): DirectMessagesStatus {
  if (!live.name) return "closed"
  if (live.refusal) return live.refusal
  if (live.open) return "live"
  return live.dropped ? "error" : "connecting"
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

/**
 * One view's DM: what its socket said, what it sent, and its timers. Its
 * methods keep their identity for the view's life.
 */
class DirectMessagesView {
  live: Live = initialLive("")
  socket?: Socket
  mounted = false
  private organizationId = ""
  private viewerId = ""
  private peerId = ""
  private readonly sendTimers = new Map<string, ReturnType<typeof setTimeout>>()
  private typingTimer?: ReturnType<typeof setTimeout>
  private history?: {
    requestId: string
    resolve: () => void
    timer: ReturnType<typeof setTimeout>
  }
  private checking = false
  /** A check under way reopens the socket when the Worker lets the member in: the object ended its run. */
  private reopenAfterCheck = false
  private saidTyping = false
  private typedAt = 0
  private readSent = 0
  private shown?: { live: Live; messages: DmMessage[] }
  private shownPeer?: DirectMessages["peer"]

  constructor(private readonly render: () => void) {}

  /** The DM the view shows: another one starts over. */
  use(
    name: string,
    organizationId: string,
    viewerId: string,
    peerId: string
  ): void {
    if (this.live.name === name) return
    this.stopTimers()
    this.live = initialLive(name)
    this.organizationId = organizationId
    this.viewerId = viewerId
    this.peerId = peerId
    this.saidTyping = false
    this.readSent = 0
  }

  get settled(): boolean {
    return this.live.refusal !== undefined
  }

  /** The conversation as the view renders it, kept while nothing changed. */
  messages(): DmMessage[] {
    if (this.shown?.live === this.live) return this.shown.messages
    const { messages, outgoing } = this.live
    const confirmed = new Set(
      messages.flatMap((message) =>
        message.clientId === undefined ? [] : [message.clientId]
      )
    )
    const shown: DmMessage[] = [
      ...messages,
      ...outgoing
        .filter((item) => !confirmed.has(item.clientId))
        .map((item) => ({
          id: item.clientId,
          seq: 0,
          authorId: this.viewerId,
          body: item.body,
          createdAt: item.createdAt,
          clientId: item.clientId,
          status: item.status,
        })),
    ]
    this.shown = { live: this.live, messages: shown }
    return shown
  }

  /** The other person as the view renders them, kept while nothing about them changed. */
  peer(): DirectMessages["peer"] {
    const next = {
      online: this.live.online.has(this.peerId),
      typing: this.live.typing.has(this.peerId),
      lastReadSeq: this.live.reads.get(this.peerId) ?? 0,
    }
    const shown = this.shownPeer
    if (
      shown?.online === next.online &&
      shown.typing === next.typing &&
      shown.lastReadSeq === next.lastReadSeq
    ) {
      return shown
    }
    this.shownPeer = next
    return next
  }

  status(): DirectMessagesStatus {
    return statusOf(this.live)
  }

  // The view's actions.

  readonly send = (body: string): void => {
    if (!this.live.name || this.settled) return
    const text = body.trim()
    if (!text) return
    const item: Outgoing = {
      clientId: newId("dmc"),
      body: text,
      createdAt: new Date().toISOString(),
      // Too long for the object: shown as failed at once, never sent.
      status: codePoints(text) > DM_MAX_BODY_LENGTH ? "failed" : "sending",
    }
    this.change({ outgoing: [...this.live.outgoing, item] })
    // The object stops the sender's typing as the message arrives.
    this.saidTyping = false
    if (item.status === "sending") this.transmit(item)
    this.render()
  }

  readonly retry = (clientId: string): void => {
    const item = this.live.outgoing.find(
      (candidate) => candidate.clientId === clientId
    )
    if (
      !item ||
      item.status !== "failed" ||
      this.settled ||
      codePoints(item.body) > DM_MAX_BODY_LENGTH
    ) {
      return
    }
    const again: Outgoing = { ...item, status: "sending" }
    this.change({
      outgoing: this.live.outgoing.map((candidate) =>
        candidate.clientId === clientId ? again : candidate
      ),
    })
    this.transmit(again)
    this.render()
  }

  readonly markRead = (): void => {
    if (!this.live.open || !tabVisible()) return
    const latest = this.live.messages.at(-1)?.seq ?? 0
    const read = this.live.reads.get(this.viewerId) ?? 0
    if (latest <= read || latest <= this.readSent) return
    this.readSent = latest
    this.say({ type: "dm-read", seq: latest })
  }

  readonly setTyping = (typing: boolean): void => {
    if (!this.live.open) {
      this.saidTyping = false
      return
    }
    if (!typing) {
      if (this.saidTyping) this.say({ type: "dm-typing", typing: false })
      this.saidTyping = false
      return
    }
    const now = Date.now()
    if (this.saidTyping && now - this.typedAt < DM_TYPING_REFRESH_MS) return
    this.saidTyping = true
    this.typedAt = now
    this.say({ type: "dm-typing", typing: true })
  }

  readonly reconnect = (): void => {
    if (!this.live.name || this.settled || this.live.open) return
    this.socket?.reconnect()
  }

  readonly loadOlder = (): Promise<void> => {
    if (!this.live.open || !this.live.hasOlder) return Promise.resolve()
    if (this.history) return this.historyDone()
    const requestId = newId("older")
    const promise = new Promise<void>((resolve) => {
      this.history = {
        requestId,
        resolve,
        timer: setTimeout(() => {
          this.finishHistory()
          this.render()
        }, historyTimeoutMs),
      }
    })
    this.say({
      type: "dm-history",
      requestId,
      beforeSeq: this.live.messages.at(0)?.seq ?? 0,
      limit: DM_PAGE_SIZE,
    })
    return promise
  }

  // The socket's events.

  opened(): void {
    this.sayVisibility()
  }

  /** Tells the object whether the tab is visible; a hidden tab stops typing. */
  sayVisibility(): void {
    const socket = this.socket
    if (!socket || socket.readyState !== socket.OPEN) return
    const visible = tabVisible()
    socket.send(
      JSON.stringify({
        type: "dm-visibility",
        visible,
      } satisfies DmClientMessage)
    )
    if (!visible) this.setTyping(false)
  }

  /** A message on the socket; one from a socket the view has since replaced is not this view's. */
  heard(data: unknown, target?: unknown): void {
    if (!fromSocketOf(this.live.name, this.socket, target)) return
    const message = parseDmServerMessage(data)
    if (!message || !this.live.name || this.settled) return
    this.apply(message)
    this.render()
  }

  closed(event: { code: number; target?: unknown }): void {
    if (
      !this.mounted ||
      !this.live.name ||
      this.settled ||
      !fromSocketOf(this.live.name, this.socket, event.target)
    ) {
      return
    }
    this.finishHistory()
    this.stopPeerTyping()
    this.change({ open: false, dropped: true, typing: new Set() })
    this.render()
    // The object's close of a run it ended (a purge) is not reopened by the
    // SDK: the Worker says whether it may be.
    this.check(event.code === conversationGoneCloseCode)
  }

  dispose(): void {
    this.mounted = false
    this.stopTimers()
  }

  private apply(message: DmServerMessage): void {
    switch (message.type) {
      case "dm-snapshot":
        this.snapshot(message)
        return
      case "dm-message": {
        const arrived = message.message
        const first = this.live.messages.at(0)?.seq
        if (first !== undefined && arrived.seq < first) {
          // Older than anything held: a message sent long ago that the
          // object answered again from storage. Held here it would leave a
          // gap history never fills; it comes with the older pages.
          this.change({ hasOlder: true })
        } else {
          this.change({ messages: merged(this.live.messages, [arrived]) })
        }
        if (arrived.clientId !== undefined) this.confirmed(arrived.clientId)
        return
      }
      case "dm-send-failed":
        this.failed(message.clientId)
        return
      case "dm-history":
        if (message.requestId !== this.history?.requestId) return
        this.change({
          messages: merged(this.live.messages, message.messages),
          hasOlder: message.hasOlder,
        })
        this.finishHistory()
        return
      case "dm-read": {
        const reads = new Map(this.live.reads)
        reads.set(
          message.userId,
          Math.max(reads.get(message.userId) ?? 0, message.seq)
        )
        this.change({ reads })
        return
      }
      case "dm-typing":
        if (message.userId !== this.peerId) return
        if (message.typing) this.peerTyping()
        else this.stopPeerTyping()
        return
      case "dm-presence": {
        const online = new Set(this.live.online)
        if (message.online) online.add(message.userId)
        else online.delete(message.userId)
        this.change({ online })
        return
      }
      case "dm-gone":
        // A purge ended this run of the conversation. Whether the viewer
        // may open it again (with what stayed of it) is the Worker's to say.
        this.finishHistory()
        this.stopPeerTyping()
        this.change({ open: false, dropped: true, typing: new Set() })
        this.check(true)
        return
    }
  }

  /**
   * The conversation as the socket opens. What the view held is kept when
   * it meets or overlaps the latest page and is the same run of the
   * conversation; otherwise the page replaces it. Messages sent before are
   * confirmed by it, or sent again (the object stores each once).
   */
  private snapshot(
    message: Extract<DmServerMessage, { type: "dm-snapshot" }>
  ): void {
    const held = this.live
    const first = message.messages.at(0)?.seq
    const heldFirst = held.messages.at(0)?.seq
    const heldLast = held.messages.at(-1)?.seq
    const sameRun =
      !held.conversation || held.conversation === message.conversation
    let messages = message.messages
    let hasOlder = message.hasOlder
    if (
      sameRun &&
      first !== undefined &&
      heldFirst !== undefined &&
      heldLast !== undefined &&
      heldLast >= first - 1
    ) {
      messages = merged(held.messages, message.messages)
      // The oldest message held is still the view's oldest.
      if (heldFirst < first) hasOlder = held.hasOlder
    }
    this.stopPeerTyping()
    this.change({
      conversation: message.conversation,
      messages,
      hasOlder,
      reads: new Map(message.reads.map((read) => [read.userId, read.seq])),
      online: new Set(message.online),
      typing: new Set(),
      open: true,
      dropped: false,
    })
    if (message.typing.includes(this.peerId)) this.peerTyping()
    this.readSent = 0
    this.saidTyping = false
    const confirmed = new Set(
      messages.flatMap((item) =>
        item.clientId === undefined ? [] : [item.clientId]
      )
    )
    for (const item of this.live.outgoing) {
      if (confirmed.has(item.clientId)) this.confirmed(item.clientId)
      else if (item.status === "sending") this.transmit(item)
    }
  }

  /** Sends a message once the socket has the conversation, and fails it if the object never answers. */
  private transmit(item: Outgoing): void {
    const timer = this.sendTimers.get(item.clientId)
    if (timer) clearTimeout(timer)
    this.sendTimers.set(
      item.clientId,
      setTimeout(() => {
        this.failed(item.clientId)
        this.render()
      }, DM_SEND_TIMEOUT_MS)
    )
    // Until then it goes with the next snapshot.
    if (this.live.open) {
      this.say({ type: "dm-send", clientId: item.clientId, body: item.body })
    }
  }

  private confirmed(clientId: string): void {
    this.clearSendTimer(clientId)
    if (!this.live.outgoing.some((item) => item.clientId === clientId)) return
    this.change({
      outgoing: this.live.outgoing.filter((item) => item.clientId !== clientId),
    })
  }

  private failed(clientId: string): void {
    this.clearSendTimer(clientId)
    const item = this.live.outgoing.find(
      (candidate) => candidate.clientId === clientId
    )
    if (item?.status !== "sending") return
    this.change({
      outgoing: this.live.outgoing.map((candidate) =>
        candidate.clientId === clientId
          ? { ...candidate, status: "failed" }
          : candidate
      ),
    })
  }

  /** The socket will not open again for this DM: what was still sending failed. */
  private settle(refusal: Refusal): void {
    this.stopTimers()
    this.change({
      refusal,
      open: false,
      typing: new Set(),
      outgoing: this.live.outgoing.map((item) =>
        item.status === "sending" ? { ...item, status: "failed" } : item
      ),
    })
  }

  /**
   * Why the socket closed, or whether a run the object ended may be opened
   * again (`reopen`): the Worker makes the socket's own check with Go. 401
   * ends the session, 403 or 404 is not allowed; otherwise a socket the SDK
   * does not reopen by itself is reopened.
   */
  private check(reopen: boolean): void {
    if (reopen) this.reopenAfterCheck = true
    if (this.checking) return
    this.checking = true
    const name = this.live.name
    const reopenIfWanted = () => {
      if (this.live.name === name && !this.settled && this.reopenAfterCheck) {
        this.socket?.reconnect()
      }
    }
    checkConversationSocket(
      directMessagePath(this.organizationId, this.viewerId, this.peerId)
    )
      .then(reopenIfWanted, (error: unknown) => {
        if (this.live.name !== name) return
        if (error instanceof ApiError && error.status === 401) {
          this.settle("session_expired")
        } else if (
          error instanceof ApiError &&
          (error.status === 403 || error.status === 404)
        ) {
          this.settle("not-allowed")
        } else {
          // Anything else may pass: the socket keeps trying.
          reopenIfWanted()
          return
        }
        this.render()
      })
      .finally(() => {
        this.checking = false
        this.reopenAfterCheck = false
      })
  }

  private peerTyping(): void {
    if (this.typingTimer) clearTimeout(this.typingTimer)
    // The object says so again while it lasts; without that, it lapses here
    // too, a moment after it would there.
    this.typingTimer = setTimeout(() => {
      this.stopPeerTyping()
      this.render()
    }, DM_TYPING_TTL_MS + 1_000)
    if (!this.live.typing.has(this.peerId)) {
      this.change({ typing: new Set([...this.live.typing, this.peerId]) })
    }
  }

  private stopPeerTyping(): void {
    if (this.typingTimer) clearTimeout(this.typingTimer)
    this.typingTimer = undefined
    if (!this.live.typing.has(this.peerId)) return
    const typing = new Set(this.live.typing)
    typing.delete(this.peerId)
    this.change({ typing })
  }

  private historyDone(): Promise<void> {
    const history = this.history
    if (!history) return Promise.resolve()
    return new Promise((resolve) => {
      const previous = history.resolve
      history.resolve = () => {
        previous()
        resolve()
      }
    })
  }

  private finishHistory(): void {
    const history = this.history
    if (!history) return
    this.history = undefined
    clearTimeout(history.timer)
    history.resolve()
  }

  private clearSendTimer(clientId: string): void {
    const timer = this.sendTimers.get(clientId)
    if (timer) clearTimeout(timer)
    this.sendTimers.delete(clientId)
  }

  private stopTimers(): void {
    for (const timer of this.sendTimers.values()) clearTimeout(timer)
    this.sendTimers.clear()
    if (this.typingTimer) clearTimeout(this.typingTimer)
    this.typingTimer = undefined
    this.finishHistory()
  }

  private say(message: DmClientMessage): void {
    const socket = this.socket
    if (!socket || socket.readyState !== socket.OPEN) return
    socket.send(JSON.stringify(message))
  }

  private change(next: Partial<Live>): void {
    this.live = { ...this.live, ...next }
  }
}

/**
 * The DM between `viewerId` and `peerId` in `organizationId`, over its own
 * socket on the DM's object, while the calling component is mounted. Nothing
 * opens until all three are known and the two are different people.
 */
export function useDirectMessages(
  organizationId?: string,
  viewerId?: string,
  peerId?: string
): DirectMessages {
  const name =
    isNameId(organizationId) &&
    isNameId(viewerId) &&
    isNameId(peerId) &&
    viewerId !== peerId
      ? directMessageName(organizationId, viewerId, peerId)
      : ""
  const [, render] = useReducer((count: number) => count + 1, 0)
  const held = useRef<DirectMessagesView>(undefined)
  held.current ??= new DirectMessagesView(render)
  const view = held.current
  view.use(name, organizationId ?? "", viewerId ?? "", peerId ?? "")

  const socket = useAgent({
    protocol: socketProtocol(),
    agent: DIRECT_MESSAGE_AGENT,
    name: name || "closed",
    enabled: Boolean(name) && !view.settled,
    onOpen: () => view.opened(),
    onMessage: (event: MessageEvent) => view.heard(event.data, event.target),
    onClose: (event: CloseEvent) => view.closed(event),
  })
  view.socket = socket

  useEffect(() => {
    view.mounted = true
    return () => view.dispose()
  }, [view])

  // Whether the tab is visible is what makes the viewer online in the DM.
  useEffect(() => {
    if (!name) return
    const onChange = () => view.sayVisibility()
    document.addEventListener("visibilitychange", onChange)
    return () => document.removeEventListener("visibilitychange", onChange)
  }, [view, name])

  return {
    messages: view.messages(),
    hasOlder: view.live.hasOlder,
    loadOlder: view.loadOlder,
    send: view.send,
    retry: view.retry,
    markRead: view.markRead,
    peer: view.peer(),
    setTyping: view.setTyping,
    reconnect: view.reconnect,
    status: view.status(),
  }
}
