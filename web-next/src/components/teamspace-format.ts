// Small presentation helpers shared by the Teamspaces list, its detail and
// the focused teamspace: who someone is (as an avatar identity) and when
// something happened (in words).
import type { AvatarIdentity } from "./workspace-avatar"
import { TEAMSPACE_UNREAD_CAP } from "@/lib/teamspaces/client"
import type { Teamspace, TeamspaceLastMessage } from "@/lib/teamspaces/client"

export function initialsOf(name: string): string {
  const letters = name
    .trim()
    .split(/\s+/)
    .slice(0, 2)
    .map((part) => part.charAt(0).toUpperCase())
    .join("")
  return letters || "?"
}

/** A person: Lorelei art, varied by their user id so it follows them everywhere. */
export function personIdentity(userId: string, name: string): AvatarIdentity {
  return { id: userId, name, initials: initialsOf(name), kind: "user" }
}

/** An agent: voxel art, seeded by the agent's id so it survives a rename. */
export function agentIdentity(agentId: string, name: string): AvatarIdentity {
  return { id: agentId, name, initials: initialsOf(name), kind: "agent" }
}

/** A teammate's name as the teamspace knows it, from their agent's seat. */
export function memberName(
  space: Teamspace,
  userId: string
): string | undefined {
  return space.whisperers.find((whisperer) => whisperer.user_id === userId)
    ?.user_display_name
}

export function statusLabel(space: Pick<Teamspace, "status">): string {
  return space.status === "active" ? "Active" : "Paused"
}

/**
 * Who paused a paused teamspace and when, as Go recorded it: "Paused by Ada
 * on 16 Oct 2026" ("by you" for the viewer), "Paused on 16 Oct 2026" when
 * nobody here knows the name, or nothing when Go recorded no date. A name
 * comes from the teamspace's seats, else from `nameOf`.
 */
export function pausedLine(
  space: Teamspace,
  {
    viewerId,
    nameOf,
  }: {
    viewerId?: string
    nameOf?: (userId: string) => string | undefined
  } = {}
): string | undefined {
  if (space.status === "active" || !space.paused_at) return undefined
  const at = Date.parse(space.paused_at)
  if (Number.isNaN(at)) return undefined
  const date = dateFormat.format(at)
  const by = space.paused_by_user_id
  const name = !by
    ? undefined
    : by === viewerId
      ? "you"
      : (memberName(space, by) ?? nameOf?.(by))
  return name ? `Paused by ${name} on ${date}` : `Paused on ${date}`
}

const minute = 60_000
const hour = 60 * minute
const day = 24 * hour

const dateFormat = new Intl.DateTimeFormat(undefined, {
  day: "numeric",
  month: "short",
  year: "numeric",
})
const weekdayFormat = new Intl.DateTimeFormat(undefined, { weekday: "long" })
const clockFormat = new Intl.DateTimeFormat(undefined, {
  hour: "numeric",
  minute: "2-digit",
})
const shortDayFormat = new Intl.DateTimeFormat(undefined, {
  weekday: "short",
})
const fullFormat = new Intl.DateTimeFormat(undefined, {
  dateStyle: "medium",
  timeStyle: "short",
})

/** How long ago, to follow a verb: "just now", "5 min ago", "yesterday", "Monday", "3 Sep 2026". */
export function timeAgo(value: string, now = Date.now()): string {
  const at = Date.parse(value)
  if (Number.isNaN(at)) return "at an unknown time"
  const elapsed = now - at
  if (elapsed < minute) return "just now"
  if (elapsed < hour) return `${Math.floor(elapsed / minute)} min ago`
  if (elapsed < day) return `${Math.floor(elapsed / hour)} hr ago`
  if (elapsed < 2 * day) return "yesterday"
  if (elapsed < 7 * day) return weekdayFormat.format(at)
  return dateFormat.format(at)
}

/** When a message was said: the time today, the day and time this week, else the date. */
export function messageTime(value: string, now = Date.now()): string {
  const at = Date.parse(value)
  if (Number.isNaN(at)) return ""
  const then = new Date(at)
  if (then.toDateString() === new Date(now).toDateString()) {
    return clockFormat.format(then)
  }
  if (now - at < 7 * day) {
    return `${shortDayFormat.format(then)} ${clockFormat.format(then)}`
  }
  return dateFormat.format(then)
}

export function fullTime(value: string): string {
  const at = Date.parse(value)
  return Number.isNaN(at) ? value : fullFormat.format(at)
}

/** The later of two timestamps, either possibly missing. */
export function latest(
  first: string | undefined,
  second: string | undefined
): string | undefined {
  if (!first) return second
  if (!second) return first
  return Date.parse(first) >= Date.parse(second) ? first : second
}

