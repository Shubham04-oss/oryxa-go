import { useSyncExternalStore } from "react"
import type { Thread } from "./client"

// Which conversation the primary agent's chat opens, and how the rest of the
// app (Home, for one) asks it to open a particular conversation or a new one.
//
// A conversation reopens only while it's fresh. The member's own choice in
// this browser session comes first; then the latest conversation, if it was
// active within the last hour. Anything older opens a new chat, so a
// conversation stopped yesterday stays in the past. A conversation still
// waiting on the member's confirmation reopens too, but the chat checks that
// itself, since the thread list doesn't say.
//
// The chat also says what it is doing (`publishPrimaryChatActivity`), so a
// page outside the panel can show that the agent is working or waiting.

/** Whose chat a choice or a request belongs to. */
export type ChatScope = { organizationId: string; userId: string }

/** What to open: one conversation, or a new one, sending `message` first. */
export type PrimaryChatTarget =
  { kind: "thread"; threadId: string } | { kind: "new"; message?: string }

/** A request to open a conversation. `id` grows with every request. */
export type PrimaryChatRequest = PrimaryChatTarget & {
  id: number
  scope: ChatScope
}

/** How long after its last activity a conversation still reopens on its own. */
export const FRESH_CONVERSATION_MS = 60 * 60 * 1000

const NEW_CHAT = "new"

/** Whether two scopes are the same member's, in the same organization. */
export function sameChatScope(a: ChatScope, b: ChatScope): boolean {
  return a.organizationId === b.organizationId && a.userId === b.userId
}

function choiceKey(scope: ChatScope, agentId: string) {
  return `oryxa.chat.${scope.organizationId}.${scope.userId}.${agentId}`
}

/**
 * The member's choice for this agent in this browser session: a thread's id,
 * `"new"` for a new chat, or null when they haven't chosen. It survives a
 * reload and ends with the tab.
 */
export function rememberedChoice(
  scope: ChatScope,
  agentId: string
): string | null {
  try {
    return window.sessionStorage.getItem(choiceKey(scope, agentId))
  } catch {
    return null
  }
}

/** Records the member's choice: a thread's id, or null for a new chat. */
export function rememberChoice(
  scope: ChatScope,
  agentId: string,
  threadId: string | null
) {
  try {
    window.sessionStorage.setItem(
      choiceKey(scope, agentId),
      threadId ?? NEW_CHAT
    )
  } catch {
    // Storage can be off or full. The chat still opens by the rules below.
  }
}

type ListedThread = Pick<Thread, "id" | "title" | "updated_at" | "origin">

/**
 * The newest conversation the member had with the agent: not an update from
 * their apps, which the chat never opens, and not one with no title, which
 * no message ever went into (Go titles a thread from its first message): a
 * start that was abandoned would otherwise count as the latest.
 */
function latestChatThread(
  threads: readonly ListedThread[]
): ListedThread | undefined {
  return threads.find(
    (thread) =>
      (!thread.origin || thread.origin === "chat") && thread.title.trim() !== ""
  )
}

/**
 * The conversation to open, or null for a new chat. `threads` is the list
 * Go answers, newest first; `choice` is `rememberedChoice`.
 */
export function chooseThread(
  threads: readonly ListedThread[],
  choice: string | null,
  now: number
): string | null {
  if (choice === NEW_CHAT) return null
  if (choice && threads.some((thread) => thread.id === choice)) return choice
  const latest = latestChatThread(threads)
  if (!latest) return null
  const updated = Date.parse(latest.updated_at)
  return Number.isFinite(updated) && now - updated < FRESH_CONVERSATION_MS
    ? latest.id
    : null
}

/**
 * What the chat opens: `chooseThread`'s answer, except that an old latest
 * conversation still opens while it waits on the member's confirmation. The
 * list can't say that, so `waiting` asks (Go's detail of the thread), and only
 * when neither the member's choice nor freshness decided. A check that fails
 * counts as not waiting, and the chat opens new.
 */
