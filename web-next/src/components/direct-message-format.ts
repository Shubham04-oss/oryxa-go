// How a direct message reads on screen (DESIGN.md §3, "Direct messages"):
// days, then each person's messages within a minute as one group; where
// "Seen" goes; and what an inbox row says about a conversation. Pure, so the
// views and their tests share one set of rules.
import type { Conversation, DmMessage } from "@/lib/dms"
import type { Role } from "@/lib/auth/client"

const minute = 60_000
const hour = 60 * minute
const day = 24 * hour

const clockFormat = new Intl.DateTimeFormat(undefined, {
  hour: "numeric",
  minute: "2-digit",
})
const weekdayFormat = new Intl.DateTimeFormat(undefined, { weekday: "short" })
const dayThisYearFormat = new Intl.DateTimeFormat(undefined, {
  weekday: "short",
  day: "numeric",
  month: "short",
})
const dateThisYearFormat = new Intl.DateTimeFormat(undefined, {
  day: "numeric",
  month: "short",
})
const dateFormat = new Intl.DateTimeFormat(undefined, {
  day: "numeric",
  month: "short",
  year: "numeric",
})

function parsed(value: string): Date | undefined {
  const at = Date.parse(value)
  return Number.isNaN(at) ? undefined : new Date(at)
}

/** The local calendar day of a moment, as `YYYY-MM-DD`. */
function localDay(at: Date): string {
  const month = String(at.getMonth() + 1).padStart(2, "0")
  const date = String(at.getDate()).padStart(2, "0")
  return `${at.getFullYear()}-${month}-${date}`
}

/** The local calendar day before `at`'s. */
function dayBefore(at: Date): string {
  const before = new Date(at)
  before.setDate(before.getDate() - 1)
  return localDay(before)
}

/** A day separator's words: "Today", "Yesterday", or the date. */
export function dayLabel(value: string, now = Date.now()): string {
  const at = parsed(value)
  if (!at) return ""
  const today = new Date(now)
  const key = localDay(at)
  if (key === localDay(today)) return "Today"
  if (key === dayBefore(today)) return "Yesterday"
  return at.getFullYear() === today.getFullYear()
    ? dayThisYearFormat.format(at)
    : dateFormat.format(at)
}

/** A message group's time: the clock, since its day has a separator. */
export function clockTime(value: string): string {
  const at = parsed(value)
  return at ? clockFormat.format(at) : ""
}

/**
 * An inbox row's time, short: "now", "5m", "3h", "Yesterday", a weekday
 * this week, then the date.
 */
export function listTime(value: string, now = Date.now()): string {
  const at = parsed(value)
  if (!at) return ""
  const elapsed = now - at.getTime()
  if (elapsed < minute) return "now"
  if (elapsed < hour) return `${Math.floor(elapsed / minute)}m`
  if (elapsed < day) return `${Math.floor(elapsed / hour)}h`
  const today = new Date(now)
  if (localDay(at) === dayBefore(today)) return "Yesterday"
  if (elapsed < 7 * day) return weekdayFormat.format(at)
  return at.getFullYear() === today.getFullYear()
    ? dateThisYearFormat.format(at)
    : dateFormat.format(at)
}

/** A message's key across its confirmation: the page's id while it has one. */
export function messageKey(message: DmMessage): string {
  return message.clientId ?? message.id
}

export type DmDay = { kind: "day"; key: string; label: string }

export type DmGroup<TMessage extends DmMessage = DmMessage> = {
  kind: "group"
  key: string
  authorId: string
  mine: boolean
  /** When its first message was said. */
  at: string
  messages: TMessage[]
}

/**
 * Something in a log that stands on its own rather than in a person's
 * group: in a side conversation, an agent's message, a request's card or a
 * quiet line. One entry per kind of item, so `item` narrows by its `kind`.
 */
export type DmEntry<TItem> = TItem extends unknown
  ? { kind: "entry"; key: string; item: TItem }
  : never

export type DmTimelineItem = DmDay | DmGroup

