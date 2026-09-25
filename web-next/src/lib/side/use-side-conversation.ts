// The seam between a side conversation and a view of it: the teamspace
// page's right panel, where picking a teammate's agent opens it. Every view
// opens its own socket on the one object (REALTIME.md,
// src/side-conversation-agent.ts), so two views on a page each keep their
// own state and both stay current.
import { useEffect, useReducer, useRef } from "react"
import { useAgent } from "agents/react"
import { ApiError } from "@/lib/api/client"
import { getSession } from "@/lib/auth/client"
import { checkConversationSocket, sideConversationPath } from "./client"
import {
  SIDE_CONVERSATION_AGENT,
  isNameId,
  sideConversationName,
} from "./names"
import {
  SIDE_MAX_BODY_LENGTH,
  SIDE_PAGE_SIZE,
  SIDE_TYPING_REFRESH_MS,
  SIDE_TYPING_TTL_MS,
  codePoints,
  emptyRoster,
  parseSideServerMessage,
  sideGoneCloseCode,
} from "./protocol"
import type {
  SideClientMessage,
  SideFailure,
  SideReviewAction,
  SideRoster,
  SideServerMessage,
  SideWireItem,
} from "./protocol"
import { agentsOf } from "./roster"
import type { AgentRef, SideItem, SideMessage, SideRequestCard } from "./types"
import { socketProtocol } from "@/lib/socket-protocol"

/** Where the viewer stands with the side conversation. */
export type SideConversationStatus =
  /** Nothing to open: no organization, viewer, teamspace or peer yet, or the viewer is the peer. */
  | "closed"
  /** The socket is opening, or has not been sent the conversation yet. */
  | "connecting"
  /** The conversation is open and arrives as it is said. */
  | "live"
  /** The Worker refused the viewer (403, or 404: the other person is not on the teamspace's team, or the teamspace is gone). For good. */
  | "not-allowed"
  /** The socket dropped and is trying again, or the Worker could not check it. */
  | "error"
  /** The session ended and could not be refreshed. Sign in again. */
  | "session_expired"

/** Why the object refused a decision, a review or a confirmation: its code, and its words as the message. */
export class SideActionError extends Error {
  constructor(
    message: string,
    readonly code: SideFailure
  ) {
    super(message)
    this.name = "SideActionError"
  }
}

/**
 * A side conversation as a view sees it.
 *
 * - `items`: the transcript oldest first (`SideItem`, types.ts): people's
 *   messages, agents' messages, request cards and quiet status lines; the
 *   viewer's own not confirmed yet last.
 * - `send(body)`: a message; it shows at once as `sending`, then as the
 *   object stored it, or `failed` (`retry(clientId)` sends it again under the
 *   same page id, so it is never stored twice).
 * - `askMyAgent(body)`: a message that asks the viewer's own agent
 *   (`agents.mine`), which answers for both people to see; its answer
 *   streams in as an agent message whose `replyTo` is the question's seq.
 *   The page calls this when the member picks "ask my agent", rather than
 *   the conversation guessing from what they typed.
 * - `askTeammateAgent(agentId, body)`: asks one of `agents.theirs` for
 *   something: a request card, `sending` until Go has it; then its owner
 *   approves it (unless it decides for itself), and the result lands here.
 * - `decide(requestId, decision, message?, allowedTools?)`: the viewer
 *   approves or declines a request held for them, as themself. An approval
 *   allows `allowedTools`, among the card's `requestedTools`, or none.
 *   Resolves once the card shows the decision; rejects with a
 *   `SideActionError` saying why not.
 * - `review(requestId, action)`: the viewer shares an answer held for their
 *   review (`send`), shares it fully masked (`send_masked`), or does not
 *   (`withhold`), as themself. Resolves once the card is past review;
 *   rejects with a `SideActionError`.
 * - `confirm(seq, answers)`: the viewer answers every confirmation their own
 *   agent's message `seq` waits on. Resolves once it goes on; rejects with a
 *   `SideActionError`.
 * - `peer`: the other person: online (a visible tab here), typing, and how
 *   far they have read.
 * - `agents`: the viewer's own agent (none when they have no agent seat in
 *   the teamspace), and the other person's: their primary agent first, then
 *   the specialists they contributed.
 */
