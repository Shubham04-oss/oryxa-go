// What travels on the sockets of the direct messages' objects
// (src/direct-message-agent.ts, src/inbox-agent.ts) and their hooks
// (useDirectMessages, useInbox), and the limits both sides keep. Every
// message is a JSON object with a `type`; each side parses what it is sent
// and drops anything else, the Agents SDK's own frames included.
import type { Conversation } from "./types"

/** The longest message, in Unicode code points after trimming. */
export const DM_MAX_BODY_LENGTH = 4_000

/** How many messages a socket is sent as it opens, and a history page holds unless it asks for fewer. */
export const DM_PAGE_SIZE = 50

/** The most messages one history page holds. */
export const DM_MAX_PAGE_SIZE = 100

/**
 * The most messages a conversation keeps: past this, the oldest go as new
 * ones arrive. At 4,000 characters a message, that bounds a conversation's
 * storage at about 160 MB (a typical one holds a small fraction of that).
 */
export const DM_HISTORY_LIMIT = 10_000

/** How long "typing" lasts after the last word of it, on the object and on a page. */
export const DM_TYPING_TTL_MS = 6_000

/** How often a page that is still typing says so again, within `DM_TYPING_TTL_MS`. */
export const DM_TYPING_REFRESH_MS = 2_500

/** A message preview's length in an inbox, in code points. */
export const PREVIEW_LENGTH = 160

/** The most unread messages an inbox counts in one conversation. */
export const MAX_UNREAD = 999

/**
 * The close code of a socket whose conversation or inbox was purged
 * (a member left, or the organization was deleted). The SDK does not
 * reopen a socket closed with it.
 */
export const conversationGoneCloseCode = 4404

// A page's ids for what it sends: a message (the object keeps it, so a
// retry is not stored twice) or a history request.
const clientIdPattern = /^[\w-]{8,64}$/
const requestIdPattern = /^[\w-]{1,64}$/

// The largest frame an object reads from a page: a message's 4,000 code
// points are at most 16,000 UTF-16 units escaped, with room to spare.
const maxClientFrame = 64 * 1024

/** A message as the object sends it: the page's `DmMessage` without a status. */
export type DmWireMessage = {
  id: string
  seq: number
  authorId: string
  body: string
  createdAt: string
  /** On the author's own messages only. */
  clientId?: string
}

/** What a page says on a DM's socket. */
export type DmClientMessage =
  /**
   * Whether the page's tab is visible: first on every socket, then on every
   * change. A person counts as online in the DM while one of their sockets
   * says it is.
   */
  | { type: "dm-visibility"; visible: boolean }
  /**
   * A message from the socket's own person, whoever the page says. `clientId`
   * is the page's id for it: the object stores a message once per author
   * and `clientId`, so sending it again (a retry) answers with the stored one.
   */
  | { type: "dm-send"; clientId: string; body: string }
  /** Up to `limit` messages before `beforeSeq` (0: the latest). */
  | { type: "dm-history"; requestId: string; beforeSeq: number; limit: number }
  /** The socket's person has read through `seq`. */
  | { type: "dm-read"; seq: number }
  /** The socket's person is typing, or stopped. Never stored. */
  | { type: "dm-typing"; typing: boolean }

/** Why the object refused a message. */
export type DmSendFailure =
  "empty" | "too_long" | "rate_limited" | "unavailable"

/** What the object says on a DM's socket. */
export type DmServerMessage =
  /**
   * The conversation as the socket opens: its latest page, whether older
   * messages exist, how far each person has read, who is online and who is
   * typing. `conversation` names this run of the conversation: it changes
   * when a purge empties it, and a page drops what it held from before.
   */
  | {
      type: "dm-snapshot"
      conversation: string
      messages: DmWireMessage[]
      hasOlder: boolean
      reads: DmRead[]
      online: string[]
      typing: string[]
    }
  /** A new message, to every socket, or a stored one to a socket that sent its `clientId` again. */
  | { type: "dm-message"; message: DmWireMessage }
  /** The object did not store a message; only the socket that sent it hears. */
  | {
      type: "dm-send-failed"
      clientId: string
      code: DmSendFailure
      message: string
    }
  /** A history page, oldest first, to the socket that asked. */
  | {
      type: "dm-history"
      requestId: string
      messages: DmWireMessage[]
      hasOlder: boolean
    }
  /** A person read further. */
  | ({ type: "dm-read" } & DmRead)
  /** The other person started or stopped typing. */
  | { type: "dm-typing"; userId: string; typing: boolean }
  /** A person came online in the DM or left it. */
  | { type: "dm-presence"; userId: string; online: boolean }
  /** The conversation was purged; the socket closes next with `conversationGoneCloseCode`. */
  | { type: "dm-gone" }

/** How far a person has read: through `seq`, 0 for nothing. */
export type DmRead = { userId: string; seq: number }

