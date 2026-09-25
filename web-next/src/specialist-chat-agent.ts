import { AIChatAgent } from "@cloudflare/ai-chat"
import type {
  ChatRecoveryOptions,
  OnChatMessageOptions,
} from "@cloudflare/ai-chat"
import { getCurrentAgent } from "agents"
import type { Connection, ConnectionContext, WSMessage } from "agents"
import { createUIMessageStream, createUIMessageStreamResponse } from "ai"
import type { UIMessage } from "ai"
import {
  RunStartError,
  inputFromMessages,
  relayGoRun,
  sentence,
} from "./chat-relay"
import type { RefusalWords, RelayWords } from "./chat-relay"
import {
  readableSpecialistStream,
  specialistAgentName,
  withoutProgressMarkers,
} from "./lib/specialists/chat-messages"
import type { SpecialistChatState } from "./lib/specialists/chat-messages"
import { itemPart, withoutSupersededDeltas } from "./lib/threads/chat-messages"
import type { ThreadChatMessage } from "./lib/threads/chat-messages"
import { runRequestBody } from "./lib/threads/client"
import type { Item, RunInput } from "./lib/threads/client"
import type { ThreadChatEnv } from "./thread-chat-agent"

/**
 * The headers the Worker sets on every request it routes to the object,
 * replacing any `X-Oryxa-*` header the browser sent, once Go has said the
 * specialist is the member's own.
 */
export const specialistRequestHeaders = {
  cookie: "X-Oryxa-Cookie",
  organization: "X-Oryxa-Organization",
  agent: "X-Oryxa-Agent",
} as const

/**
 * What a socket keeps from its upgrade request: every run it starts goes to
 * Go as the member, with this cookie and origin. A socket never learns a
 * newer cookie; the page reopens it after refreshing the session.
 */
export type SpecialistConnectionState = {
  cookie: string
  organizationId: string
  agentId: string
  origin: string
}

/** The specialist chat's words for Go's refusals. */
export const specialistRefusals: RefusalWords = {
  run_in_progress: "The specialist is still answering in this conversation.",
  confirmation_pending: "Answer the confirmation above to continue.",
  agent_disabled: "The specialist is paused. Turn it back on to continue.",
  agents_unavailable: "Specialists aren't available right now.",
  not_found: "The specialist or this conversation no longer exists.",
  rate_limited: "Too many requests at once. Wait a moment and try again.",
}

const specialistWords: RelayWords = {
  refusals: specialistRefusals,
  cutOff: "The specialist's answer was cut off before it finished.",
  unreachable: "The specialist could not be reached. Try again.",
}

/**
 * What a turn sends Go, checked as Go checks it before anything is sent.
 * Here that matters: with no `thread_id`, Go allocates the conversation
 * before it reads the input, so input it would refuse would leave an empty
 * conversation behind. Answers need a conversation that asked for them.
 * Throws `RunStartError` with Go's words (`invalid_request`) for what it
 * would refuse, and as `inputFromMessages` does when there is nothing to
 * send.
 */
export function specialistRunInput(
  messages: readonly UIMessage[],
  body: Record<string, unknown> | undefined,
  threadId: string | undefined
): RunInput {
  let input: RunInput
  try {
    input = runRequestBody(inputFromMessages(messages, body))
  } catch (error) {
    if (error instanceof RunStartError) throw error
    // runRequestBody refuses in Go's words, which say what to change.
    throw new RunStartError(
      sentence(error instanceof Error ? error.message : String(error)),
      "invalid_request"
    )
  }
  if ("confirmations" in input && !threadId) {
    throw new RunStartError(
      "Nothing in this conversation is waiting for an answer.",
      "invalid_request"
    )
  }
  return input
}

