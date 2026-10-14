// What Today reads out of a member's threads: the briefs their agent wrote
// when their apps reported something (the wake-up thread), and the files it
// saved in their recent conversations.
import { buildTimeline } from "@/lib/threads/timeline"
import type { MessageEntry } from "@/lib/threads/timeline"
import type { ThreadDetail } from "@/lib/threads/client"

export type Brief = {
  key: string
  /** The first sentence of what the agent told the member. */
  text: string
  at?: string
  /** The toolkits whose events the agent was reporting, in the order the batch named them. */
  toolkits: string[]
}

export type AgentFile = {
  key: string
  name: string
  threadId: string
  at?: string
}

/** The toolkits a wake-up's batch names: its events are "1. github · trigger · time" lines (wakeups.Message). */
export function batchToolkits(text: string): string[] {
  const found: string[] = []
  for (const match of text.matchAll(/^\d+\. ([^·\n]+?) · /gm)) {
    const toolkit = match[1].trim().toLowerCase()
    if (toolkit && toolkit !== "an app" && !found.includes(toolkit))
      found.push(toolkit)
  }
  return found
}

/** A message's first sentence as plain text: Markdown marks dropped, at most `max` characters. */
export function firstSentence(text: string, max = 140): string {
  const plain = text
    .replace(/```[\s\S]*?```/g, " ")
    .replace(/!?\[([^\]]*)\]\([^)]*\)/g, "$1")
    .replace(/^\s{0,3}(#{1,6}|[-*+]|\d+\.)\s+/gm, "")
    .replace(/[*_`>~]/g, "")
    .replace(/\s+/g, " ")
    .trim()
  const end = plain.search(/[.!?](\s|$)/)
  const sentence = end === -1 ? plain : plain.slice(0, end + 1)
  return sentence.length > max
    ? `${sentence.slice(0, max - 1).trimEnd()}…`
    : sentence
}

/**
 * The agent's briefs in the wake-up thread, newest first: each agent message
 * that answers a batch from the member's apps, with the apps that batch came
 * from. A message that merely continues a conversation the member started
 * there is not a brief.
 */
export function briefsOf(detail: ThreadDetail, max = 4): Brief[] {
  const briefs: Brief[] = []
  let batch: string[] | null = null
  for (const entry of buildTimeline(detail.items)) {
    if (entry.kind !== "message") continue
    const message: MessageEntry = entry
    if (message.role === "apps") {
      batch = batchToolkits(message.text)
    } else if (message.role === "member") {
      batch = null
    } else if (batch && message.text.trim()) {
      briefs.push({
        key: message.key,
        text: firstSentence(message.text),
        at: message.at,
        toolkits: batch,
      })
      batch = null
    }
  }
  return briefs.reverse().slice(0, max)
}

/** The files the agent saved in these conversations, newest first, each once (its latest version). */
export function agentFilesOf(
  details: ReadonlyArray<ThreadDetail | undefined>,
  max = 4
): AgentFile[] {
  const files = new Map<string, AgentFile>()
  for (const detail of details) {
    if (!detail) continue
    for (const entry of buildTimeline(detail.items)) {
      if (entry.kind !== "artifact") continue
      const key = `${detail.id}/${entry.name}`
      const known = files.get(key)
      if (!known || (entry.at ?? "") >= (known.at ?? "")) {
        files.set(key, {
          key,
          name: entry.name,
          threadId: detail.id,
          at: entry.at,
        })
      }
    }
  }
  return [...files.values()]
    .sort((left, right) => (right.at ?? "").localeCompare(left.at ?? ""))
    .slice(0, max)
}

/** "12 min ago", "3 h ago", "Yesterday", then a date: how long ago, for a quiet line. */
export function agoPhrase(iso: string | undefined, now: number): string {
  const time = iso ? Date.parse(iso) : NaN
  if (!Number.isFinite(time)) return ""
  const minutes = Math.max(0, Math.round((now - time) / 60_000))
  if (minutes < 1) return "Just now"
  if (minutes < 60) return `${minutes} min ago`
  const hours = Math.round(minutes / 60)
  if (hours < 24) return `${hours} h ago`
  if (hours < 48) return "Yesterday"
  return new Intl.DateTimeFormat(undefined, {
    month: "short",
    day: "numeric",
  }).format(time)
}

const weekdays: Partial<Record<string, string>> = {
  MON: "Mondays",
  TUE: "Tuesdays",
  WED: "Wednesdays",
  THU: "Thursdays",
  FRI: "Fridays",
  SAT: "Saturdays",
  SUN: "Sundays",
}

/** A schedule as a member reads it: "Every 1 day", "Weekdays at 09:00 UTC", or the expression itself. */
export function scheduleWords(expression: string, timezone: string): string {
  const rate = /^rate\((\d+)\s+(minute|minutes|hour|hours|day|days)\)$/.exec(
    expression.trim()
  )
  if (rate) {
    const count = Number(rate[1])
    const unit = rate[2].replace(/s$/, "")
    return count === 1 ? `Every ${unit}` : `Every ${count} ${unit}s`
  }
  const cron = /^cron\((\S+) (\S+) \S+ \S+ (\S+) \S+\)$/.exec(expression.trim())
  if (cron && /^\d+$/.test(cron[1]) && /^\d+$/.test(cron[2])) {
    const time = `${cron[2].padStart(2, "0")}:${cron[1].padStart(2, "0")} ${timezone}`
    if (cron[3] === "MON-FRI") return `Weekdays at ${time}`
    if (cron[3] === "*" || cron[3] === "?") return `Daily at ${time}`
    const days = cron[3].split(",").map((day) => weekdays[day.toUpperCase()])
    if (days.every(Boolean)) return `${days.join(" and ")} at ${time}`
    return `${cron[3]} at ${time}`
  }
  const at = /^at\((.+)\)$/.exec(expression.trim())
  if (at) return `Once, ${at[1].replace("T", " ")} ${timezone}`
  return expression
}
