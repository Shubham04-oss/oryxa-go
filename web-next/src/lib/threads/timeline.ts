import type { SpecialistResult } from "@/lib/specialists/result"
import { sessionExpiredCode } from "./chat-messages"
import type { ConfirmationStatus, Item, ThreadDetail } from "./client"

/** Who a message is from, for how it is shown. */
export type MessageRole = "member" | "apps" | "agent"

export type MessageEntry = {
  kind: "message"
  /** Unique within one timeline, and stable across renders: use it as the React key. */
  key: string
  /** The stored event's id; missing on a draft still being written. */
  id?: string
  at?: string
  /** `"member"`, `"apps"`, or the display name of the agent that wrote it. */
  author: string
  /** `apps` is a batch of events from the member's connected apps that woke the agent: a notice, not a message from either side. */
  role: MessageRole
  text: string
  /** Why the model stopped without (fully) answering. */
  error?: string
  /**
   * A specialist's work result, when the message is one: its status, the
   * tools it used, the files it saved and the hand-offs it suggests. `text`
   * is then the result's summary.
   */
  result?: SpecialistResult
  /**
   * `streaming` for a draft built from deltas, replaced in place when its
   * finished message arrives; `interrupted` for a draft whose run ended
   * without one, which Go did not store either. The specialist chat streams
   * a draft with no text while its specialist writes (`isProgressMarker`):
   * show it as the agent working.
   */
  state: "streaming" | "complete" | "interrupted"
}

/**
 * `waiting_approval` while a confirmation holds the call back; `declined`
 * once the member declined it (Go then reports the rejection as a failed
 * result, which leaves it declined); `running` again once approved, until its
 * result makes it `done` or `failed`. `interrupted` when its run ended (it
 * completed, failed or was stopped) before the call got a result.
 */
export type ToolState =
  | "running"
  | "waiting_approval"
  | "declined"
  | "done"
  | "failed"
  | "interrupted"

export type ToolEntry = {
  kind: "tool"
  key: string
  callId: string
  id?: string
  at?: string
  author?: string
  tool: string
  /** Left out when Go found them too large to show. */
  arguments?: Record<string, unknown>
  state: ToolState
  /** A failed result's error. Go never sends a result's content. */
  error?: string
}

export type ConfirmationEntry = {
  kind: "confirmation"
  key: string
  /** What the member's answer names. */
  callId: string
  /** The tool call it holds back. */
  toolCallId?: string
  id?: string
  at?: string
  author?: string
  tool?: string
  arguments?: Record<string, unknown>
  /** The tool's own reason; missing when Go had only ADK's developer-facing default (see `confirmationHint`). */
  hint?: string
  status: ConfirmationStatus
}

export type ArtifactEntry = {
  kind: "artifact"
  key: string
  id?: string
  at?: string
  author?: string
  /** The file's name, as `artifactContentUrl` takes it. */
  name: string
  /** Missing only if Go left it out, which reads the latest version. */
  version?: number
}

/** A run that failed, with Go's words for the member. Only a stream has one: history does not keep them. */
export type RunFailedEntry = {
  kind: "run_failed"
  key: string
  runId?: string
  at?: string
  error: string
  code?: string
}

/** A request filed in this conversation, read live using the member's session. */
export type RequestEntry = {
  kind: "request"
  key: string
  id?: string
  at?: string
  requestId: string
  text?: string
  status?: Item["status"]
}

export type TimelineEntry =
  | MessageEntry
  | ToolEntry
  | ConfirmationEntry
  | ArtifactEntry
  | RunFailedEntry
  | RequestEntry

function roleOf(author: string): MessageRole {
  if (author === "member") return "member"
  if (author === "apps") return "apps"
  return "agent"
}

