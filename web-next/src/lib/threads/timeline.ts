import type { SpecialistResult } from "@/lib/specialists/result"
import { sessionExpiredCode } from "./chat-messages"
import type {
  ConfirmationStatus,
  Item,
  ItemUsage,
  ThreadDetail,
} from "./client"

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
  /** A matching retry started after this failure and returned successfully. */
  recovered?: boolean
}

/** Only explicit JSON arguments and a known author can establish a retry. */
function toolRetryIdentity(tool: ToolEntry, group: number): string | undefined {
  if (
    !tool.tool ||
    !tool.author ||
    !tool.arguments ||
    typeof tool.arguments !== "object" ||
    Array.isArray(tool.arguments)
  )
    return undefined
  try {
    return JSON.stringify(
      [group, tool.tool, tool.author, tool.arguments],
      (_key, value: unknown) => {
        if (
          value === undefined ||
          typeof value === "function" ||
          typeof value === "symbol" ||
          (typeof value === "number" && !Number.isFinite(value))
        )
          throw new Error("Tool arguments must be JSON")
        if (
          value !== null &&
          typeof value === "object" &&
          !Array.isArray(value)
        )
          return Object.fromEntries(
            Object.entries(value).sort(([left], [right]) =>
              left < right ? -1 : left > right ? 1 : 0
            )
          )
        return value
      }
    )
  } catch {
    return undefined
  }
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

/**
 * A run that failed, with Go's words for the member and, where it says, why
 * (`runFailureCode`). A stream has one, and since Go stored runs (#390) so
 * does the thread's history.
 */
export type RunFailedEntry = {
  kind: "run_failed"
  key: string
  runId?: string
  at?: string
  error: string
  code?: string
}

/**
 * Why Go says a run failed (`code` on its `run_failed`, #390), each said in
 * plain words by `runFailureWords`: Oryxa restarted mid-run, it ran out of
 * time, the member stopped it, the caller went away, another run took the
 * conversation over, or something else went wrong.
 */
export type RunFailureCode =
  "interrupted" | "timeout" | "stopped" | "cancelled" | "taken_over" | "error"

const runFailures: Record<RunFailureCode, { label: string; words: string }> = {
  interrupted: {
    label: "Interrupted",
    words: "Oryxa restarted while your agent was working, so this run stopped.",
  },
  timeout: {
    label: "Timed out",
    words: "This run took too long, so it was stopped.",
  },
  stopped: { label: "Stopped", words: "You stopped this run." },
  cancelled: {
    label: "Cancelled",
    words: "This run was cancelled before it finished.",
  },
  taken_over: {
    label: "Taken over",
    words: "Another run took over this conversation, so this one stopped.",
  },
  error: {
    label: "Failed",
    words: "Your agent ran into a problem and couldn't finish.",
  },
}

/** A failed run's code, if it is one Go gives (`RunFailureCode`). */
export function runFailureCode(
  code: string | undefined
): RunFailureCode | undefined {
  return code !== undefined && Object.hasOwn(runFailures, code)
    ? (code as RunFailureCode)
    : undefined
}

/**
 * A failed run in words: a short label for its card's status, and a sentence
 * for its notice when Go sent no words of its own. A code Go does not give
 * (a chat Durable Object's refusal, or none) reads as plain Failed.
 */
export function runFailureWords(code: string | undefined): {
  label: string
  words: string
} {
  const known = runFailureCode(code)
  return known
    ? runFailures[known]
    : { label: "Failed", words: "Your agent couldn't answer. Try again." }
}

/**
 * What names a run's start or end, so the same one is shown once: its `id`
 * (`<run>.started` or `<run>.ended`, the same on the streamed bracket and the
 * stored one, #390), or for a bracket without one (an older stream, or a
 * `run_failed` a chat Durable Object wrote itself) the id Go would give it.
 * Nothing for a bracket that names no run, which is never deduped.
 */
function bracketKey(item: Item): string | undefined {
  if (item.id) return item.id
  if (!item.run_id) return undefined
  return `${item.run_id}.${item.type === "run_started" ? "started" : "ended"}`
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
 * was never finished, so two runs' deltas never share a bubble. A run's start
 * and end show once each however many copies arrive (a streamed one and the
 * stored one, see `bracketKey`); an end Go stored replaces one a chat Durable
 * Object wrote when the stream broke, which only guessed how the run ended. A run that
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
  const attempts = new Map<
    ToolEntry,
    { start: number; result?: number; identity?: string }
  >()
  const failedAttempts = new Map<string, Set<ToolEntry>>()
  let toolGroup = 0
  const confirmations = new Map<string, ConfirmationEntry>()
  const artifacts = new Set<string>()
  const requests = new Map<string, RequestEntry>()
  // Each run's start and end seen so far: whether Go sent it (it has an id),
  // and the notice an end put up.
  const brackets = new Map<
    string,
    { stored: boolean; notice?: RunFailedEntry }
  >()
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
        if (entry.role !== "agent") toolGroup += 1
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
        attempts.set(entry, {
          start: index,
          identity: toolRetryIdentity(entry, toolGroup),
        })
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
          delete entry.recovered
          const current = attempts.get(entry)
          if (current) current.result = index
          if (current?.identity) {
            const failed =
              failedAttempts.get(current.identity) ?? new Set<ToolEntry>()
            if (entry.state === "failed") {
              failed.add(entry)
              failedAttempts.set(current.identity, failed)
            } else {
              failed.delete(entry)
              for (const previous of failed) {
                const attempt = attempts.get(previous)
                // A parallel duplicate is no proof of a retry: this call must
                // have started after the previous call's final failed result.
                if (
                  attempt?.result !== undefined &&
                  attempt.result < current.start
                ) {
                  previous.recovered = true
                  failed.delete(previous)
                }
              }
              if (failed.size === 0) failedAttempts.delete(current.identity)
            }
          }
        }
        break
      }
      case "confirmation": {
        const status: ConfirmationStatus =
          item.status === "approved" ||
          item.status === "declined" ||
          item.status === "expired"
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
                : status === "expired"
                  ? "interrupted"
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
      case "run_started": {
        const bracket = bracketKey(item)
        if (bracket !== undefined) {
          if (brackets.has(bracket)) break
          brackets.set(bracket, { stored: item.id !== undefined })
        }
        endDraft()
        break
      }
      case "run_completed":
      case "run_failed": {
        if (item.type === "run_failed" && item.code === sessionExpiredCode) {
          endRun()
          break
        }
        const bracket = bracketKey(item)
        const seen = bracket !== undefined ? brackets.get(bracket) : undefined
        if (seen) {
          // The same end again; only Go's own record replaces a guessed one.
          if (seen.stored || item.id === undefined) break
          if (seen.notice) entries.splice(entries.indexOf(seen.notice), 1)
        }
        const record: { stored: boolean; notice?: RunFailedEntry } = {
          stored: item.id !== undefined,
        }
        if (bracket !== undefined) brackets.set(bracket, record)
        endRun()
        if (item.type === "run_completed") break
        toolGroup += 1
        record.notice = {
          kind: "run_failed",
          key: uniqueKey(`run_failed:${item.run_id ?? index}`),
          runId: item.run_id,
          at: item.at,
          error: item.error ?? "",
          code: item.code,
        }
        entries.push(record.notice)
        break
      }
    }
  })

  return entries
}