export type SideConversation = {
  items: SideItem[]
  hasOlder: boolean
  /** Loads the page before the oldest item held; resolves once it arrives, or the socket closed first. */
  loadOlder: () => Promise<void>
  send: (body: string) => void
  retry: (clientId: string) => void
  /** Reads the conversation through its latest item, while the tab is visible. */
  markRead: () => void
  /** Says the viewer is typing (again every few seconds while true), or stopped. */
  setTyping: (typing: boolean) => void
  askMyAgent: (body: string) => void
  askTeammateAgent: (agentId: string, body: string) => void
  decide: (
    requestId: string,
    decision: "approved" | "rejected",
    message?: string,
    allowedTools?: readonly string[]
  ) => Promise<void>
  review: (requestId: string, action: SideReviewAction) => Promise<void>
  confirm: (
    seq: number,
    answers: Array<{ id: string; confirmed: boolean }>
  ) => Promise<void>
  peer: { online: boolean; typing: boolean; lastReadSeq: number }
  agents: { mine: AgentRef | undefined; theirs: AgentRef[] }
  /** The teamspace it is in, and whether it is paused (its agents can't be asked then). */
  teamspace: { name: string; paused: boolean }
  /** Opens the socket again now rather than at its next try, while it is not open (`connecting` or `error`); what is waiting to send goes once it opens. */
  reconnect: () => void
  status: SideConversationStatus
}

/** How long a message may wait for the object before it shows as failed. */
export const SIDE_SEND_TIMEOUT_MS = 15_000

/** How long a request to a teammate's agent may take to be filed before it shows as not filed. */
export const SIDE_FILE_TIMEOUT_MS = 45_000

/** How long a decision, a review or a confirmation may wait for the object. */
export const SIDE_ACTION_TIMEOUT_MS = 30_000

// How long a history page may take before `loadOlder` gives up on it.
const historyTimeoutMs = 15_000

type Refusal = "not-allowed" | "session_expired"

/** Something the viewer sent that the object has not confirmed. */
type Outgoing = {
  clientId: string
  kind: "message" | "ask-mine" | "ask-teammate"
  body: string
  /** The agent asked: the viewer's own, or the teammate's. */
  agentId?: string
  createdAt: string
  status: "sending" | "failed"
  error?: string
  /** It went again after the session was renewed: another ended session fails it. */
  renewed?: boolean
}

/** A decision, a review or a confirmation on its way. */
type Action = {
  kind: "decide" | "review" | "confirm"
  message: SideClientMessage
  resolve: () => void
  reject: (error: SideActionError) => void
  timer: ReturnType<typeof setTimeout>
  renewed?: boolean
}