export type RelaySpecialistRunOptions = {
  /** Go's origin: `ThreadChatEnv.ORYXA_API_ORIGIN`. */
  apiOrigin: string
  /** The socket's state from `onConnect`; missing when it was never set. */
  connection: SpecialistConnectionState | null | undefined
  /** The Go conversation to continue; none starts a new one. */
  threadId: string | undefined
  /** The transcript as the turn started, the member's new message last. */
  messages: readonly UIMessage[]
  /** The chat request's custom body: `{ confirmations }` for answers. */
  body: Record<string, unknown> | undefined
  /** The turn's abort signal: the member stopped, or the chat is starting over. */
  signal: AbortSignal | undefined
  /** Receives every item for the page, in order. */
  write: (item: Item) => void
  /**
   * Called once Go accepts the run, before any item, with the conversation
   * it names in `X-Oryxa-Thread` when that differs from `threadId`: a new
   * one. Never called for a turn already aborted.
   */
  onThread: (threadId: string) => void
}

/**
 * One chat turn with a specialist: POSTs `…/agents/{agent}/runs` as the
 * member with `{ thread_id?, message | confirmations }` and writes what it
 * streams, as `relayGoRun` does for a thread (refusals, echoed answers, a
 * stream that stops early). What the page is shown differs in one way: a
 * specialist answers with a JSON work result, so its deltas become a
 * progress marker and its finished message the readable result
 * (`readableSpecialistStream`).
 */
export async function relaySpecialistRun({
  apiOrigin,
  connection,
  threadId,
  messages,
  body,
  signal,
  write,
  onThread,
}: RelaySpecialistRunOptions): Promise<void> {
  await relayGoRun({
    connection,
    words: specialistWords,
    label: "specialist chat",
    signal,
    write,
    request: ({ organizationId, agentId }) => {
      const input = specialistRunInput(messages, body, threadId)
      const path = `/v1/organizations/${encodeURIComponent(organizationId)}/agents/${encodeURIComponent(agentId)}/runs`
      return {
        url: new URL(path, apiOrigin),
        body: threadId ? { thread_id: threadId, ...input } : input,
        input,
      }
    },
    onAccepted: (response) => {
      const accepted = response.headers.get("X-Oryxa-Thread")
      // A purge that stopped this turn has already let go of the old
      // conversation, and must not be handed the new one.
      if (accepted && accepted !== threadId && !signal?.aborted) {
        onThread(accepted)
      }
    },
    present: readableSpecialistStream(),
  })
}

/** How long a purge waits for a stopped run to finish writing, before clearing anyway. */
const purgeSettleTimeoutMs = 10_000

const chatClearType = "cf_agent_chat_clear"

/** Whether a socket message is useAgentChat's `clearHistory()`. */
function isChatClear(message: WSMessage): boolean {
  if (typeof message !== "string" || !message.includes(chatClearType)) {
    return false
  }
  try {
    const parsed: { type?: unknown } = JSON.parse(message)
    return parsed.type === chatClearType
  } catch {
    return false
  }
}

/**
 * The member's chat with one of their own specialists: a SQLite-backed
 * AIChatAgent that keeps the visible transcript, relays Go's runs to the page
 * over a resumable WebSocket stream, and never runs a model itself. Go keeps
 * the ADK session, runs and the specialist's conversation list.
 *
 * It is named `specialistAgentName(org, agent)`, `{organization}~{agent}`,
 * one per specialist, and not after a conversation, because Go offers no
 * specialist conversation before its first run: there is no route that
 * creates one, and `POST …/agents/{agent}/runs` with no `thread_id` allocates
 * it inside the run (managedagents.Service.startRun) and names it only in
 * the answer's `X-Oryxa-Thread`. A page cannot name an object after a
 * conversation that exists only once a message has gone through that object.
 * So the object holds one conversation at a time, the specialist chat's
 * current one: it learns the id from the first run's `X-Oryxa-Thread`,
 * keeps it in its state (`SpecialistChatState`, synced to every socket, so
 * the page can read Go's copy with `getSpecialistThread`) and sends it as
 * `thread_id` on every later run. Starting over (the Worker's DELETE, or
 * useAgentChat's `clearHistory()`) clears the transcript and forgets the id,
 * so the next message starts a new conversation. Go keeps the old one: it
 * has no route that deletes a specialist conversation, so nothing here can.
 *
 * Only the specialist's owner reaches it: the Worker routes a socket only
 * once Go answers `GET …/agents/{agent}` for the member's cookie, and Go
 * loads an agent only for its owner.
 */
export class SpecialistChatAgent extends AIChatAgent<
  ThreadChatEnv,
  SpecialistChatState
