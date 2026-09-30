import { useEffect, useSyncExternalStore } from "react"
import type { Thread } from "./client"
import type { ScreenContext } from "@/lib/agents/screen-context"

// Which conversation the primary agent's chat opens, and how the rest of the
// app (Home, for one) asks it to open a particular conversation or a new one.
//
// A mounted chat stays where the member left it. On remount or reload,
// their last view in this tab reopens for five minutes (including New chat),
// then the newest conversation waiting on their approval, then New chat.
// Server update times never imply the member was recently present.
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
  /** A message the chat was already holding too many of to take: it is not to be sent, and the chat shows it as not sent. */
  refused?: true
}

/** How long after its last activity a conversation still reopens on its own. */
export const FRESH_CONVERSATION_MS = 5 * 60 * 1000

const NEW_CHAT = "new"

/** Whether two scopes are the same member's, in the same organization. */
export function sameChatScope(a: ChatScope, b: ChatScope): boolean {
  return a.organizationId === b.organizationId && a.userId === b.userId
}

function choiceKey(scope: ChatScope, agentId: string) {
  return `oryxa.chat.${scope.organizationId}.${scope.userId}.${agentId}`
}

/** The last conversation and member-presence time in this tab, scoped by owner. */
export function rememberedView(
  scope: ChatScope,
  agentId: string
): { choice: string; leftAt: number } | null {
  try {
    const value: unknown = JSON.parse(
      window.sessionStorage.getItem(choiceKey(scope, agentId)) ?? "null"
    )
    if (typeof value !== "object" || value === null) return null
    const { choice, leftAt } = value as { choice?: unknown; leftAt?: unknown }
    return typeof choice === "string" &&
      typeof leftAt === "number" &&
      Number.isFinite(leftAt)
      ? { choice, leftAt }
      : null
  } catch {
    return null
  }
}

export function rememberedChoice(
  scope: ChatScope,
  agentId: string
): string | null {
  return rememberedView(scope, agentId)?.choice ?? null
}

/** Records the current view at a real member activity/leave, including automatic choices. */
export function rememberChoice(
  scope: ChatScope,
  agentId: string,
  threadId: string | null,
  leftAt = Date.now()
) {
  try {
    window.sessionStorage.setItem(
      choiceKey(scope, agentId),
      JSON.stringify({ choice: threadId ?? NEW_CHAT, leftAt })
    )
  } catch {
    /* Storage may be unavailable; the chat still works. */
  }
}

type ListedThread = Pick<Thread, "id" | "title" | "updated_at" | "origin">

export function chatHistory(threads: readonly ListedThread[]): ListedThread[] {
  return threads
    .filter((thread) => !thread.origin || thread.origin === "chat")
    .sort((a, b) => Date.parse(b.updated_at) - Date.parse(a.updated_at))
}

/** Only a last-viewed choice strictly younger than five minutes can reopen. */
export function chooseThread(
  threads: readonly ListedThread[],
  choice: string | null,
  now: number,
  leftAt?: number
): string | null {
  if (
    !choice ||
    leftAt === undefined ||
    leftAt > now ||
    now - leftAt >= FRESH_CONVERSATION_MS
  )
    return null
  return choice !== NEW_CHAT &&
    chatHistory(threads).some((thread) => thread.id === choice)
    ? choice
    : null
}

/** A restored explicit New stays explicit while the member continues viewing it. */
export function recentNewView(
  view: { choice: string | null; leftAt?: number } | null | undefined,
  now = Date.now()
): boolean {
  return (
    view?.choice === NEW_CHAT &&
    view.leftAt !== undefined &&
    view.leftAt <= now &&
    now - view.leftAt < FRESH_CONVERSATION_MS
  )
}

