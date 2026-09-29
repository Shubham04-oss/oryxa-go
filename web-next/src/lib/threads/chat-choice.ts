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

/** Whose chat a choice or a request belongs to. */
export type ChatScope = { organizationId: string; userId: string }

/** What to open: one conversation, or a new one, sending `message` first. */
export type PrimaryChatTarget =
  | { kind: "thread"; threadId: string }
  | { kind: "new"; message?: string }

/** A request to open a conversation. `id` grows with every request. */
export type PrimaryChatRequest = PrimaryChatTarget & {
  id: number
  scope: ChatScope
}

/** How long after its last activity a conversation still reopens on its own. */
export const FRESH_CONVERSATION_MS = 60 * 60 * 1000

const NEW_CHAT = "new"

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
    window.sessionStorage.setItem(choiceKey(scope, agentId), threadId ?? NEW_CHAT)
  } catch {
    // Storage can be off or full. The chat still opens by the rules below.
  }
}

/**
 * The conversation to open, or null for a new chat. `threads` is the list
 * Go answers, newest first; `choice` is `rememberedChoice`.
 */
export function chooseThread(
  threads: readonly Pick<Thread, "id" | "updated_at" | "origin">[],
  choice: string | null,
  now: number
): string | null {
  if (choice === NEW_CHAT) return null
  if (choice && threads.some((thread) => thread.id === choice)) return choice
  const latest = threads.find(
    (thread) => !thread.origin || thread.origin === "chat"
  )
  if (!latest) return null
  const updated = Date.parse(latest.updated_at)
  return Number.isFinite(updated) && now - updated < FRESH_CONVERSATION_MS
    ? latest.id
    : null
}

let latestRequest: PrimaryChatRequest | null = null
let nextRequestId = 1
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