> {
  override initialState: SpecialistChatState = { threadId: null }

  constructor(ctx: DurableObjectState, env: ThreadChatEnv) {
    super(ctx, env)
    // useAgentChat's clearHistory() empties the transcript inside the SDK,
    // which handles it before any hook of this class runs. It has to start
    // a new conversation too, or the next message would continue one the
    // member can no longer see. The SDK's own turn reset follows at once.
    const handle = this.onMessage.bind(this)
    this.onMessage = (connection, message) => {
      if (isChatClear(message)) this.forgetConversation()
      return handle(connection, message)
    }
  }

  /**
   * Starts the chat over: deletes the stored transcript and forgets the Go
   * conversation, so the next message starts a new one; Go keeps the old
   * one. The Worker calls this over RPC, for the page's DELETE, once Go has
   * said the specialist is the member's own. A run still streaming is
   * stopped and allowed to finish writing first, or its partial answer would
   * land in the emptied transcript. Open sockets are sent the empty
   * conversation and the new state.
   */
  async purgeTranscript(): Promise<void> {
    this.resetTurnState()
    await this.waitUntilStable({ timeout: purgeSettleTimeoutMs })
    // Every stored message, including any older than the in-memory window.
    await this.sessions.session().clearMessages()
    await this.persistMessages([])
    this.forgetConversation()
  }

  /** Continues `threadId` from now on. */
  private keepConversation(threadId: string): void {
    if (this.state.threadId !== threadId) this.setState({ threadId })
  }

  /** Lets the next message start a new conversation. */
  private forgetConversation(): void {
    if (this.state.threadId !== null) this.setState({ threadId: null })
  }

  // Which Go conversation the chat continues is the object's to decide: a
  // page could otherwise point it at another of the member's conversations
  // behind the transcript's back.
  override validateStateChange(
    _nextState: SpecialistChatState,
    source: Connection | "server"
  ): void {
    if (source !== "server") {
      throw new Error("Only the chat itself changes its conversation.")
    }
  }

  // A Go run can call tools with side effects. Never replay or retry one
  // after the object restarts: what the interrupted run stored stays in Go,
  // and the page reconciles with it.
  protected override async onChatRecovery(): Promise<ChatRecoveryOptions> {
    return { continue: false }
  }

  // What streamed as progress markers carries nothing once the run is over:
  // a finished answer replaces its marker, and a failed run says so itself.
  protected override sanitizeMessageForPersistence(
    message: UIMessage
  ): UIMessage {
    return withoutProgressMarkers(withoutSupersededDeltas(message))
  }

  override async onConnect(connection: Connection, context: ConnectionContext) {
    const { request } = context
    const cookie = request.headers.get(specialistRequestHeaders.cookie)
    const organizationId = request.headers.get(
      specialistRequestHeaders.organization
    )
    const agentId = request.headers.get(specialistRequestHeaders.agent)
    if (
      !cookie ||
      !organizationId ||
      !agentId ||
      specialistAgentName(organizationId, agentId) !== this.name
    ) {
      connection.close(1008, "Unauthorized")
      return
    }
    connection.setState({
      cookie,
      organizationId,
      agentId,
      origin: request.headers.get("Origin") ?? "",
    } satisfies SpecialistConnectionState)
  }

  override async onChatMessage(
    _onFinish: Parameters<AIChatAgent["onChatMessage"]>[0],
    options?: OnChatMessageOptions
  ): Promise<Response> {
    const connection = getCurrentAgent().connection?.state as
      SpecialistConnectionState | null | undefined
    const messages = [...this.messages]
    // Read as the turn starts: turns run one at a time, so a first run's
    // new conversation is known before the next turn reads it.
    const threadId = this.state.threadId ?? undefined
    const stream = createUIMessageStream<ThreadChatMessage>({
      execute: async ({ writer }) => {
        await relaySpecialistRun({
          apiOrigin: this.env.ORYXA_API_ORIGIN,
          connection,
          threadId,
          messages,
          body: options?.body,
          signal: options?.abortSignal,
          write: (item) => writer.write(itemPart(item)),
          onThread: (id) => this.keepConversation(id),
        })
      },
    })
    return createUIMessageStreamResponse({ stream })
  }
}
