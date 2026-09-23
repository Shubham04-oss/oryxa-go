import { AIChatAgent } from "@cloudflare/ai-chat"
import type {
  ChatRecoveryOptions,
  OnChatMessageOptions,
} from "@cloudflare/ai-chat"
import { getCurrentAgent } from "agents"
import type { Connection, ConnectionContext } from "agents"
import { createUIMessageStream, createUIMessageStreamResponse } from "ai"
import type { UIMessage } from "ai"
import {
  itemPart,
  sessionExpiredCode,
  threadAgentName,
  withoutSupersededDeltas,
} from "./lib/threads/chat-messages"
import type { ThreadChatMessage } from "./lib/threads/chat-messages"
import type { ConfirmationAnswer, Item, RunInput } from "./lib/threads/client"
import { readItemStream } from "./lib/threads/event-stream"

/** What the chat's Durable Object reads from the Worker's environment (wrangler.jsonc, `.dev.vars`). */
export interface ThreadChatEnv extends Cloudflare.Env {
  /** Where the Go API answers, such as `https://api.oryxa.in`. */
  ORYXA_API_ORIGIN: string
}

/**
 * The headers the Worker sets on every request it routes to the object,
 * replacing any the browser sent, once Go has said the member may read the
 * thread.
 */
export const chatRequestHeaders = {
  cookie: "X-Oryxa-Cookie",
  organization: "X-Oryxa-Organization",
  thread: "X-Oryxa-Thread",
} as const

/**
 * What a socket keeps from its upgrade request: every run it starts goes to
 * Go as the member, with this cookie and origin. A socket never learns a
 * newer cookie; the page reopens it after refreshing the session.
 */
export type ChatConnectionState = {
  cookie: string
  organizationId: string
  threadId: string
  origin: string
}

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
function sentence(text: string): string {
  const trimmed = text.trim()
  if (!trimmed) return trimmed
  const capitalized = trimmed[0].toUpperCase() + trimmed.slice(1)
  return /[.!?]$/.test(capitalized) ? capitalized : `${capitalized}.`
}

/**
 * Why Go refused to start a run, from its answer, in words the member can
 * act on. A 401 means the cookie this socket was opened with has expired, and
 * only the page can refresh it: it does, reopens the socket and sends again,
 * once, so the member reads this only when that did not help either.
 */
export async function runStartError(
  response: Response
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
  switch (code) {
    case "run_in_progress":
      return refused(
        "Your primary agent is still answering in this conversation."
      )
    case "confirmation_pending":
      return refused("Answer the confirmation above to continue.")
    case "agents_unavailable":
      return refused("Your primary agent isn't available right now.")
    case "not_found":
      return refused("This conversation no longer exists.")
    case "rate_limited":
      return refused("Too many requests at once. Wait a moment and try again.")
    case "invalid_request":
      // Go's words for this code say what to change.
      if (message) return refused(sentence(message))
      break
  }
  return refused(`The agent could not start (${response.status}).`)
}

export type RelayRunOptions = {
  /** Go's origin: `ThreadChatEnv.ORYXA_API_ORIGIN`. */
  apiOrigin: string
  /** The socket's state from `onConnect`; missing when it was never set. */
  connection: ChatConnectionState | null | undefined
  /** The transcript as the turn started, the member's new message last. */
  messages: readonly UIMessage[]
  /** The chat request's custom body: `{ confirmations }` for answers. */
  body: Record<string, unknown> | undefined
  /** The turn's abort signal: the member stopped, or the transcript is being purged. */
  signal: AbortSignal | undefined
  /** Receives every item for the page, in order. */
  write: (item: Item) => void
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
export async function relayRun({
  apiOrigin,
  connection,
  messages,
  body,
  signal,
  write,
}: RelayRunOptions): Promise<void> {
  let runId: string | undefined
  try {
    if (!connection?.cookie) {
      // Nothing ran. A reopened socket carries the page's cookie again.
      throw new RunStartError(
        "Your session has ended. Sign in again to continue.",
        sessionExpiredCode
      )
    }
    const input = inputFromMessages(messages, body)
    const path = `/v1/organizations/${encodeURIComponent(connection.organizationId)}/threads/${encodeURIComponent(connection.threadId)}/runs`
    const response = await fetch(new URL(path, apiOrigin), {
      method: "POST",
      headers: {
        "Content-Type": "application/json",
        Cookie: connection.cookie,
        // Go takes a cookie-authenticated change only from an allowed origin.
        ...(connection.origin ? { Origin: connection.origin } : {}),
      },
      body: JSON.stringify(input),
      signal,
    })
    if (!response.ok) throw await runStartError(response)
    runId = response.headers.get("X-Oryxa-Run") ?? undefined
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
        write(item)
      },
    })
    if (result.outcome === "incomplete") {
      write({
        type: "run_failed",
        run_id: result.runId ?? runId,
        error: "Your primary agent's answer was cut off before it finished.",
      })
    }
  } catch (error) {
    if (signal?.aborted) return
    if (!(error instanceof RunStartError)) {
      console.error("thread chat: the run could not reach Go", error)
    }
    write({
      type: "run_failed",
      run_id: runId,
      error:
        error instanceof RunStartError
          ? error.message
          : "Your primary agent could not be reached. Try again.",
      code: error instanceof RunStartError ? error.code : undefined,
    })
  }
}