/** What the inbox's object says on its socket. A page says nothing on it. */
export type InboxServerMessage =
  /** Every conversation, latest first, as the socket opens. */
  | {
      type: "inbox-snapshot"
      conversations: Conversation[]
      unreadTotal: number
    }
  /** A conversation was added or changed. */
  | {
      type: "inbox-conversation"
      conversation: Conversation
      unreadTotal: number
    }
  /** A conversation left the inbox. */
  | { type: "inbox-removed"; name: string; unreadTotal: number }
  /** The inbox was purged; the socket closes next with `conversationGoneCloseCode`. */
  | { type: "inbox-gone" }

/** A page's message to a DM, or nothing for anything the object does not take. */
export function parseDmClientMessage(
  data: unknown
): DmClientMessage | undefined {
  const value = parsedObject(data, maxClientFrame)
  if (!value) return undefined
  switch (value.type) {
    case "dm-visibility":
      return typeof value.visible === "boolean"
        ? { type: "dm-visibility", visible: value.visible }
        : undefined
    case "dm-send":
      return isClientId(value.clientId) && typeof value.body === "string"
        ? { type: "dm-send", clientId: value.clientId, body: value.body }
        : undefined
    case "dm-history":
      if (
        typeof value.requestId !== "string" ||
        !requestIdPattern.test(value.requestId) ||
        !isSeq(value.beforeSeq) ||
        !isSeq(value.limit)
      ) {
        return undefined
      }
      return {
        type: "dm-history",
        requestId: value.requestId,
        beforeSeq: value.beforeSeq,
        limit: Math.min(Math.max(value.limit, 1), DM_MAX_PAGE_SIZE),
      }
    case "dm-read":
      return isSeq(value.seq) ? { type: "dm-read", seq: value.seq } : undefined
    case "dm-typing":
      return typeof value.typing === "boolean"
        ? { type: "dm-typing", typing: value.typing }
        : undefined
    default:
      return undefined
  }
}

/** The object's message on a DM's socket, or nothing for anything else there (the SDK's own frames). */
export function parseDmServerMessage(
  data: unknown
): DmServerMessage | undefined {
  const value = parsedObject(data)
  if (!value) return undefined
  switch (value.type) {
    case "dm-snapshot": {
      const messages = wireMessages(value.messages)
      const reads = Array.isArray(value.reads)
        ? (value.reads as unknown[]).filter(isRead)
        : undefined
      if (
        typeof value.conversation !== "string" ||
        !messages ||
        typeof value.hasOlder !== "boolean" ||
        !reads ||
        !isStrings(value.online) ||
        !isStrings(value.typing)
      ) {
        return undefined
      }
      return {
        type: "dm-snapshot",
        conversation: value.conversation,
        messages,
        hasOlder: value.hasOlder,
        reads: reads.map(({ userId, seq }) => ({ userId, seq })),
        online: value.online,
        typing: value.typing,
      }
    }
    case "dm-message":
      return isWireMessage(value.message)
        ? { type: "dm-message", message: wireMessage(value.message) }
        : undefined
    case "dm-send-failed":
      return isClientId(value.clientId) &&
        typeof value.code === "string" &&
        sendFailures.has(value.code) &&
        typeof value.message === "string"
        ? {
            type: "dm-send-failed",
            clientId: value.clientId,
            code: value.code as DmSendFailure,
            message: value.message,
          }
        : undefined
    case "dm-history": {
      const messages = wireMessages(value.messages)
      if (
        typeof value.requestId !== "string" ||
        !messages ||
        typeof value.hasOlder !== "boolean"
      ) {
        return undefined
      }
      return {
        type: "dm-history",
        requestId: value.requestId,
        messages,
        hasOlder: value.hasOlder,
      }
    }
    case "dm-read":
      return isRead(value)
        ? { type: "dm-read", userId: value.userId, seq: value.seq }
        : undefined
    case "dm-typing":
      return typeof value.userId === "string" &&
        typeof value.typing === "boolean"
        ? { type: "dm-typing", userId: value.userId, typing: value.typing }
        : undefined
    case "dm-presence":
      return typeof value.userId === "string" &&
        typeof value.online === "boolean"
        ? { type: "dm-presence", userId: value.userId, online: value.online }
        : undefined
    case "dm-gone":
      return { type: "dm-gone" }
    default:
      return undefined
  }
}

/** The inbox's message, or nothing for anything else on its socket. */
export function parseInboxServerMessage(
  data: unknown
): InboxServerMessage | undefined {
  const value = parsedObject(data)
  if (!value) return undefined
  switch (value.type) {
    case "inbox-snapshot": {
      if (!Array.isArray(value.conversations) || !isCount(value.unreadTotal)) {
        return undefined
      }
      const conversations = (value.conversations as unknown[]).filter(
        isConversation
      )
      return {
        type: "inbox-snapshot",
        conversations: conversations.map(conversation),
        unreadTotal: value.unreadTotal,
      }
    }
    case "inbox-conversation":
      return isConversation(value.conversation) && isCount(value.unreadTotal)
        ? {
            type: "inbox-conversation",
            conversation: conversation(value.conversation),
            unreadTotal: value.unreadTotal,
          }
        : undefined
    case "inbox-removed":
      return typeof value.name === "string" && isCount(value.unreadTotal)
        ? {
            type: "inbox-removed",
            name: value.name,
            unreadTotal: value.unreadTotal,
          }
        : undefined
    case "inbox-gone":
      return { type: "inbox-gone" }
    default:
      return undefined
  }
}