/**
 * One run of the agent, as its start and end mark it: when it started and
 * ended, what it used, and why it failed. Runs streamed into a transcript
 * (the chat Durable Objects keep every item they relay) have them, and since
 * #390 so does a thread's history; runs from before that have none.
 */
export type RunSpan = {
  runId?: string
  startedAt?: string
  endedAt?: string
  usage?: ItemUsage
  failed?: boolean
  /** A failed run's `code`, where it gave one. */
  code?: string
}

/**
 * One task and the work that answered it: the message that started it (the
 * member's, or a batch from their apps), then everything the agent did until
 * the next one, and the runs that did it. Work before any prompt (a
 * conversation whose history starts mid-way) is a turn with no prompt.
 */
export type Turn = {
  key: string
  prompt?: MessageEntry
  entries: TimelineEntry[]
  runs: RunSpan[]
}

const isPrompt = (entry: TimelineEntry): entry is MessageEntry =>
  entry.kind === "message" && entry.role !== "agent"

/**
 * The entries `buildTimeline` made, as turns. Pass the items they were built
 * from (history then live, as `buildTimeline` merged them) to place each
 * run's markers in its turn: a run belongs to the turn of the last prompt
 * before it, counted the way `buildTimeline` counts messages (a message seen
 * again by its id is the same one). A start or end seen again is the same one
 * too (`bracketKey`), and an end Go stored replaces a guessed one, as there.
 */