/** A `YYYY-MM-DD` day as local midnight, or undefined when it isn't one. */
export function parseDay(value: string | null | undefined): Date | undefined {
  const match = value ? /^(\d{4})-(\d{2})-(\d{2})$/.exec(value) : null
  if (!match) return undefined
  const [year, month, date] = match.slice(1).map(Number)
  const parsed = new Date(year, month - 1, date)
  return parsed.getFullYear() === year &&
    parsed.getMonth() === month - 1 &&
    parsed.getDate() === date
    ? parsed
    : undefined
}

const longDayFormat = new Intl.DateTimeFormat(undefined, {
  day: "numeric",
  month: "long",
})
const longDayYearFormat = new Intl.DateTimeFormat(undefined, {
  day: "numeric",
  month: "long",
  year: "numeric",
})
const shortDayMonthFormat = new Intl.DateTimeFormat(undefined, {
  day: "numeric",
  month: "short",
})
const shortDayYearFormat = new Intl.DateTimeFormat(undefined, {
  day: "numeric",
  month: "short",
  year: "numeric",
})

/** A day in words: "4 November" this year, "4 November 2027" another ("4 Nov" short). */
export function dayLabel(
  date: Date,
  { short = false, now = Date.now() }: { short?: boolean; now?: number } = {}
): string {
  const thisYear = date.getFullYear() === new Date(now).getFullYear()
  const format = short
    ? thisYear
      ? shortDayMonthFormat
      : shortDayYearFormat
    : thisYear
      ? longDayFormat
      : longDayYearFormat
  return format.format(date)
}

/** Whole days from today to `target` by the member's calendar (negative once past). */
export function daysUntil(target: Date, now = Date.now()): number {
  const today = new Date(now)
  const start = Date.UTC(today.getFullYear(), today.getMonth(), today.getDate())
  const end = Date.UTC(
    target.getFullYear(),
    target.getMonth(),
    target.getDate()
  )
  return Math.round((end - start) / day)
}

/** "today", "tomorrow", "in 21 days", "yesterday", "3 days ago". */
export function daysAway(days: number): string {
  if (days === 0) return "today"
  if (days === 1) return "tomorrow"
  if (days === -1) return "yesterday"
  return days > 0 ? `in ${days} days` : `${-days} days ago`
}

/** What the teamspace is working towards, when it has a date: its label, else "Target". */
export function targetName(space: Pick<Teamspace, "target_label">): string {
  return space.target_label?.trim() || "Target"
}

/** "Launch on 4 November · in 21 days", or nothing without a target date. */
export function targetLine(
  space: Pick<Teamspace, "target_date" | "target_label">,
  now = Date.now()
): string | undefined {
  const date = parseDay(space.target_date)
  if (!date) return undefined
  return `${targetName(space)} on ${dayLabel(date, { now })} · ${daysAway(daysUntil(date, now))}`
}

/** "Launch 4 Nov", the target as a card's fact chip, or nothing without a date. */
export function targetChip(
  space: Pick<Teamspace, "target_date" | "target_label">,
  now = Date.now()
): string | undefined {
  const date = parseDay(space.target_date)
  if (!date) return undefined
  return `${targetName(space)} ${dayLabel(date, { short: true, now })}`
}

/** How many room messages wait on the member; 0 when the server doesn't say. */
export function unreadCount(space: Pick<Teamspace, "unread_count">): number {
  const count = space.unread_count
  return typeof count === "number" && count > 0 ? count : 0
}

/** An unread count as a badge shows it: "99+" once it reaches the server's cap. */
export function unreadLabel(count: number): string {
  return count >= TEAMSPACE_UNREAD_CAP
    ? `${TEAMSPACE_UNREAD_CAP - 1}+`
    : String(count)
}

/** An unread count in words, for a screen reader: "3 unread", "99+ unread". */
export function unreadWords(count: number): string {
  return `${unreadLabel(count)} unread`
}

/**
 * The room's newest message on one line: "Bo: the pricing sheet is done",
 * "You: …" for the member's own, and "Parley notes" for a parley's notes.
 */
export function lastMessageLine(
  message: TeamspaceLastMessage,
  viewerId?: string
): string {
  if (message.kind === "parley_notes") return "Parley notes"
  const author =
    message.author_kind === "person" &&
    viewerId &&
    message.author_id === viewerId
      ? "You"
      : message.author_name?.trim() || message.author_handle
  const preview = message.preview.trim()
  if (!author) return preview
  return preview ? `${author}: ${preview}` : author
}