/**
 * The blocks a conversation renders, in order: messages (a growing draft
 * while deltas stream), tool calls with their results and confirmations
 * merged in by `call_id`, the files runs kept, and runs that failed.
 *
 * Pass a thread's history, and the items streamed since it was read as
 * `live`: what the history already holds is shown once (see
 * `unseenLiveItems`), so the list can be refetched mid-run or after one
 * without the run appearing twice. A single list, such as a Durable Object
 * transcript from `chatMessagesToItems`, goes in as `history`.
 *
 * `run_started` and `run_completed` render nothing, but end any draft that
 * was never finished, so two runs' deltas never share a bubble. A run that
 * ends (`run_completed` or `run_failed`), or the member's next message, which
 * only follows a run's end, also leaves the tool calls that got no result
 * `interrupted`: they will never get one. A `run_failed` with
 * `sessionExpiredCode` renders nothing: it is the Durable Object asking the
 * page to refresh the session and send again.
 */
export function buildTimeline(
  history: readonly Item[],
  live: readonly Item[] = []
): TimelineEntry[] {
  const items =
    live.length === 0
      ? history
      : [...history, ...unseenLiveItems(history, live)]
  const entries: TimelineEntry[] = []
  const keys = new Set<string>()
  const messages = new Map<string, MessageEntry>()
  const tools = new Map<string, ToolEntry>()
  // Every call, including those with no call id to find them by.
  const calls: ToolEntry[] = []
  const confirmations = new Map<string, ConfirmationEntry>()
  const artifacts = new Set<string>()
  const requests = new Map<string, RequestEntry>()
  let draft: MessageEntry | undefined
  let drafts = 0

  // Keys come from Go's ids, which items can share; this keeps them unique
  // whatever the input, so React never sees two blocks with one key.
  const uniqueKey = (base: string) => {
    let key = base
    for (let copy = 2; keys.has(key); copy += 1) key = `${base}#${copy}`
    keys.add(key)
    return key
  }
  const endDraft = () => {
    if (draft) draft.state = "interrupted"
    draft = undefined
  }
  // A call still running that its run is over for never gets its result.
  // One held back by a confirmation is waiting, not lost.
  const interruptRunning = () => {
    for (const call of calls) {
      if (call.state === "running") call.state = "interrupted"
    }
  }
  // A run that ended: no draft goes on, and its calls are done with.
  const endRun = () => {
    endDraft()
    interruptRunning()
  }

  items.forEach((item, index) => {
    switch (item.type) {
      case "delta": {
        const author = item.author ?? ""
        // A different writer, such as a sub-agent the turn was handed to,
        // starts a bubble of its own.
        if (draft && draft.author !== author) endDraft()
        if (!draft) {
          draft = {
            kind: "message",
            key: uniqueKey(`draft:${drafts}`),
            at: item.at,
            author,
            role: roleOf(author),
            text: "",
            state: "streaming",
          }
          drafts += 1
          entries.push(draft)
        }
        draft.text += item.text ?? ""
        break
      }
      case "message": {
        const author = item.author ?? ""
        // Go takes a message only once the run before it is over, so the
        // member speaking again says that run's calls are too, whether or not
        // its end marker came (a stopped run leaves none; history has none).
        if (item.author === "member") interruptRunning()
        const drafted = draft && draft.author === author ? draft : undefined
        const existing = item.id ? messages.get(item.id) : undefined
        if (existing) {
          // Already shown: keep its place, take the latest words, and drop
          // the draft that was writing it again.
          existing.text = item.text ?? ""
          existing.error = item.error
          existing.result = item.result
          if (drafted) {
            entries.splice(entries.indexOf(drafted), 1)
            draft = undefined
          }
          break
        }
        const entry: MessageEntry = {
          kind: "message",
          key: uniqueKey(`message:${item.id ?? index}`),
          id: item.id,
          at: item.at,
          author,
          role: roleOf(author),
          text: item.text ?? "",
          error: item.error,
          result: item.result,
          state: "complete",
        }
        if (item.id) messages.set(item.id, entry)
        if (drafted) {
          // The finished message takes its draft's place.
          entries[entries.indexOf(drafted)] = entry
          draft = undefined
        } else {
          endDraft()
          entries.push(entry)
        }
        break
      }
      case "tool_call": {
        const existing = item.call_id ? tools.get(item.call_id) : undefined
        if (existing) {
          existing.id ??= item.id
          existing.at ??= item.at
          existing.arguments ??= item.arguments
          break
        }
        const entry: ToolEntry = {
          kind: "tool",
          key: uniqueKey(`tool:${item.call_id ?? index}`),
          callId: item.call_id ?? "",
          id: item.id,
          at: item.at,
          author: item.author,
          tool: item.tool ?? "",
          arguments: item.arguments,
          state: "running",
        }
        if (item.call_id) tools.set(item.call_id, entry)
        calls.push(entry)
        entries.push(entry)
        break
      }
      case "tool_result": {
        let entry = item.call_id ? tools.get(item.call_id) : undefined
        if (!entry) {
          // History can start mid-call: show the result on its own rather
          // than drop it.
          entry = {
            kind: "tool",
            key: uniqueKey(`tool:${item.call_id ?? index}`),
            callId: item.call_id ?? "",
            id: item.id,
            at: item.at,
            author: item.author,
            tool: item.tool ?? "",
            state: "running",
          }
          if (item.call_id) tools.set(item.call_id, entry)
          calls.push(entry)
          entries.push(entry)
        }
        // A declined call never runs; ADK answers it with a failed result
        // ("call is rejected") that says no more than "declined" does.
        if (entry.state !== "declined") {
          entry.state = item.failed ? "failed" : "done"
          entry.error = item.error
        }
        break
      }
      case "confirmation": {
        const status: ConfirmationStatus =
          item.status === "approved" || item.status === "declined"
            ? item.status
            : "pending"
        let entry = item.call_id ? confirmations.get(item.call_id) : undefined
        if (entry) {
          // The same confirmation again, such as its answer: the latest
          // status wins, and the first copy keeps its place.
          entry.status = status
          entry.toolCallId ??= item.tool_call_id
          entry.id ??= item.id
          entry.at ??= item.at
          entry.author ??= item.author
          entry.tool ??= item.tool
          entry.arguments ??= item.arguments
          entry.hint ??= item.hint || undefined
        } else {
          entry = {
            kind: "confirmation",
            key: uniqueKey(`confirmation:${item.call_id ?? index}`),
            callId: item.call_id ?? "",
            toolCallId: item.tool_call_id,
            id: item.id,
            at: item.at,
            author: item.author,
            tool: item.tool,
            arguments: item.arguments,
            hint: item.hint || undefined,
            status,
          }
          if (item.call_id) confirmations.set(item.call_id, entry)
          entries.push(entry)
        }
        // The call it holds back shows as waiting rather than running. One
        // its own result already settled is left alone.
        const held = entry.toolCallId ? tools.get(entry.toolCallId) : undefined
        if (held && held.state !== "done" && held.state !== "failed") {
          held.state =
            status === "pending"
              ? "waiting_approval"
              : status === "declined"
                ? "declined"
                : "running"
        }
        break
      }
      case "request": {
        if (!item.request_id) break
        const previous = requests.get(item.request_id)
        if (previous) {
          previous.status = item.status
          previous.text = item.text ?? previous.text
          break
        }
        const entry: RequestEntry = {
          kind: "request",
          key: uniqueKey(`request:${item.request_id}`),
          id: item.id,
          at: item.at,
          requestId: item.request_id,
          text: item.text,
          status: item.status,
        }
        requests.set(item.request_id, entry)
        entries.push(entry)
        break
      }
      case "artifact": {
        // One stored event can keep several files, and all its items carry
        // its id, so the file and its version name the entry.
        const identity = `artifact:${item.id ?? ""}/${item.artifact ?? ""}@${item.version ?? ""}`
        if (artifacts.has(identity)) break
        artifacts.add(identity)
        entries.push({
          kind: "artifact",
          key: uniqueKey(identity),
          id: item.id,
          at: item.at,
          author: item.author,
          name: item.artifact ?? "",
          version: item.version,
        })
        break
      }
      case "run_started":
        endDraft()
        break
      case "run_completed":
        endRun()
        break
      case "run_failed":
        endRun()
        if (item.code === sessionExpiredCode) break
        entries.push({
          kind: "run_failed",
          key: uniqueKey(`run_failed:${item.run_id ?? index}`),
          runId: item.run_id,
          at: item.at,
          error: item.error ?? "",
          code: item.code,
        })
        break
    }
  })

  return entries
}