export async function openingThread({
  threads,
  choice,
  now,
  waiting,
}: {
  threads: readonly ListedThread[]
  choice: string | null
  now: number
  waiting: (threadId: string) => Promise<boolean>
}): Promise<string | null> {
  const chosen = chooseThread(threads, choice, now)
  if (chosen !== null || choice === NEW_CHAT) return chosen
  const latest = latestChatThread(threads)
  if (!latest) return null
  const held = await waiting(latest.id).catch(() => false)
  return held ? latest.id : null
}

let latestRequest: PrimaryChatRequest | null = null
let nextRequestId = 1
let handledRequestId = 0
const listeners = new Set<() => void>()

/**
 * Asks the agent panel's primary chat to open `target`. The caller opens the
 * panel itself; the chat switches to the conversation, or to a new one and
 * sends `message` as its first message, and remembers the choice.
 */
export function requestPrimaryChat(
  scope: ChatScope,
  target: PrimaryChatTarget
) {
  latestRequest = { ...target, scope, id: nextRequestId++ }
  for (const listener of listeners) listener()
}

/**
 * Claims a request for whoever acts on it: true the first time for each `id`,
 * false after. The panel calls it for the request it reads, so however often
 * it renders, or the page mounts it again, an old request is never opened
 * twice; and a request made before the panel mounted is still there to claim.
 */
export function claimPrimaryChatRequest(id: number): boolean {
  if (id <= handledRequestId) return false
  handledRequestId = id
  return true
}

function subscribe(listener: () => void) {
  listeners.add(listener)
  return () => {
    listeners.delete(listener)
  }
}

/** The latest request, for the chat to act on once per `id`. */
export function usePrimaryChatRequest(): PrimaryChatRequest | null {
  return useSyncExternalStore(
    subscribe,
    () => latestRequest,
    () => null
  )
}

/** What the rest of the app may see of the primary chat: which conversation it has open, and whether it works or waits. */
export type PrimaryChatActivity = {
  /** The conversation the primary chat has open; null for a new, empty chat. */
  threadId: string | null
  /** "running" while a run streams or is being submitted; "waiting" while a confirmation waits on the member; else "idle". */
  state: "idle" | "running" | "waiting"
}

let latestActivity: PrimaryChatActivity | null = null
// Whose chat published it: a reader in another organization, or signed in as
// someone else, hears nothing.
let latestActivityScope: ChatScope | null = null
const activityListeners = new Set<() => void>()

function sameScope(a: ChatScope | null, b: ChatScope | null) {
  return (
    a !== null &&
    b !== null &&
    a.organizationId === b.organizationId &&
    a.userId === b.userId
  )
}

function sameActivity(
  a: PrimaryChatActivity | null,
  b: PrimaryChatActivity | null
) {
  if (a === null || b === null) return a === b
  return a.threadId === b.threadId && a.state === b.state
}

/**
 * The primary chat says what it is doing: the conversation it has open and
 * whether it works, waits on the member, or is idle, for `scope`, the
 * organization and member it belongs to. `null` says it is gone (it
 * unmounted). A chat without a scope is heard by no one. Only a change is
 * passed on, so a chat may publish on every render without waking whoever
 * listens.
 */
export function publishPrimaryChatActivity(
  activity: PrimaryChatActivity | null,
  scope?: ChatScope
) {
  const nextScope = activity && scope ? scope : null
  if (
    sameActivity(activity, latestActivity) &&
    (nextScope === null
      ? latestActivityScope === null
      : sameScope(nextScope, latestActivityScope))
  )
    return
  latestActivity = activity && {
    threadId: activity.threadId,
    state: activity.state,
  }
  latestActivityScope = nextScope && {
    organizationId: nextScope.organizationId,
    userId: nextScope.userId,
  }
  for (const listener of activityListeners) listener()
}

function subscribeToActivity(listener: () => void) {
  activityListeners.add(listener)
  return () => {
    activityListeners.delete(listener)
  }
}

/**
 * What the primary chat last published for `scope`: null until it has
 * mounted in this page, once it is gone, and whenever the chat belongs to
 * another organization or member.
 */
export function usePrimaryChatActivity(
  scope: ChatScope
): PrimaryChatActivity | null {
  return useSyncExternalStore(
    subscribeToActivity,
    () => (sameScope(latestActivityScope, scope) ? latestActivity : null),
    () => null
  )
}
