import { AIChatAgent } from "@cloudflare/ai-chat"
import type {
  ChatRecoveryOptions,
  OnChatMessageOptions,
} from "@cloudflare/ai-chat"
import { getCurrentAgent } from "agents"
import type { Connection, ConnectionContext } from "agents"
import { createUIMessageStream, createUIMessageStreamResponse } from "ai"
import type { UIMessage } from "ai"
import { inputFromMessages, primaryRefusals, relayGoRun } from "./chat-relay"
import type { RelayWords } from "./chat-relay"
import {
  itemPart,
  threadAgentName,
  withoutSupersededDeltas,
} from "./lib/threads/chat-messages"
import type { ThreadChatMessage } from "./lib/threads/chat-messages"
import type { Item } from "./lib/threads/client"

// A turn's input and Go's refusals are the same for every chat object, and
// live in ./chat-relay; they are exported from here too, where they began.
export {
  RunStartError,
  confirmationInput,
  inputFromMessages,
  runStartError,
} from "./chat-relay"

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

/** The primary chat's words for a run that did not finish. */
const primaryWords: RelayWords = {
  refusals: primaryRefusals,
  cutOff: "Your primary agent's answer was cut off before it finished.",
  unreachable: "Your primary agent could not be reached. Try again.",
}

/**
 * One chat turn: POSTs the thread's run as the member and writes what it
 * streams (`relayGoRun`, which says how refusals, confirmations and a
 * stream that stops early reach the page).
 */
export async function relayRun({
  apiOrigin,
  connection,
  messages,
  body,
  signal,
  write,
}: RelayRunOptions): Promise<void> {
  await relayGoRun({
    connection,
    words: primaryWords,
    label: "thread chat",
    signal,
    write,
    request: ({ organizationId, threadId }) => {
      const input = inputFromMessages(messages, body)
      const path = `/v1/organizations/${encodeURIComponent(organizationId)}/threads/${encodeURIComponent(threadId)}/runs`
      return { url: new URL(path, apiOrigin), body: input, input }
    },
  })
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