/** Recent view (including explicit New), otherwise newest pending approval, otherwise New. */
export async function openingThread({
  threads,
  choice,
  now,
  leftAt,
  waiting,
}: {
  threads: readonly ListedThread[]
  choice: string | null
  now: number
  leftAt?: number
  waiting: (threadId: string) => Promise<boolean>
}): Promise<string | null> {
  const chosen = chooseThread(threads, choice, now, leftAt)
  if (chosen || recentNewView({ choice, leftAt }, now)) return chosen
  const history = chatHistory(threads)
  const held = await Promise.all(
    history.map((thread) => waiting(thread.id).catch(() => false))
  )
  return history.find((_, index) => held[index])?.id ?? null
}

/** Record leaves and activity only while this view is visible; hidden panels never extend the grace. */
export function useRememberedView(
  thread: string | null | undefined,
  scope: ChatScope | undefined,
  agentId: string | undefined,
  visible = true,
  rememberNew = true
) {
  const organizationId = scope?.organizationId
  const userId = scope?.userId
  useEffect(() => {
    if (
      thread === undefined ||
      (thread === null && !rememberNew) ||
      !organizationId ||
      !userId ||
      !agentId ||
      !visible
    )
      return
    const save = () =>
      rememberChoice({ organizationId, userId }, agentId, thread)
    let present = document.visibilityState !== "hidden"
    const leave = () => {
      if (present) save()
      present = false
    }
    const activity = () => {
      if (present && document.visibilityState !== "hidden") save()
    }
    const visibility = () => {
      if (document.visibilityState === "hidden") leave()
      else {
        present = true
        save()
      }
    }
    activity()
    window.addEventListener("pagehide", leave)
    document.addEventListener("visibilitychange", visibility)
    for (const type of ["pointerdown", "keydown", "wheel"])
      document.addEventListener(type, activity, { passive: true })
    return () => {
      leave()
      window.removeEventListener("pagehide", leave)
      document.removeEventListener("visibilitychange", visibility)
      for (const type of ["pointerdown", "keydown", "wheel"])
        document.removeEventListener(type, activity)
    }
  }, [thread, organizationId, userId, agentId, visible, rememberNew])
}

/**
 * How many requests carrying a message may wait for the chat to take them.
 * The chat takes them as soon as it can, so only one that never reads them (no
 * panel, an agents list that never answers) fills this; the request after that
 * is refused, and no older one is pushed out to make room.
 */
export const MAX_WAITING_MESSAGES = 20

/** The words a request starts a new conversation with, trimmed; none for one that only opens a conversation. */
export function requestMessage(target: PrimaryChatTarget): string {
  return target.kind === "new" ? (target.message?.trim() ?? "") : ""
}

// The requests nobody has claimed yet, oldest first.
let waiting: readonly PrimaryChatRequest[] = []
let nextRequestId = 1
const listeners = new Set<() => void>()

function announce() {
  for (const listener of listeners) listener()
}

/**
 * Asks the agent panel's primary chat to open `target`. The caller opens the
 * panel itself; the chat switches to the conversation, or to a new one and
 * sends `message` as its first message, and remembers the choice.
 *
 * Each request waits until the chat claims it, however many are made before
 * it does (the panel takes none until the member's agents have loaded), and
 * the chat gets them in the order they were made. A message past
 * `MAX_WAITING_MESSAGES` is refused: it still waits its turn, marked `refused`
 * so the chat shows it as not sent instead of sending it.
 */
export function requestPrimaryChat(
  scope: ChatScope,
  target: PrimaryChatTarget
) {
  const request: PrimaryChatRequest = { ...target, scope, id: nextRequestId++ }
  const held = waiting.filter(
    (other) =>
      !other.refused &&
      requestMessage(other) !== "" &&
      sameChatScope(other.scope, scope)
  ).length
  const refused = requestMessage(target) !== "" && held >= MAX_WAITING_MESSAGES
  waiting = [...waiting, refused ? { ...request, refused: true } : request]
  announce()
}

/**
 * Claims a request for whoever acts on it: true the first time for each `id`,
 * false after. A claimed request is gone from the queue, so however often the
 * panel renders, or the page mounts it again, a request is never acted on
 * twice; and one made before the panel mounted is still there to claim.
 */