export function groupTurns(
  entries: readonly TimelineEntry[],
  items: readonly Item[] = []
): Turn[] {
  const turns: Turn[] = []
  let current: Turn | undefined
  for (const entry of entries) {
    if (isPrompt(entry)) {
      current = {
        key: `turn:${entry.key}`,
        prompt: entry,
        entries: [],
        runs: [],
      }
      turns.push(current)
      continue
    }
    if (!current) {
      current = { key: `turn:${entry.key}`, entries: [], runs: [] }
      turns.push(current)
    }
    current.entries.push(entry)
  }

  // Runs by turn: the number of prompts before each, as above.
  const leading = turns[0] && !turns[0].prompt ? turns[0] : undefined
  const prompted = turns.filter((turn) => turn.prompt)
  const seen = new Set<string>()
  const starts = new Set<string>()
  const ends = new Map<string, { span: RunSpan; stored: boolean }>()
  let prompts = 0
  let run: RunSpan | undefined
  const turnAt = (count: number): Turn | undefined =>
    count === 0 ? leading : prompted[count - 1]
  for (const item of items) {
    if (item.type === "message") {
      if (roleOf(item.author ?? "") === "agent") continue
      if (item.id) {
        if (seen.has(item.id)) continue
        seen.add(item.id)
      }
      prompts += 1
    } else if (item.type === "run_started") {
      const bracket = bracketKey(item)
      if (bracket !== undefined) {
        if (starts.has(bracket)) continue
        starts.add(bracket)
      }
      run = { runId: item.run_id, startedAt: item.at }
      turnAt(prompts)?.runs.push(run)
    } else if (item.type === "run_completed" || item.type === "run_failed") {
      if (item.type === "run_failed" && item.code === sessionExpiredCode)
        continue
      const bracket = bracketKey(item)
      const known = bracket !== undefined ? ends.get(bracket) : undefined
      if (known) {
        if (known.stored || item.id === undefined) continue
        // Go's record of how the run ended, over the guess made for it.
        endSpan(known.span, item)
        known.stored = true
        continue
      }
      if (!run || (item.run_id && run.runId && item.run_id !== run.runId)) {
        // An end with no start in this list: the run began before it.
        run = { runId: item.run_id }
        turnAt(prompts)?.runs.push(run)
      }
      endSpan(run, item)
      if (bracket !== undefined)
        ends.set(bracket, { span: run, stored: item.id !== undefined })
      run = undefined
    }
  }
  return turns
}

/**
 * Why a turn's run failed, as its last failure says (`code`): its notice's,
 * or its run's end's. Nothing when no failure gave one.
 */
export function turnFailureCode(turn: Turn): string | undefined {
  for (let index = turn.entries.length - 1; index >= 0; index -= 1) {
    const entry = turn.entries[index]
    if (entry.kind === "run_failed" && entry.code) return entry.code
  }
  return [...turn.runs].reverse().find((run) => run.failed && run.code)?.code
}

/** Writes a run's end onto its span, in place of any end it had. */
function endSpan(span: RunSpan, item: Item) {
  span.endedAt = item.at
  if (item.usage) span.usage = item.usage
  else delete span.usage
  if (item.type === "run_failed") {
    span.failed = true
    span.code = item.code
  } else {
    delete span.failed
    delete span.code
  }
}

/** How a turn stands, for its run card. */
export type TurnStatus =
  "working" | "waiting" | "done" | "interrupted" | "failed"

/**
 * A turn's status. A confirmation waiting for the member comes first, then
 * a run still working (`live`: the last turn, while a run streams or its end
 * is not yet certain), then a failure, then anything cut short: an answer or
 * a call its run left unfinished, or a task that got no answer at all.
 */
export function turnStatus(
  turn: Turn,
  {
    live,
    waiting,
  }: {
    live: boolean
    /** The confirmations the member can answer now, by call id. */
    waiting: ReadonlySet<string>
  }
): TurnStatus {
  if (
    turn.entries.some(
      (entry) => entry.kind === "confirmation" && waiting.has(entry.callId)
    )
  )
    return "waiting"
  if (live) return "working"
  if (
    turn.entries.some((entry) => entry.kind === "run_failed") ||
    turn.runs.some((run) => run.failed)
  )
    return "failed"
  const cut = turn.entries.some(
    (entry) =>
      (entry.kind === "message" &&
        entry.role === "agent" &&
        entry.state !== "complete") ||
      (entry.kind === "tool" &&
        (entry.state === "interrupted" || entry.state === "running"))
  )
  if (cut || turn.entries.length === 0) return "interrupted"
  return "done"
}

function timeOf(at: string | undefined): number | undefined {
  if (!at) return undefined
  const value = Date.parse(at)
  return Number.isFinite(value) ? value : undefined
}

/**
 * When a turn's work started and ended, in ms, as far as its items say: its
 * runs' markers where it has them, otherwise the prompt's time and its last
 * item's. `end` is left out while the turn is still being worked on, and
 * whenever the items say nothing readable.
 */