/** Whether `value` is an id a page may give a message it sends. */
export function isClientId(value: unknown): value is string {
  return typeof value === "string" && clientIdPattern.test(value)
}

/** A message body's length as the limits count it: Unicode code points. */
export function codePoints(text: string): number {
  // Spreading a string steps through code points, not UTF-16 units.
  return Array.from(text).length
}

/** A message as an inbox previews it: one line, at most `PREVIEW_LENGTH` code points. */
export function preview(body: string): string {
  const line = body.replace(/\s+/g, " ").trim()
  const characters = Array.from(line)
  if (characters.length <= PREVIEW_LENGTH) return line
  return `${characters
    .slice(0, PREVIEW_LENGTH - 1)
    .join("")
    .trimEnd()}…`
}

const sendFailures = new Set<string>([
  "empty",
  "too_long",
  "rate_limited",
  "unavailable",
])

function isSeq(value: unknown): value is number {
  return typeof value === "number" && Number.isSafeInteger(value) && value >= 0
}

function isCount(value: unknown): value is number {
  return isSeq(value)
}

function isStrings(value: unknown): value is string[] {
  return Array.isArray(value) && value.every((item) => typeof item === "string")
}

function isRead(value: unknown): value is DmRead {
  if (typeof value !== "object" || value === null) return false
  const { userId, seq } = value as { userId?: unknown; seq?: unknown }
  return typeof userId === "string" && isSeq(seq)
}

function isWireMessage(value: unknown): value is DmWireMessage {
  if (typeof value !== "object" || value === null) return false
  const message = value as Record<string, unknown>
  return (
    typeof message.id === "string" &&
    isSeq(message.seq) &&
    message.seq > 0 &&
    typeof message.authorId === "string" &&
    typeof message.body === "string" &&
    typeof message.createdAt === "string" &&
    (message.clientId === undefined || typeof message.clientId === "string")
  )
}

/** A wire message with only its own fields. */
function wireMessage(message: DmWireMessage): DmWireMessage {
  return {
    id: message.id,
    seq: message.seq,
    authorId: message.authorId,
    body: message.body,
    createdAt: message.createdAt,
    ...(message.clientId === undefined ? {} : { clientId: message.clientId }),
  }
}

function wireMessages(value: unknown): DmWireMessage[] | undefined {
  if (!Array.isArray(value)) return undefined
  return (value as unknown[]).filter(isWireMessage).map(wireMessage)
}

function isConversation(value: unknown): value is Conversation {
  if (typeof value !== "object" || value === null) return false
  const item = value as Record<string, unknown>
  return (
    (item.kind === "dm" || item.kind === "side") &&
    typeof item.name === "string" &&
    typeof item.peerId === "string" &&
    isCount(item.unread) &&
    typeof item.updatedAt === "string" &&
    (item.teamspaceId === undefined || typeof item.teamspaceId === "string") &&
    (item.teamspaceName === undefined ||
      typeof item.teamspaceName === "string") &&
    (item.lastMessage === undefined || isLastMessage(item.lastMessage))
  )
}

function isLastMessage(
  value: unknown
): value is NonNullable<Conversation["lastMessage"]> {
  if (typeof value !== "object" || value === null) return false
  const last = value as Record<string, unknown>
  return (
    typeof last.authorId === "string" &&
    typeof last.body === "string" &&
    typeof last.createdAt === "string"
  )
}

/** A conversation with only its own fields. */
function conversation(item: Conversation): Conversation {
  return {
    kind: item.kind,
    name: item.name,
    peerId: item.peerId,
    ...(item.teamspaceId === undefined
      ? {}
      : { teamspaceId: item.teamspaceId }),
    ...(item.teamspaceName === undefined
      ? {}
      : { teamspaceName: item.teamspaceName }),
    ...(item.lastMessage === undefined
      ? {}
      : {
          lastMessage: {
            authorId: item.lastMessage.authorId,
            body: item.lastMessage.body,
            createdAt: item.lastMessage.createdAt,
          },
        }),
    unread: item.unread,
    updatedAt: item.updatedAt,
  }
}

function parsedObject(
  data: unknown,
  maxLength = Number.POSITIVE_INFINITY
): Record<string, unknown> | undefined {
  if (typeof data !== "string" || data.length > maxLength) return undefined
  let value: unknown
  try {
    value = JSON.parse(data)
  } catch {
    return undefined
  }
  if (typeof value !== "object" || value === null || Array.isArray(value)) {
    return undefined
  }
  return value as Record<string, unknown>
}
