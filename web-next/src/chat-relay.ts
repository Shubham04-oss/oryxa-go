import type { UIMessage } from "ai"
import { sessionExpiredCode } from "./lib/threads/chat-messages"
import type { ConfirmationAnswer, Item, RunInput } from "./lib/threads/client"
import { readItemStream } from "./lib/threads/event-stream"

// What a chat Durable Object does with one of Go's runs, whichever runs route
// it calls: read the member's input from the chat turn, POST it as the
// member, and relay the items the run streams. ThreadChatAgent and
// SpecialistChatAgent differ only in the route, the body, what the page is
// shown of an item, and their words.

/** A run Go would not start, or one that could not be asked for, in words for the member. */
export class RunStartError extends Error {
  /**
   * `sessionExpiredCode` when Go answered 401, otherwise Go's refusal code
   * (`run_in_progress`, `confirmation_pending`, ...), when it sent one.
   */
  readonly code: string | undefined

  constructor(message: string, code?: string) {
    super(message)
    this.name = "RunStartError"
    this.code = code
  }
}

/**
 * The member's answers from a chat request's body, or nothing unless there
 * is at least one and every one is well formed. Only `id` and `confirmed`
 * go on: Go refuses a body with any other field.
 */
export function confirmationInput(
  value: unknown
): ConfirmationAnswer[] | undefined {
  if (!Array.isArray(value) || value.length === 0) return undefined
  const answers: ConfirmationAnswer[] = []
  for (const answer of value as unknown[]) {
    if (typeof answer !== "object" || answer === null) return undefined
    const { id, confirmed } = answer as { id?: unknown; confirmed?: unknown }
    if (typeof id !== "string" || id === "" || typeof confirmed !== "boolean") {
      return undefined
    }
    answers.push({ id, confirmed })
  }
  return answers
}

/**
 * What a chat turn asks Go for. Answers to confirmations travel in the chat
 * request's body (`sendMessage(placeholder, { body: { confirmations } })`);
 * anything else sends the text of the member's latest message, which
 * useAgentChat has already stored. Throws `RunStartError` when there is
 * nothing to send.
 */
export function inputFromMessages(
  messages: readonly UIMessage[],
  body: Record<string, unknown> | undefined
): RunInput {
  if (body?.confirmations !== undefined) {
    const confirmations = confirmationInput(body.confirmations)
    if (!confirmations) throw new RunStartError("Invalid confirmation answers.")
    return { confirmations }
  }
  let lastUser: UIMessage | undefined
  for (let index = messages.length - 1; index >= 0; index -= 1) {
    if (messages[index].role === "user") {
      lastUser = messages[index]
      break
    }
  }
  const text = (lastUser?.parts ?? [])
    .flatMap((part) => (part.type === "text" ? [part.text] : []))
    .join("")
    .trim()
  if (!text) throw new RunStartError("Send a message to start a run.")
  return { message: text }
}

/** Go's words for a refused run, as a sentence: they start lower case and end bare. */
export function sentence(text: string): string {
  const trimmed = text.trim()
  if (!trimmed) return trimmed
  const capitalized = trimmed[0].toUpperCase() + trimmed.slice(1)
  return /[.!?]$/.test(capitalized) ? capitalized : `${capitalized}.`
}

/**
 * The member's words for each code Go refuses a run with, by that code.
 * `invalid_request` always takes Go's own words, which say what to change;
 * a code with no words here reads as the agent not starting.
 */
export type RefusalWords = Readonly<Partial<Record<string, string>>>

/** The primary chat's words for Go's refusals. */
export const primaryRefusals: RefusalWords = {
  run_in_progress:
    "Your primary agent is still answering in this conversation.",
  confirmation_pending: "Answer the confirmation above to continue.",
  agents_unavailable: "Your primary agent isn't available right now.",
  not_found: "This conversation no longer exists.",
  rate_limited: "Too many requests at once. Wait a moment and try again.",
}

/**
 * Why Go refused to start a run, from its answer, in words the member can
 * act on: `refusals` for Go's code, the primary chat's by default. A 401
 * means the cookie this socket was opened with has expired, and only the
 * page can refresh it: it does, reopens the socket and sends again, once, so
 * the member reads this only when that did not help either.
 */
export async function runStartError(
  response: Response,
  refusals: RefusalWords = primaryRefusals
): Promise<RunStartError> {
  if (response.status === 401) {
    return new RunStartError(
      "Your session has ended. Sign in again to continue.",
      sessionExpiredCode
    )
  }
  let code = ""
  let message = ""
  try {
    const body: { error?: string | { code?: string; message?: string } } =
      await response.json()
    if (typeof body.error === "object") {
      code = body.error.code ?? ""
      message = body.error.message ?? ""
    }
  } catch {
    // A gateway can answer with something that is not JSON.
  }
  const refused = (words: string) => new RunStartError(words, code || undefined)
  if (code === "invalid_request") {
    // Go's words for this code say what to change.
    if (message) return refused(sentence(message))
  } else if (code && Object.hasOwn(refusals, code)) {
    const words = refusals[code]
    if (words) return refused(words)
  }
  return refused(`The agent could not start (${response.status}).`)
}