/** What the socket has said since the view asked for this conversation. */
type Live = {
  name: string
  conversation?: string
  /** The object's items, by seq. */
  items: SideWireItem[]
  hasOlder: boolean
  reads: ReadonlyMap<string, number>
  online: ReadonlySet<string>
  typing: ReadonlySet<string>
  roster: SideRoster
  outgoing: Outgoing[]
  /** The last refusal of a decision or a review, by request. */
  decisionErrors: ReadonlyMap<string, string>
  open: boolean
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
    items: [],
    hasOlder: false,
    reads: new Map(),
    online: new Set(),
    typing: new Set(),
    roster: emptyRoster,
    outgoing: [],
    decisionErrors: new Map(),
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

/** Folds items into held ones: one per seq, the newer copy winning, in order. */
function merged(
  held: readonly SideWireItem[],
  arrived: readonly SideWireItem[]
): SideWireItem[] {
  const bySeq = new Map(held.map((item) => [item.seq, item]))
  for (const item of arrived) bySeq.set(item.seq, item)
  return [...bySeq.values()].sort((left, right) => left.seq - right.seq)
}

function statusOf(live: Live): SideConversationStatus {
  if (!live.name) return "closed"
  if (live.refusal) return live.refusal
  if (live.open) return "live"
  return live.dropped ? "error" : "connecting"
}

/** Whether an event came from the socket of the conversation the view shows now (see useDirectMessages). */
function fromSocketOf(name: string, socket: unknown, target: unknown): boolean {
  if (target === undefined || target === null) return true
  const room = (target as { room?: unknown }).room
  if (typeof room === "string") return room === name
  return target === socket
}

function toAgentRef(
  agent: ReturnType<typeof agentsOf>[number],
  ownerId: string
): AgentRef {
  return {
    id: agent.id,
    name: agent.name,
    ownerId,
    kind: agent.kind,
    approval: agent.approval,
    ...(agent.role ? { role: agent.role } : {}),
  }
}

/**
 * One view's side conversation: what its socket said, what it sent, and its
 * timers. Its methods keep their identity for the view's life.
 */
class SideConversationView {
  live: Live = initialLive("")
  socket?: Socket
  mounted = false
  private organizationId = ""
  private viewerId = ""
  private teamspaceId = ""
  private peerId = ""
  private readonly sendTimers = new Map<string, ReturnType<typeof setTimeout>>()
  private readonly actions = new Map<string, Action>()
  private typingTimer?: ReturnType<typeof setTimeout>
  private history?: {
    requestId: string
    resolve: () => void
    timer: ReturnType<typeof setTimeout>
  }
  private checking = false
  private reopenAfterCheck = false
  private renewing = false
  /** The view closed its socket to reopen it with a fresh cookie: that close needs no check. */
  private reopening = false
  private saidTyping = false
  private typedAt = 0
  private readSent = 0
  private shown?: { live: Live; items: SideItem[] }
  private shownPeer?: SideConversation["peer"]
  private shownAgents?: {
    roster: SideRoster
    agents: SideConversation["agents"]
  }
  private shownTeamspace?: SideConversation["teamspace"]

  constructor(private readonly render: () => void) {}

  /** The conversation the view shows: another one starts over. */
  use(
    name: string,
    organizationId: string,
    viewerId: string,
    teamspaceId: string,
    peerId: string
  ): void {
    if (this.live.name === name) return
    this.stopTimers()
    this.live = initialLive(name)
    this.organizationId = organizationId
    this.viewerId = viewerId
    this.teamspaceId = teamspaceId
    this.peerId = peerId
    this.saidTyping = false
    this.readSent = 0
  }

  get settled(): boolean {
    return this.live.refusal !== undefined
  }

  /** The transcript as the view renders it, kept while nothing changed. */
  items(): SideItem[] {
    if (this.shown?.live === this.live) return this.shown.items
    const { items, outgoing, decisionErrors } = this.live
    const confirmed = new Set(
      items.flatMap((item) =>
        "clientId" in item && item.clientId !== undefined ? [item.clientId] : []
      )
    )
    const deciding = new Set(
      [...this.actions.values()].flatMap((action) =>
        action.message.type === "side-decide" ||
        action.message.type === "side-review"
          ? [action.message.requestId]
          : []
      )
    )
    const shown: SideItem[] = items.map((item): SideItem => {
      if (item.kind !== "request-card") return item
      const error = decisionErrors.get(item.requestId)
      return {
        ...item,
        ...(deciding.has(item.requestId) ? { deciding: true } : {}),
        ...(error ? { decisionError: error } : {}),
      }
    })
    for (const item of outgoing) {
      if (confirmed.has(item.clientId)) continue
      shown.push(this.pending(item))
    }
    this.shown = { live: this.live, items: shown }
    return shown
  }

  /** Something the viewer sent, as the view shows it until the object confirms it. */
  private pending(item: Outgoing): SideMessage | SideRequestCard {
    if (item.kind === "ask-teammate") {
      const mine = this.agents().mine
      const target = this.agents().theirs.find(
        (agent) => agent.id === item.agentId
      )
      return {
        kind: "request-card",
        id: item.clientId,
        seq: 0,
        requestId: "",
        requesterId: this.viewerId,
        requesterAgentId: mine?.id ?? "",
        requesterAgentName: mine?.name ?? "",
        targetId: this.peerId,
        targetAgentId: item.agentId ?? "",
        targetAgentName: target?.name ?? "",
        approval: target?.approval ?? "owner",
        status: item.status === "sending" ? "sending" : "not_filed",
        task: item.body,
        createdAt: item.createdAt,
        updatedAt: item.createdAt,
        expiresAt: "",
        clientId: item.clientId,
        ...(item.error ? { error: item.error } : {}),
      }
    }
    return {
      kind: "message",
      id: item.clientId,
      seq: 0,
      authorId: this.viewerId,
      body: item.body,
      createdAt: item.createdAt,
      clientId: item.clientId,
      ...(item.kind === "ask-mine" && item.agentId
        ? { asksAgentId: item.agentId }
        : {}),
      status: item.status,
      ...(item.error ? { error: item.error } : {}),
    }
  }

  /** The other person as the view renders them, kept while nothing about them changed. */
  peer(): SideConversation["peer"] {
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

  /** Both people's agents, kept while the roster is the same. */
  agents(): SideConversation["agents"] {
    const { roster } = this.live
    if (this.shownAgents?.roster === roster) return this.shownAgents.agents
    const mine = agentsOf(roster, this.viewerId).find(
      (agent) => agent.kind === "primary"
    )
    const agents = {
      mine: mine ? toAgentRef(mine, this.viewerId) : undefined,
      theirs: agentsOf(roster, this.peerId).map((agent) =>
        toAgentRef(agent, this.peerId)
      ),
    }
    this.shownAgents = { roster, agents }
    return agents
  }

  teamspace(): SideConversation["teamspace"] {
    const { teamspaceName, paused } = this.live.roster
    const shown = this.shownTeamspace
    if (shown?.name === teamspaceName && shown.paused === paused) return shown
    this.shownTeamspace = { name: teamspaceName, paused }
    return this.shownTeamspace
  }

  status(): SideConversationStatus {
    return statusOf(this.live)
  }

  // The view's actions.

  readonly send = (body: string): void => {
    this.queue("message", body)
  }

  readonly askMyAgent = (body: string): void => {
    this.queue("ask-mine", body, this.agents().mine?.id)
  }

  readonly askTeammateAgent = (agentId: string, body: string): void => {
    this.queue("ask-teammate", body, agentId)
  }

  readonly retry = (clientId: string): void => {
    const item = this.live.outgoing.find(
      (candidate) => candidate.clientId === clientId
    )
    if (
      !item ||
      item.status !== "failed" ||
      this.settled ||
      codePoints(item.body) > SIDE_MAX_BODY_LENGTH
    ) {
      return
    }
    const again: Outgoing = {
      ...item,
      status: "sending",
      error: undefined,
      renewed: false,
    }
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
    const latest = this.live.items.at(-1)?.seq ?? 0
    const read = this.live.reads.get(this.viewerId) ?? 0
    if (latest <= read || latest <= this.readSent) return
    this.readSent = latest
    this.say({ type: "side-read", seq: latest })
  }

  readonly setTyping = (typing: boolean): void => {
    if (!this.live.open) {
      this.saidTyping = false
      return
    }
    if (!typing) {
      if (this.saidTyping) this.say({ type: "side-typing", typing: false })
      this.saidTyping = false
      return
    }
    const now = Date.now()
    if (this.saidTyping && now - this.typedAt < SIDE_TYPING_REFRESH_MS) return
    this.saidTyping = true
    this.typedAt = now
    this.say({ type: "side-typing", typing: true })
  }

  readonly reconnect = (): void => {
    if (!this.live.name || this.settled || this.live.open) return
    this.socket?.reconnect()
  }

  readonly decide = (
    requestId: string,
    decision: "approved" | "rejected",
    message?: string,
    allowedTools?: readonly string[]
  ): Promise<void> => {
    const card = this.live.items.find(
      (item) => item.kind === "request-card" && item.requestId === requestId
    )
    if (
      !card ||
      card.kind !== "request-card" ||
      card.targetId !== this.viewerId ||
      card.approval !== "owner" ||
      card.status !== "pending"
    ) {
      return Promise.reject(
        new SideActionError(
          "There is no request of yours waiting for that decision.",
          "not_allowed"
        )
      )
    }
    const errors = new Map(this.live.decisionErrors)
    errors.delete(requestId)
    this.change({ decisionErrors: errors })
    return this.act(`decide:${requestId}`, "decide", {
      type: "side-decide",
      requestId,
      decision,
      ...(message?.trim() ? { message: message.trim() } : {}),
      // Only an approval allows tools, and only those the page names.
      ...(decision === "approved" && allowedTools
        ? { allowedTools: [...allowedTools] }
        : {}),
    })
  }

  readonly review = (
    requestId: string,
    action: SideReviewAction
  ): Promise<void> => {
    const card = this.live.items.find(
      (item) => item.kind === "request-card" && item.requestId === requestId
    )
    if (
      !card ||
      card.kind !== "request-card" ||
      card.targetId !== this.viewerId ||
      card.status !== "review"
    ) {
      return Promise.reject(
        new SideActionError(
          "There is no answer of yours waiting for that review.",
          "not_allowed"
        )
      )
    }
    // An answer the guard could not check has no masked version.
    if (
      action === "send_masked" &&
      card.heldAnswer &&
      card.heldAnswer.masked === undefined
    ) {
      return Promise.reject(
        new SideActionError(
          "This answer has no masked version to send.",
          "not_allowed"
        )
      )
    }
    const errors = new Map(this.live.decisionErrors)
    errors.delete(requestId)
    this.change({ decisionErrors: errors })
    return this.act(`review:${requestId}`, "review", {
      type: "side-review",
      requestId,
      action,
    })
  }

  readonly confirm = (
    seq: number,
    answers: Array<{ id: string; confirmed: boolean }>
  ): Promise<void> => {
    const item = this.live.items.find((each) => each.seq === seq)
    if (
      !item ||
      item.kind !== "agent-message" ||
      item.ownerId !== this.viewerId ||
      item.status !== "waiting"
    ) {
      return Promise.reject(
        new SideActionError(
          "There is nothing of yours waiting for that answer.",
          "not_allowed"
        )
      )
    }
    return this.act(`confirm:${seq}`, "confirm", {
      type: "side-confirm",
      seq,
      answers: answers.map(({ id, confirmed }) => ({ id, confirmed })),
    })
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
      type: "side-history",
      requestId,
      beforeSeq: this.live.items.at(0)?.seq ?? 0,
      limit: SIDE_PAGE_SIZE,
    })
    return promise
  }

  // The socket's events.

  opened(): void {
    this.reopening = false
    this.sayVisibility()
  }

  /** Tells the object whether the tab is visible; a hidden tab stops typing. */
  sayVisibility(): void {
    const socket = this.socket
    if (!socket || socket.readyState !== socket.OPEN) return
    const visible = tabVisible()
    socket.send(
      JSON.stringify({
        type: "side-visibility",
        visible,
      } satisfies SideClientMessage)
    )
    if (!visible) this.setTyping(false)
  }

  /** A message on the socket; one from a socket the view has since replaced is not this view's. */
  heard(data: unknown, target?: unknown): void {
    if (!fromSocketOf(this.live.name, this.socket, target)) return
    const message = parseSideServerMessage(data)
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
    if (this.reopening && event.code !== sideGoneCloseCode) return
    // The object's close of a run it ended (a purge) is not reopened by the
    // SDK: the Worker says whether it may be.
    this.check(event.code === sideGoneCloseCode)
  }

  dispose(): void {
    this.mounted = false
    this.stopTimers()
  }

  private queue(kind: Outgoing["kind"], body: string, agentId?: string): void {
    if (!this.live.name || this.settled) return
    const text = body.trim()
    if (!text) return
    const item: Outgoing = {
      clientId: newId("sdc"),
      kind,
      body: text,
      ...(agentId ? { agentId } : {}),
      createdAt: new Date().toISOString(),
      // Too long for the object: shown as failed at once, never sent.
      status: codePoints(text) > SIDE_MAX_BODY_LENGTH ? "failed" : "sending",
    }
    this.change({ outgoing: [...this.live.outgoing, item] })
    // The object stops the sender's typing as the message arrives.
    this.saidTyping = false
    if (item.status === "sending") this.transmit(item)
    this.render()
  }

  private apply(message: SideServerMessage): void {
    switch (message.type) {
      case "side-snapshot":
        this.snapshot(message)
        return
      case "side-item":
        this.arrived(message.item)
        return
      case "side-delta": {
        const item = this.live.items.find((each) => each.seq === message.seq)
        // Only where the view's copy ends: anything else waits for the
        // object's next whole copy.
        if (item?.kind !== "agent-message" || item.body.length !== message.at) {
          return
        }
        this.change({
          items: merged(this.live.items, [
            { ...item, body: item.body + message.text },
          ]),
        })
        return
      }
      case "side-failed":
        this.failed(message.clientId, message.code, message.message)
        return
      case "side-refused":
        this.refused(message.target, message.code, message.message)
        return
      case "side-history":
        if (message.requestId !== this.history?.requestId) return
        this.change({
          items: merged(this.live.items, message.items),
          hasOlder: message.hasOlder,
        })
        this.finishHistory()
        return
      case "side-read": {
        const reads = new Map(this.live.reads)
        reads.set(
          message.userId,
          Math.max(reads.get(message.userId) ?? 0, message.seq)
        )
        this.change({ reads })
        return
      }
      case "side-typing":
        if (message.userId !== this.peerId) return
        if (message.typing) this.peerTyping()
        else this.stopPeerTyping()
        return
      case "side-presence": {
        const online = new Set(this.live.online)
        if (message.online) online.add(message.userId)
        else online.delete(message.userId)
        this.change({ online })
        return
      }
      case "side-roster":
        this.change({ roster: message.roster })
        return
      case "side-session-expired":
        this.renew()
        return
      case "side-gone":
        // A purge ended this run of the conversation. Whether the viewer
        // may open it again (with what stayed of it) is the Worker's to say.
        this.finishHistory()
        this.stopPeerTyping()
        this.change({ open: false, dropped: true, typing: new Set() })
        this.check(true)
        return
    }
  }

  /** An item new or changed: what it confirms, and the decision or confirmation it answers. */
  private arrived(item: SideWireItem): void {
    const first = this.live.items.at(0)?.seq
    if (first !== undefined && item.seq < first) {
      // Older than anything held: it comes with the older pages.
      this.change({ hasOlder: true })
    } else {
      this.change({ items: merged(this.live.items, [item]) })
    }
    if ("clientId" in item && item.clientId !== undefined) {
      this.confirmed(item.clientId)
    }
    if (item.kind === "request-card" && item.status !== "pending") {
      this.finishAction(`decide:${item.requestId}`)
    }
    if (item.kind === "request-card" && item.status !== "review") {
      this.finishAction(`review:${item.requestId}`)
    }
    if (item.kind === "agent-message" && item.status !== "waiting") {
      this.finishAction(`confirm:${item.seq}`)
    }
  }

  /**
   * The conversation as the socket opens. What the view held is kept when
   * it meets or overlaps the latest page and is the same run of the
   * conversation; otherwise the page replaces it. What was sent before is
   * confirmed by it, or sent again (the object keeps each once).
   */
  private snapshot(
    message: Extract<SideServerMessage, { type: "side-snapshot" }>
  ): void {
    const held = this.live
    const first = message.items.at(0)?.seq
    const heldFirst = held.items.at(0)?.seq
    const heldLast = held.items.at(-1)?.seq
    const sameRun =
      !held.conversation || held.conversation === message.conversation
    let items = message.items
    let hasOlder = message.hasOlder
    if (
      sameRun &&
      first !== undefined &&
      heldFirst !== undefined &&
      heldLast !== undefined &&
      heldLast >= first - 1
    ) {
      items = merged(held.items, message.items)
      if (heldFirst < first) hasOlder = held.hasOlder
    }
    this.stopPeerTyping()
    this.change({
      conversation: message.conversation,
      items,
      hasOlder,
      reads: new Map(message.reads.map((read) => [read.userId, read.seq])),
      online: new Set(message.online),
      typing: new Set(),
      roster: message.roster,
      open: true,
      dropped: false,
    })
    if (message.typing.includes(this.peerId)) this.peerTyping()
    this.readSent = 0
    this.saidTyping = false
    const confirmed = new Set(
      items.flatMap((item) =>
        "clientId" in item && item.clientId !== undefined ? [item.clientId] : []
      )
    )
    for (const item of this.live.outgoing) {
      if (confirmed.has(item.clientId)) this.confirmed(item.clientId)
      else if (item.status === "sending") this.transmit(item)
    }
    // Decisions and confirmations still on their way go again: the object
    // takes each once, and answers one already done with where it stands.
    for (const action of this.actions.values()) this.say(action.message)
  }

  /** Sends something once the socket has the conversation, and fails it if the object never answers. */
  private transmit(item: Outgoing): void {
    const timer = this.sendTimers.get(item.clientId)
    if (timer) clearTimeout(timer)
    this.sendTimers.set(
      item.clientId,
      setTimeout(
        () => {
          this.failed(
            item.clientId,
            "unavailable",
            "That couldn't be sent. Try again."
          )
          this.render()
        },
        item.kind === "ask-teammate"
          ? SIDE_FILE_TIMEOUT_MS
          : SIDE_SEND_TIMEOUT_MS
      )
    )
    // Until then it goes with the next snapshot.
    if (!this.live.open) return
    switch (item.kind) {
      case "message":
        this.say({
          type: "side-send",
          clientId: item.clientId,
          body: item.body,
        })
        return
      case "ask-mine":
        this.say({
          type: "side-ask-mine",
          clientId: item.clientId,
          body: item.body,
        })
        return
      case "ask-teammate":
        this.say({
          type: "side-ask-teammate",
          clientId: item.clientId,
          agentId: item.agentId ?? "",
          body: item.body,
        })
        return
    }
  }

  private confirmed(clientId: string): void {
    this.clearSendTimer(clientId)
    if (!this.live.outgoing.some((item) => item.clientId === clientId)) return
    this.change({
      outgoing: this.live.outgoing.filter((item) => item.clientId !== clientId),
    })
  }

  /**
   * The object refused something sent. An ended session is renewed and it
   * goes again, once; anything else shows it failed, with the object's words.
   */
  private failed(clientId: string, code: SideFailure, words: string): void {
    const item = this.live.outgoing.find(
      (candidate) => candidate.clientId === clientId
    )
    if (item?.status !== "sending") {
      this.clearSendTimer(clientId)
      return
    }
    if (code === "session_expired" && !item.renewed) {
      // Nothing was done: once the socket carries a fresh cookie, the
      // snapshot sends it again.
      this.change({
        outgoing: this.live.outgoing.map((candidate) =>
          candidate.clientId === clientId
            ? { ...candidate, renewed: true }
            : candidate
        ),
      })
      this.renew()
      return
    }
    this.clearSendTimer(clientId)
    this.change({
      outgoing: this.live.outgoing.map((candidate) =>
        candidate.clientId === clientId
          ? { ...candidate, status: "failed", error: words }
          : candidate
      ),
    })
  }

  /**
   * The object refused a decision, a review or a confirmation: a
   * confirmation names its message's seq, the others their request, which
   * is never waiting on a decision and a review at once.
   */
  private refused(target: string, code: SideFailure, words: string): void {
    const key = /^\d+$/.test(target)
      ? `confirm:${target}`
      : this.actions.has(`decide:${target}`)
        ? `decide:${target}`
        : `review:${target}`
    const action = this.actions.get(key)
    if (!action) return
    if (code === "session_expired" && !action.renewed) {
      action.renewed = true
      this.renew()
      return
    }
    if (action.kind === "decide" || action.kind === "review") {
      const errors = new Map(this.live.decisionErrors)
      errors.set(target, words)
      this.change({ decisionErrors: errors })
    }
    this.finishAction(key, new SideActionError(words, code))
  }

  /** Sends a decision or a confirmation, resolved by the item it changes. */
  private act(
    key: string,
    kind: Action["kind"],
    message: SideClientMessage
  ): Promise<void> {
    if (!this.live.open) {
      return Promise.reject(
        new SideActionError(
          "The conversation is not connected. Try again in a moment.",
          "unavailable"
        )
      )
    }
    const running = this.actions.get(key)
    if (running) {
      return new Promise((resolve, reject) => {
        const { resolve: before, reject: beforeReject } = running
        running.resolve = () => {
          before()
          resolve()
        }
        running.reject = (error) => {
          beforeReject(error)
          reject(error)
        }
      })
    }
    const promise = new Promise<void>((resolve, reject) => {
      this.actions.set(key, {
        kind,
        message,
        resolve,
        reject,
        timer: setTimeout(() => {
          this.finishAction(
            key,
            new SideActionError("That took too long. Try again.", "unavailable")
          )
          this.render()
        }, SIDE_ACTION_TIMEOUT_MS),
      })
    })
    this.say(message)
    // A card shows it is being decided.
    this.change({})
    this.render()
    return promise
  }

  private finishAction(key: string, error?: SideActionError): void {
    const action = this.actions.get(key)
    if (!action) return
    this.actions.delete(key)
    clearTimeout(action.timer)
    if (error) action.reject(error)
    else action.resolve()
    this.change({})
  }

  /**
   * The object no longer takes this socket's cookie. Reading the session
   * refreshes it, and a reopened socket carries the new one to the object,
   * whose snapshot sends again what was waiting.
   */
  private renew(): void {
    if (this.renewing) return
    this.renewing = true
    const name = this.live.name
    getSession()
      .then(
        () => {
          if (this.live.name === name && !this.settled) {
            this.reopening = true
            this.socket?.reconnect()
          }
        },
        (error: unknown) => {
          if (this.live.name !== name) return
          if (error instanceof ApiError && error.status === 401) {
            this.settle("session_expired")
            this.render()
          }
        }
      )
      .finally(() => {
        this.renewing = false
      })
  }

  /** The socket will not open again for this conversation: what was still sending failed. */
  private settle(refusal: Refusal): void {
    const failures = [...this.actions.keys()]
    this.stopTimers()
    for (const key of failures) {
      this.finishAction(
        key,
        new SideActionError("The conversation closed.", "unavailable")
      )
    }
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
      sideConversationPath(
        this.organizationId,
        this.teamspaceId,
        this.viewerId,
        this.peerId
      )
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
    }, SIDE_TYPING_TTL_MS + 1_000)
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
    for (const key of [...this.actions.keys()]) {
      this.finishAction(
        key,
        new SideActionError("The conversation closed.", "unavailable")
      )
    }
    if (this.typingTimer) clearTimeout(this.typingTimer)
    this.typingTimer = undefined
    this.finishHistory()
  }

  private say(message: SideClientMessage): void {
    const socket = this.socket
    if (!socket || socket.readyState !== socket.OPEN) return
    socket.send(JSON.stringify(message))
  }

  private change(next: Partial<Live>): void {
    this.live = { ...this.live, ...next }
  }
}