/** How long a purge waits for a stopped run to finish writing, before clearing anyway. */
const purgeSettleTimeoutMs = 10_000

/**
 * The primary chat of one thread, named `threadAgentName(org, thread)`: a
 * SQLite-backed AIChatAgent that keeps the visible transcript, relays Go's
 * runs to the page over a resumable WebSocket stream, and never runs a model
 * itself. Go keeps the ADK session, runs, thread lists and artifacts.
 */
export class ThreadChatAgent extends AIChatAgent<ThreadChatEnv> {
  /**
   * Deletes the stored transcript. The Worker calls this over RPC, and only
   * after Go has deleted the thread, so nothing a browser sends can reach
   * it. A run still streaming is stopped and allowed to finish writing
   * first, or its partial answer would land in the emptied transcript. Open
   * sockets are sent the empty conversation.
   */
  async purgeTranscript(): Promise<void> {
    this.resetTurnState()
    await this.waitUntilStable({ timeout: purgeSettleTimeoutMs })
    // Every stored message, including any older than the in-memory window.
    await this.sessions.session().clearMessages()
    await this.persistMessages([])
  }

  // A Go run can call tools with side effects. Never replay or retry one
  // after the object restarts: what the interrupted run stored stays in Go,
  // and the page reconciles with it.
  protected override async onChatRecovery(): Promise<ChatRecoveryOptions> {
    return { continue: false }
  }

  // Every chunk Go streams arrives as its own `delta` part, and the finished
  // `message` after them holds all their text again. The socket still
  // streams every delta; only what is stored leaves out those a finished
  // message has replaced.
  protected override sanitizeMessageForPersistence(
    message: UIMessage
  ): UIMessage {
    return withoutSupersededDeltas(message)
  }

  override async onConnect(connection: Connection, context: ConnectionContext) {
    const { request } = context
    const cookie = request.headers.get(chatRequestHeaders.cookie)
    const organizationId = request.headers.get(chatRequestHeaders.organization)
    const threadId = request.headers.get(chatRequestHeaders.thread)
    if (
      !cookie ||
      !organizationId ||
      !threadId ||
      threadAgentName(organizationId, threadId) !== this.name
    ) {
      connection.close(1008, "Unauthorized")
      return
    }
    connection.setState({
      cookie,
      organizationId,
      threadId,
      origin: request.headers.get("Origin") ?? "",
    } satisfies ChatConnectionState)
  }

  override async onChatMessage(
    _onFinish: Parameters<AIChatAgent["onChatMessage"]>[0],
    options?: OnChatMessageOptions
  ): Promise<Response> {
    const connection = getCurrentAgent().connection?.state as
      ChatConnectionState | null | undefined
    const messages = [...this.messages]
    const stream = createUIMessageStream<ThreadChatMessage>({
      execute: async ({ writer }) => {
        await relayRun({
          apiOrigin: this.env.ORYXA_API_ORIGIN,
          connection,
          messages,
          body: options?.body,
          signal: options?.abortSignal,
          write: (item) => writer.write(itemPart(item)),
        })
      },
    })
    return createUIMessageStreamResponse({ stream })
  }
}