/** The words a relay writes in its own `run_failed` items. */
export type RelayWords = {
  /** For the codes Go refuses a run with. */
  refusals: RefusalWords
  /** A stream that ended or broke before the run's last item. */
  cutOff: string
  /** Go could not be reached, or answered with something that is not a run. */
  unreachable: string
}

/** What a socket keeps from its upgrade request to run as the member. */
export type MemberSocket = {
  cookie: string
  /** The page's origin; Go takes a cookie-authenticated change only from an allowed one. */
  origin: string
}

/** One turn's request to Go. */
export type GoRunRequest = {
  /** Go's runs route for this turn. */
  url: URL
  /** The JSON body: the input, and whatever else the route reads. */
  body: RunInput & Record<string, unknown>
  /** What the member sent, so answered confirmations can be echoed. */
  input: RunInput
}

export type RelayGoRunOptions<TSocket extends MemberSocket> = {
  /** The socket's state from `onConnect`; missing when it was never set. */
  connection: TSocket | null | undefined
  /** This turn's request. Throws `RunStartError` when there is nothing to send. */
  request: (connection: TSocket) => GoRunRequest
  words: RelayWords
  /** Names the object in what is logged. */
  label: string
  /** The turn's abort signal: the member stopped, or the transcript is being purged. */
  signal: AbortSignal | undefined
  /** Receives every item for the page, in order. */
  write: (item: Item) => void
  /** Sees Go's accepted answer, before any item. */
  onAccepted?: (response: Response) => void
  /**
   * What the page is shown of an item Go streamed; nothing leaves it out.
   * The member's own echoed message is always left out.
   */
  present?: (item: Item) => Item | undefined
}

/**
 * One chat turn: POSTs Go's run as the member and writes what it streams.
 *
 * Everything the member should see arrives as an item, refusals included, as
 * a `run_failed` with words for them and, when there is one, a `code` (Go's,
 * or `sessionExpiredCode` for a 401 or a socket that holds no cookie). The
 * member's own message is left out (the transcript has it), and answered
 * confirmations are echoed with their status once Go accepts the run, since
 * Go streams a confirmation only while it waits.
 *
 * It resolves as soon as the run's last item arrives: `readItemStream` stops
 * there rather than waiting out the up to 15 seconds Go keeps the response
 * open while it writes the conversation to memory. A stream that ends or
 * breaks before its last item gets a `run_failed`, so no draft is left
 * looking as if it were still being written. An aborted turn writes nothing
 * more.
 */
export async function relayGoRun<TSocket extends MemberSocket>({
  connection,
  request,
  words,
  label,
  signal,
  write,
  onAccepted,
  present,
}: RelayGoRunOptions<TSocket>): Promise<void> {
  let runId: string | undefined
  try {
    if (!connection?.cookie) {
      // Nothing ran. A reopened socket carries the page's cookie again.
      throw new RunStartError(
        "Your session has ended. Sign in again to continue.",
        sessionExpiredCode
      )
    }
    const { url, body, input } = request(connection)
    const response = await fetch(url, {
      method: "POST",
      headers: {
        "Content-Type": "application/json",
        Cookie: connection.cookie,
        // Go takes a cookie-authenticated change only from an allowed origin.
        ...(connection.origin ? { Origin: connection.origin } : {}),
      },
      body: JSON.stringify(body),
      signal,
    })
    if (!response.ok) throw await runStartError(response, words.refusals)
    runId = response.headers.get("X-Oryxa-Run") ?? undefined
    onAccepted?.(response)
    if (!response.body) {
      throw new RunStartError("The agent did not return a stream.")
    }
    if ("confirmations" in input) {
      for (const answer of input.confirmations) {
        write({
          type: "confirmation",
          call_id: answer.id,
          status: answer.confirmed ? "approved" : "declined",
        })
      }
    }
    const result = await readItemStream(response.body, {
      signal,
      onItem: (item) => {
        if (item.type === "message" && item.author === "member") return
        const shown = present ? present(item) : item
        if (shown) write(shown)
      },
    })
    if (result.outcome === "incomplete") {
      write({
        type: "run_failed",
        run_id: result.runId ?? runId,
        error: words.cutOff,
      })
    }
  } catch (error) {
    if (signal?.aborted) return
    if (!(error instanceof RunStartError)) {
      console.error(`${label}: the run could not reach Go`, error)
    }
    write({
      type: "run_failed",
      run_id: runId,
      error: error instanceof RunStartError ? error.message : words.unreachable,
      code: error instanceof RunStartError ? error.code : undefined,
    })
  }
}