/**
 * The side conversation between `viewerId` and `peerId` in the teamspace
 * `teamspaceId` of `organizationId`, over its own socket on the object,
 * while the calling component is mounted. Nothing opens until all four are
 * known and the two are different people.
 */
export function useSideConversation(
  organizationId?: string,
  viewerId?: string,
  teamspaceId?: string,
  peerId?: string
): SideConversation {
  const name =
    isNameId(organizationId) &&
    isNameId(viewerId) &&
    isNameId(teamspaceId) &&
    isNameId(peerId) &&
    viewerId !== peerId
      ? sideConversationName(organizationId, teamspaceId, viewerId, peerId)
      : ""
  const [, render] = useReducer((count: number) => count + 1, 0)
  const held = useRef<SideConversationView>(undefined)
  held.current ??= new SideConversationView(render)
  const view = held.current
  view.use(
    name,
    organizationId ?? "",
    viewerId ?? "",
    teamspaceId ?? "",
    peerId ?? ""
  )

  const socket = useAgent({
    protocol: socketProtocol(),
    agent: SIDE_CONVERSATION_AGENT,
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

  // Whether the tab is visible is what makes the viewer online here.
  useEffect(() => {
    if (!name) return
    const onChange = () => view.sayVisibility()
    document.addEventListener("visibilitychange", onChange)
    return () => document.removeEventListener("visibilitychange", onChange)
  }, [view, name])

  return {
    items: view.items(),
    hasOlder: view.live.hasOlder,
    loadOlder: view.loadOlder,
    send: view.send,
    retry: view.retry,
    markRead: view.markRead,
    setTyping: view.setTyping,
    askMyAgent: view.askMyAgent,
    askTeammateAgent: view.askTeammateAgent,
    decide: view.decide,
    review: view.review,
    confirm: view.confirm,
    peer: view.peer(),
    agents: view.agents(),
    teamspace: view.teamspace(),
    reconnect: view.reconnect,
    status: view.status(),
  }
}