function isRunMarker(item: Item): boolean {
  return (
    item.type === "run_started" ||
    item.type === "run_completed" ||
    item.type === "run_failed"
  )
}

/**
 * The streamed items a thread's history does not hold yet. A refetched
 * history holds a finished run's items, and one read mid-run holds its first
 * ones; a stored item carries its event's id and so does the streamed copy,
 * so an item the history holds is left out, and so are the deltas that
 * drafted a message it holds, even when that message's event put a tool call
 * before its text. Run markers and deltas carry no id and always stay.
 */
export function unseenLiveItems(
  history: readonly Item[],
  live: readonly Item[]
): Item[] {
  const stored = new Set<string>()
  for (const item of history) {
    if (item.id) stored.add(item.id)
  }
  const unseen: Item[] = []
  let drafting: Item[] = []
  for (const item of live) {
    if (item.type === "delta") {
      drafting.push(item)
      continue
    }
    const held = item.id !== undefined && stored.has(item.id)
    if (item.type === "message") {
      // The deltas before a finished message drafted it: they go with it.
      if (!held) unseen.push(...drafting, item)
      drafting = []
      continue
    }
    if (isRunMarker(item)) {
      unseen.push(...drafting)
      drafting = []
    }
    if (!held) unseen.push(item)
  }
  unseen.push(...drafting)
  return unseen
}