export function claimPrimaryChatRequest(id: number): boolean {
  if (!waiting.some((request) => request.id === id)) return false
  waiting = waiting.filter((request) => request.id !== id)
  announce()
  return true
}

function subscribe(listener: () => void) {
  listeners.add(listener)
  return () => {
    listeners.delete(listener)
  }
}

/**
 * The oldest request nobody has claimed, for the chat to act on once per `id`:
 * claim it and the next one is read. With a `scope`, only that member's in that
 * organization, so a request for someone else never holds theirs up.
 */
export function usePrimaryChatRequest(
  scope?: ChatScope
): PrimaryChatRequest | null {
  return useSyncExternalStore(
    subscribe,
    () =>
      waiting.find(
        (request) => !scope || sameChatScope(request.scope, scope)
      ) ?? null,
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

// What the agent panel holds for the primary chat, in one reducer: the
// conversation it is on, the requests and first messages waiting their turn,
// the first messages that could not be sent, and every agent's draft. Keeping
// it in one place is what lets every step be named by the request it belongs
// to, so a slow step of an old request can't undo a newer one and no message
// is dropped: each first message is made a conversation of its own, and sent
// into it, in order, or kept, in view, until the member deals with it.

/** Where the primary chat's unsent message is kept, among each agent's (the others by the specialist's id). */
export const PRIMARY_DRAFT = "primary"

/**
 * A message to start a new conversation with, on its way to the primary
 * agent. `id` names the request or send it came from, and every step that
 * follows names it too.
 */
export type FirstMessage = {
  id: number
  kind: "send"
  text: string
  /** What the member was looking at when they sent it. */
  screen?: ScreenContext
  /** The conversation made for it, once it exists. */
  threadId?: string
  /** A chat is making that conversation now. */
  making?: boolean
  /** Its conversation's chat has taken the message to send. */
  taken?: boolean
}

/** One thing the chat was asked to do: open a conversation, start a blank one, or start one with a first message. */
export type ChatTask =
  | { id: number; kind: "open"; threadId: string }
  | { id: number; kind: "blank" }
  | FirstMessage

/**
 * A first message that was not sent. It belongs to no conversation, so the
 * chat keeps it in view whichever one is open, until the member sends it
 * again, moves it to the composer or lets it go.
 */
export type UnsentMessage = {
  /** The request or send it came from. */
  id: number
  text: string
  /** What the member was looking at when they sent it. */
  screen?: ScreenContext
  /** Why it was not sent. */
  notice: string
}

export type PrimaryChat = {
  /** The conversation the chat is on: undefined until it is decided, null for a new one. */
  thread: string | null | undefined
  /** Explicit member/request choice, including a recently restored explicit New. Automatic existing chats are recorded too. */
  chosen: boolean
  /** What waits, oldest first. An open or a blank chat is done at once; a first message stays until its conversation has it. */
  tasks: ChatTask[]
  /** Every agent's unsent message, by `PRIMARY_DRAFT` or the specialist's id. */
  drafts: Partial<Record<string, string>>
  /** Why the chat left a conversation, for the blank chat to say. */
  notice: string
  /** The first messages that were not sent, oldest first. */
  unsent: UnsentMessage[]
  /** Counts each request and send: something the member just did here. */
  touched: number
  /** The last id given to a send from the composer (requests have their own ids, which are positive). */
  own: number
}

export type PrimaryChatAction =
  /** The app asked for a conversation (see `requestPrimaryChat`). */
  | { type: "request"; request: PrimaryChatRequest; screen?: ScreenContext }
  /** The app asked with a message the chat had no room for (`refused`): it is kept as not sent, for `notice`. */
  | {
      type: "refused"
      request: PrimaryChatRequest
      notice: string
      screen?: ScreenContext
    }
  /** The member sent the first message of a new chat from the composer. */
  | { type: "submit"; text: string; screen?: ScreenContext }
  /** The rules chose the conversation to open. Only fills in a chat still undecided and idle. */
  | { type: "decide"; thread: string | null; resumeNew?: boolean }
  /** The member (or a conversation that is gone) moved the chat. Waits while a first message is on its way. */
  | { type: "move"; thread: string | null; notice?: string }
  | { type: "making"; id: number }
  | { type: "made"; id: number; threadId: string }
  | { type: "taken"; id: number }
  | { type: "sent"; id: number }
  /** No conversation could be made for a first message (or the one made for it is gone): it is kept as not sent, for `notice`. */
  | { type: "failed"; id: number; notice: string }
  /** The member sends a message that was not sent once more, into a conversation of its own. */
  | { type: "retry"; id: number }
  /** The member puts a message that was not sent in the composer to change it: only while the composer is empty. */
  | { type: "edit"; id: number }
  /** The member lets a message that was not sent go. */
  | { type: "dismiss"; id: number }
  | { type: "draft"; key: string; text: string }
  /** Another member has the page: nothing held is theirs. */
  | { type: "reset" }

export function initialPrimaryChat(): PrimaryChat {
  return {
    thread: undefined,
    chosen: false,
    tasks: [],
    drafts: {},
    notice: "",
    unsent: [],
    touched: 0,
    own: 0,
  }
}

/** Applies the tasks at the front that need no conversation made. */
function settle(state: PrimaryChat): PrimaryChat {
  let { thread, tasks } = state
  while (tasks.length > 0 && tasks[0].kind !== "send") {
    const task = tasks[0]
    thread = task.kind === "open" ? task.threadId : null
    tasks = tasks.slice(1)
  }
  return tasks === state.tasks
    ? state
    : { ...state, thread, tasks, chosen: true }
}

function amend(
  state: PrimaryChat,
  id: number,
  change: (message: FirstMessage) => FirstMessage
): PrimaryChat {
  const tasks = state.tasks.map((task) =>
    task.id === id && task.kind === "send" ? change(task) : task
  )
  return tasks.some((task, index) => task !== state.tasks[index])
    ? { ...state, tasks }
    : state
}

export function primaryChatReducer(
  state: PrimaryChat,
  action: PrimaryChatAction
): PrimaryChat {
  switch (action.type) {
    case "request": {
      const { request } = action
      const text = requestMessage(request)
      const task: ChatTask =
        request.kind === "thread"
          ? { id: request.id, kind: "open", threadId: request.threadId }
          : text
            ? { id: request.id, kind: "send", text, screen: action.screen }
            : { id: request.id, kind: "blank" }
      return settle({
        ...state,
        tasks: [...state.tasks, task],
        touched: state.touched + 1,
        notice: "",
      })
    }
    case "refused": {
      const text = requestMessage(action.request)
      if (!text) return state
      return {
        ...state,
        unsent: [
          ...state.unsent,
          {
            id: action.request.id,
            text,
            screen: action.screen,
            notice: action.notice,
          },
        ],
        touched: state.touched + 1,
        notice: "",
      }
    }
    case "submit": {
      const text = action.text.trim()
      if (!text) return state
      const own = state.own + 1
      return settle({
        ...state,
        own,
        tasks: [
          ...state.tasks,
          { id: -own, kind: "send", text, screen: action.screen },
        ],
        // The composer gives its text up; a request's message never did.
        drafts: { ...state.drafts, [PRIMARY_DRAFT]: "" },
        touched: state.touched + 1,
        notice: "",
      })
    }
    case "decide":
      return state.thread === undefined && state.tasks.length === 0
        ? { ...state, thread: action.thread, chosen: action.resumeNew === true }
        : state
    case "move": {
      let tasks = state.tasks
      const head = tasks.at(0)
      if (head?.kind === "send") {
        // Not yet in its conversation's hands: it goes first.
        if (!head.taken) return state
        tasks = tasks.slice(1)
      }
      return settle({
        ...state,
        tasks,
        thread: action.thread,
        chosen: true,
        notice: action.notice ?? "",
      })
    }
    case "making":
      return amend(state, action.id, (message) => ({
        ...message,
        making: true,
      }))
    case "made":
      return amend(state, action.id, (message) =>
        message.threadId ? message : { ...message, threadId: action.threadId }
      )
    case "taken":
      return amend(state, action.id, (message) =>
        message.threadId ? { ...message, taken: true } : message
      )
    case "sent": {
      const head = state.tasks.at(0)
      if (head?.id !== action.id || head.kind !== "send" || !head.taken) {
        return state
      }
      return settle({
        ...state,
        thread: head.threadId ?? null,
        chosen: true,
        tasks: state.tasks.slice(1),
      })
    }
    case "failed": {
      const failed = state.tasks.find((task) => task.id === action.id)
      if (!failed || failed.kind !== "send") return state
      // The words are kept as a message of their own, in view whatever the
      // chat does next, and the composer is never touched: what is in it is
      // the member's, and a message put there could be typed over.
      const head = failed === state.tasks[0]
      return settle({
        ...state,
        tasks: state.tasks.filter((task) => task.id !== action.id),
        thread: head ? null : state.thread,
        chosen: head ? true : state.chosen,
        unsent: [
          ...state.unsent,
          {
            id: failed.id,
            text: failed.text,
            screen: failed.screen,
            notice: action.notice,
          },
        ],
        notice: "",
      })
    }
    case "retry": {
      const message = state.unsent.find((item) => item.id === action.id)
      if (!message) return state
      // As a send from the composer is: last in line, in a conversation of
      // its own, with the screen it was first sent from.
      const own = state.own + 1
      return settle({
        ...state,
        own,
        unsent: state.unsent.filter((item) => item.id !== action.id),
        tasks: [
          ...state.tasks,
          {
            id: -own,
            kind: "send",
            text: message.text,
            screen: message.screen,
          },
        ],
        touched: state.touched + 1,
        notice: "",
      })
    }
    case "edit": {
      const message = state.unsent.find((item) => item.id === action.id)
      // Never over what the composer holds.
      if (!message || (state.drafts[PRIMARY_DRAFT] ?? "").trim() !== "") {
        return state
      }
      return {
        ...state,
        drafts: { ...state.drafts, [PRIMARY_DRAFT]: message.text },
        unsent: state.unsent.filter((item) => item.id !== action.id),
      }
    }
    case "dismiss":
      return state.unsent.some((item) => item.id === action.id)
        ? {
            ...state,
            unsent: state.unsent.filter((item) => item.id !== action.id),
          }
        : state
    case "draft":
      return {
        ...state,
        drafts: { ...state.drafts, [action.key]: action.text },
      }
    case "reset":
      // The counter goes on, so a step still coming for an old send can't
      // land on a new one.
      return { ...initialPrimaryChat(), own: state.own }
  }
}

/** The message on its way: the task at the front, when it starts a conversation with one. */
export function firstMessage(state: PrimaryChat): FirstMessage | null {
  const head = state.tasks.at(0)
  return head?.kind === "send" ? head : null
}

/**
 * The conversation the chat shows: the one being made for the message on its
 * way (a blank chat until it exists), else the one the chat is on.
 */
export function shownThread(state: PrimaryChat): string | null | undefined {
  const message = firstMessage(state)
  return message ? (message.threadId ?? null) : state.thread
}

/**
 * Records real member presence/leave for the conversation actually shown.
 * Existing automatically opened conversations count too; only explicit New
 * is remembered as New, so an automatic blank cannot mask a pending approval.
 */
export function useRememberedChoice(
  chat: PrimaryChat,
  scope: ChatScope | undefined,
  agentId: string | undefined,
  visible = true
) {
  useRememberedView(
    shownThread(chat),
    scope,
    agentId,
    visible,
    chat.chosen || firstMessage(chat) !== null
  )
}