export function turnTimes(
  turn: Turn,
  { finished }: { finished: boolean }
): { start?: number; end?: number } {
  const start = [
    timeOf(turn.runs[0]?.startedAt),
    timeOf(turn.prompt?.at),
    ...turn.entries.map((entry) => timeOf(entry.at)),
  ].find((value) => value !== undefined)
  if (!finished || start === undefined) return { start }
  const ends = [
    ...turn.runs.map((run) => timeOf(run.endedAt)),
    ...turn.entries.map((entry) => timeOf(entry.at)),
  ].filter((value): value is number => value !== undefined)
  const end = ends.length > 0 ? Math.max(...ends) : undefined
  return { start, end: end !== undefined && end > start ? end : undefined }
}

/**
 * How long a finished turn's runs worked, in ms: each run's end less its
 * start, added up, so the time a confirmation waited for the member between
 * two runs is left out. Nothing unless every run has both, readable and in
 * order (a run whose start has no end ended without a record, #390); the
 * card then falls back to `turnTimes`.
 */
export function turnDuration(turn: Turn): number | undefined {
  if (turn.runs.length === 0) return undefined
  let total = 0
  for (const run of turn.runs) {
    const start = timeOf(run.startedAt)
    const end = timeOf(run.endedAt)
    if (start === undefined || end === undefined || end < start)
      return undefined
    total += end - start
  }
  return total
}

/** What a turn's runs used, added up; nothing when no run says. */
export function turnUsage(turn: Turn): ItemUsage | undefined {
  const used = turn.runs.flatMap((run) => (run.usage ? [run.usage] : []))
  if (used.length === 0) return undefined
  const total = used.reduce<ItemUsage>(
    (sum, usage) => ({
      input_tokens: sum.input_tokens + usage.input_tokens,
      output_tokens: sum.output_tokens + usage.output_tokens,
      total_tokens: sum.total_tokens + usage.total_tokens,
      ...(sum.cached_tokens !== undefined || usage.cached_tokens !== undefined
        ? {
            cached_tokens:
              (sum.cached_tokens ?? 0) + (usage.cached_tokens ?? 0),
          }
        : {}),
    }),
    { input_tokens: 0, output_tokens: 0, total_tokens: 0 }
  )
  return total
}

const keyArgumentNames = [
  "query",
  "q",
  "search",
  "url",
  "file_path",
  "path",
  "file",
  "filename",
  "name",
  "channel",
  "title",
  "subject",
  "to",
]

function argumentWords(value: unknown): string | undefined {
  if (typeof value === "number" && Number.isFinite(value)) return String(value)
  if (typeof value !== "string") return undefined
  const words = value.replace(/\s+/g, " ").trim()
  if (!words) return undefined
  return words.length > 80 ? `${words.slice(0, 79)}…` : words
}

/**
 * The one argument that says what a call was about (its query, file, URL or
 * channel), in a few words, or nothing. An app call's own arguments sit one
 * level down, and are read first.
 */
export function toolKeyArgument(
  args: Record<string, unknown> | undefined,
  depth = 0
): string | undefined {
  if (!args || depth > 2) return undefined
  for (const nested of ["arguments", "input", "params"]) {
    const inner = args[nested]
    if (inner && typeof inner === "object" && !Array.isArray(inner)) {
      const found = toolKeyArgument(inner as Record<string, unknown>, depth + 1)
      if (found) return found
    }
  }
  for (const name of keyArgumentNames) {
    const words = argumentWords(args[name])
    if (words) return words
  }
  return undefined
}

/**
 * The web addresses a turn's calls were given, in order and once each: what
 * the agent opened, as its calls said. Only http and https count.
 */
export function openedLinks(entries: readonly TimelineEntry[]): URL[] {
  const links = new Map<string, URL>()
  const visit = (value: unknown, depth: number) => {
    if (depth > 3 || links.size >= 12) return
    if (typeof value === "string") {
      const text = value.trim()
      if (!/^https?:\/\/\S+$/i.test(text)) return
      try {
        const url = new URL(text)
        links.set(url.href, url)
      } catch {
        // Not an address after all.
      }
      return
    }
    if (value && typeof value === "object") {
      for (const part of Object.values(value)) visit(part, depth + 1)
    }
  }
  for (const entry of entries) {
    if (entry.kind === "tool") visit(entry.arguments, 0)
  }
  return [...links.values()]
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
 * before its text. Deltas carry no id and always stay; so do run markers from
 * a Go without #390, which streamed them without one. Since #390 a streamed
 * run marker carries the stored one's id and is left out like any other item.
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