/**
 * The confirmations the member has to answer now, by `call_id`: the thread's
 * `pending_confirmations` as Go last reported them, updated by what streamed
 * since (a new one asked, or an answer recorded). Go wants all of them
 * answered in one run, and refuses a message until they are.
 */
export function pendingConfirmationIds(
  detail: Pick<ThreadDetail, "items" | "pending_confirmations"> | undefined,
  live: readonly Item[] = []
): string[] {
  const waiting = new Set(detail?.pending_confirmations ?? [])
  for (const item of unseenLiveItems(detail?.items ?? [], live)) {
    if (item.type !== "confirmation" || !item.call_id) continue
    if ((item.status ?? "pending") === "pending") waiting.add(item.call_id)
    else waiting.delete(item.call_id)
  }
  return [...waiting]
}

/** A tool's name in words: "sandbox_execute_code" becomes "Sandbox execute code". */
export function humanizeToolName(tool: string): string {
  const spaced = tool.replace(/_/g, " ").trim()
  return spaced === "" ? tool : spaced.charAt(0).toUpperCase() + spaced.slice(1)
}

/**
 * Words for a confirmation card. Go leaves `hint` empty when ADK had only its
 * developer-facing default ("Please approve or reject the tool call …"), so
 * this fills in words for the member; a hint the tool wrote is kept.
 */
export function confirmationHint(
  tool: string | undefined,
  hint: string | undefined
): string {
  if (hint) return hint
  const name = tool ? humanizeToolName(tool) : "a tool"
  return `Your agent wants to use ${name}. It only runs if you approve.`
}

const imageArtifactExtensions: ReadonlySet<string> = new Set([
  "png",
  "jpg",
  "jpeg",
  "gif",
  "webp",
])

/**
 * The name a downloaded artifact is saved as, the way Go names it in
 * `Content-Disposition`: without the `user:` prefix, which is how ADK scopes
 * a member's own file rather than part of its name.
 */
export function artifactFileName(name: string): string {
  const base = name.startsWith("user:") ? name.slice("user:".length) : name
  return base.split("/").pop() || base
}

/**
 * Whether an artifact's name looks like an image Go shows inline, so a page
 * can render it as a thumbnail without reading it first. Go decides by the
 * stored media type; PDFs and plain text also open in place.
 */
export function isImageArtifact(name: string): boolean {
  const extension = artifactFileName(name).split(".").pop()?.toLowerCase()
  return extension !== undefined && imageArtifactExtensions.has(extension)
}
