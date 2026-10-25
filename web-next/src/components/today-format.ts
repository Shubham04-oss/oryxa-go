// What Today reads out of a member's threads: the briefs their agent wrote
// when their apps reported something (the wake-up thread), and the files it
// saved in their recent conversations.
import { buildTimeline } from "@/lib/threads/timeline"
import type { MessageEntry } from "@/lib/threads/timeline"
import type { Thread, ThreadDetail } from "@/lib/threads/client"

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

/**
 * The files the agent saved, newest first, from the chat list alone: each
 * chat's latest file, as its runs left it in the list (#388). No chat's
 * history is read.
 */
export function agentFilesFromList(
  threads: ReadonlyArray<
    Pick<Thread, "id" | "updated_at" | "origin" | "latest_artifact">
  >,
  max = 4
): AgentFile[] {
  return threads
    .filter(
      (thread) =>
        (!thread.origin || thread.origin === "chat") && thread.latest_artifact
    )
    .sort((left, right) => right.updated_at.localeCompare(left.updated_at))
    .slice(0, max)
    .map((thread) => {
      const name = thread.latest_artifact?.name ?? ""
      return {
        key: `${thread.id}/${name}`,
        name,
        threadId: thread.id,
        at: thread.updated_at,
      }
    })
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

/** "Good morning, Ada": by the member's own clock. */
export function greeting(now: number, name?: string): string {
  const hour = new Date(now).getHours()
  const part =
    hour < 5
      ? "evening"
      : hour < 12
        ? "morning"
        : hour < 18
          ? "afternoon"
          : "evening"
  // An email address is not a name to greet someone by.
  const first = name?.includes("@") ? undefined : name?.trim().split(/\s+/)[0]
  return first ? `Good ${part}, ${first}` : `Good ${part}`
}

/** Whether two instants fall on the same day by the member's own clock. */
function sameDay(left: number, right: number): boolean {
  const a = new Date(left)
  const b = new Date(right)
  return (
    a.getFullYear() === b.getFullYear() &&
    a.getMonth() === b.getMonth() &&
    a.getDate() === b.getDate()
  )
}

/**
 * How many asks put to this member their agent decided today, by the
 * decision the server recorded (`decided_by.kind === "agent"`).
 */
export function handledToday(
  requests: ReadonlyArray<{
    target: { user_id: string }
    decision?: { decided_at: string; decided_by?: { kind: string } }
  }>,
  viewerId: string,
  now: number
): number {
  return requests.filter((request) => {
    const decision = request.decision
    if (request.target.user_id !== viewerId || !decision) return false
    if (decision.decided_by?.kind !== "agent") return false
    const at = Date.parse(decision.decided_at)
    return Number.isFinite(at) && sameDay(at, now)
  }).length
}

/**
 * The line under the greeting: what needs the member, then what their agent
 * handled today. Empty when there is nothing true to say.
 */
export function helloLine(needs: number, handled: number, agent = "Krude") {
  const waiting =
    needs > 0
      ? `${needs} ${needs === 1 ? "thing needs" : "things need"} you.`
      : ""
  const done =
    handled > 0
      ? needs > 0
        ? `${agent} handled ${handled} more today.`
        : `${agent} handled ${handled} ${handled === 1 ? "ask" : "asks"} for you today.`
      : ""
  return [waiting, done].filter(Boolean).join(" ")
}

export type PickupItem = {
  key: string
  kind: "file" | "chat" | "knowledge"
  title: string
  /** The quiet line: where it lives and how long ago. */
  detail: string
  at?: string
  /** The chat a file or a conversation opens. */
  threadId?: string
  running?: boolean
}

/**
 * "Pick up where you left off": the member's most recent work across the
 * files their agent saved, their titled conversations and their knowledge,
 * newest first. A running conversation leads.
 */
export function pickupItems(
  {
    files,
    chats,
    knowledge,
    runningThreadId,
    starting = false,
  }: {
    files: ReadonlyArray<AgentFile & { chatTitle?: string }>
    chats: ReadonlyArray<{ id: string; title?: string; updated_at: string }>
    knowledge: ReadonlyArray<{
      id: string
      name: string
      document_count: number
      updated_at: string
    }>
    runningThreadId?: string | null
    /** A new chat is running and has no id yet: it leads, and opens nothing. */
    starting?: boolean
  },
  now: number,
  max = 4
): PickupItem[] {
  const items: PickupItem[] = [
    ...(starting
      ? [
          {
            key: "chat:starting",
            kind: "chat" as const,
            title: "Starting conversation",
            detail: "Working",
            running: true,
          },
        ]
      : []),
    ...files.map((file) => ({
      key: `file:${file.key}`,
      kind: "file" as const,
      title: file.name,
      detail: [file.chatTitle, agoPhrase(file.at, now)]
        .filter(Boolean)
        .join(" · "),
      at: file.at,
      threadId: file.threadId,
      running: false,
    })),
    ...chats
      .filter((chat) => (chat.title ?? "").trim().length > 0)
      .map((chat) => {
        const running = chat.id === runningThreadId
        return {
          key: `chat:${chat.id}`,
          kind: "chat" as const,
          title: (chat.title ?? "").trim(),
          detail: running
            ? "Working"
            : `Chat · ${agoPhrase(chat.updated_at, now)}`,
          at: chat.updated_at,
          threadId: chat.id,
          running,
        }
      }),
    ...knowledge.map((base) => ({
      key: `knowledge:${base.id}`,
      kind: "knowledge" as const,
      title: base.name,
      detail: `Library · ${base.document_count} document${base.document_count === 1 ? "" : "s"}`,
      at: base.updated_at,
      running: false,
    })),
  ]
  return items
    .sort((left, right) => {
      if (left.running !== right.running) return left.running ? -1 : 1
      return (right.at ?? "").localeCompare(left.at ?? "")
    })
    .slice(0, max)
}