/** What a log may hold beside people's messages. */
type Standalone = { id: string; createdAt: string; clientId?: string }

/**
 * The conversation as it reads: a separator as each day starts, and each
 * person's consecutive messages in the same minute as one group, with the
 * author and the time said once.
 */
export function dmTimeline(
  messages: readonly DmMessage[],
  viewerId: string,
  now = Date.now()
): DmTimelineItem[] {
  return conversationTimeline<DmMessage, never>(
    messages,
    viewerId,
    (_item): _item is DmMessage => true,
    now
  )
}

/**
 * A log of people's messages and other items, as it reads: a separator as
 * each day starts; each person's consecutive messages in the same minute as
 * one group, as a DM's; and each other item (`isMessage` false) on its own,
 * which ends the group before it.
 */
export function conversationTimeline<
  TMessage extends DmMessage,
  TOther extends Standalone,
>(
  items: ReadonlyArray<TMessage | TOther>,
  viewerId: string,
  isMessage: (item: TMessage | TOther) => item is TMessage,
  now = Date.now()
): Array<DmDay | DmGroup<TMessage> | DmEntry<TOther>> {
  const timeline: Array<DmDay | DmGroup<TMessage> | DmEntry<TOther>> = []
  let group: DmGroup<TMessage> | undefined
  let groupMinute: number | undefined
  let currentDay: string | undefined
  for (const item of items) {
    const at = parsed(item.createdAt)
    // A time that does not parse stays with what came before it.
    const dayOf = at ? localDay(at) : currentDay
    const minuteOf = at ? Math.floor(at.getTime() / minute) : groupMinute
    if (dayOf !== undefined && dayOf !== currentDay) {
      currentDay = dayOf
      timeline.push({
        kind: "day",
        key: `day:${dayOf}`,
        label: dayLabel(item.createdAt, now),
      })
      group = undefined
    }
    if (!isMessage(item)) {
      group = undefined
      timeline.push({
        kind: "entry",
        key: `entry:${item.clientId ?? item.id}`,
        item,
      } as DmEntry<TOther>)
      continue
    }
    if (group && group.authorId === item.authorId && groupMinute === minuteOf) {
      group.messages.push(item)
      continue
    }
    group = {
      kind: "group",
      key: `group:${messageKey(item)}`,
      authorId: item.authorId,
      mine: item.authorId === viewerId,
      at: item.createdAt,
      messages: [item],
    }
    groupMinute = minuteOf
    timeline.push(group)
  }
  return timeline
}

/**
 * Where "Seen" goes: the viewer's last message the other person has read,
 * unless they have said something since, which already shows they read it.
 */
export function seenMessageId(
  messages: readonly DmMessage[],
  viewerId: string,
  peerLastReadSeq: number
): string | undefined {
  for (let index = messages.length - 1; index >= 0; index -= 1) {
    const message = messages[index]
    // Not confirmed yet: nobody has read it.
    if (message.seq === 0) continue
    if (message.authorId !== viewerId) return undefined
    if (message.seq <= peerLastReadSeq) return message.id
  }
  return undefined
}

/** An inbox row's preview: its last message, "You: " first when it was the viewer's. */
export function conversationPreview(
  conversation: Conversation,
  viewerId: string
): string {
  const last = conversation.lastMessage
  if (!last) return "No messages yet"
  return last.authorId === viewerId ? `You: ${last.body}` : last.body
}

/** A count as a badge shows it: `cap+` past the cap. */
export function countLabel(count: number, cap: number): string {
  return count > cap ? `${cap}+` : String(count)
}

/** What a person is called in a greeting: the first word of their name, or all of an email address. */
export function firstName(name: string): string {
  const trimmed = name.trim()
  if (trimmed.includes("@")) return trimmed
  return trimmed.split(/\s+/)[0] || trimmed
}

const roleLabels: Record<Role, string> = {
  owner: "Owner",
  admin: "Admin",
  member: "Member",
}

export function roleLabel(role: Role): string {
  return roleLabels[role]
}
