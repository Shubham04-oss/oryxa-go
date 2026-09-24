import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"
import type { OnChatMessageOptions } from "@cloudflare/ai-chat"
import type { Connection, ConnectionContext } from "agents"
import type { UIMessage } from "ai"
import { runStartError } from "./chat-relay"
import { sessionExpiredCode } from "./lib/threads/chat-messages"
import type { Item } from "./lib/threads/client"
import { MAX_MESSAGE_LENGTH } from "./lib/threads/client"
import {
  SpecialistChatAgent,
  relaySpecialistRun,
  specialistRefusals,
  specialistRunInput,
} from "./specialist-chat-agent"
import type {
  RelaySpecialistRunOptions,
  SpecialistConnectionState,
} from "./specialist-chat-agent"

// The Agents SDK only runs in workerd. As for the thread chat, a stand-in
// base class records what the object asks of it: its state, the purge's
// steps, and the socket messages that reach the SDK's own handler.
const current = vi.hoisted(() => ({
  connection: undefined as { state: unknown } | undefined,
}))

vi.mock("agents", () => ({
  getCurrentAgent: () => ({ connection: current.connection }),
}))

vi.mock("@cloudflare/ai-chat", () => {
  class AIChatAgent {
    name = "org_sim~agt_sales"
    env: unknown
    initialState: unknown = undefined
    stored: unknown = undefined
    messages: Array<unknown> = []
    calls: Array<string> = []
    sdkMessages: Array<unknown> = []
    resetTurnState = vi.fn(() => {
      this.calls.push("resetTurnState")
    })
    waitUntilStable = vi.fn(async () => {
      this.calls.push("waitUntilStable")
      return true
    })
    clearMessages = vi.fn(async () => {
      this.calls.push("clearMessages")
    })
    persistMessages = vi.fn(async () => {
      this.calls.push("persistMessages")
    })
    setState = vi.fn((state: unknown) => {
      this.calls.push(`setState ${JSON.stringify(state)}`)
      this.stored = state
    })
    sessions = { session: () => ({ clearMessages: this.clearMessages }) }
    // The SDK's socket handler, which the object wraps.
    onMessage = vi.fn(async (_connection: unknown, message: unknown) => {
      this.sdkMessages.push(message)
      this.calls.push("sdk onMessage")
    })
    constructor(_ctx: unknown, env: unknown) {
      this.env = env
    }
    get state() {
      return this.stored ?? this.initialState
    }
  }
  return { AIChatAgent }
})

const encoder = new TextEncoder()
const apiOrigin = "http://go.test"
const runsUrl = "http://go.test/v1/organizations/org_sim/agents/agt_sales/runs"

const connection: SpecialistConnectionState = {
  cookie: "oryxa_access=token",
  organizationId: "org_sim",
  agentId: "agt_sales",
  origin: "http://localhost:3003",
}

function sse(item: Item): string {
  return `event: ${item.type}\ndata: ${JSON.stringify(item)}\n\n`
}

function json(body: unknown, status: number): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  })
}

/**
 * Go's answer to a specialist run: the given items, then (unless `close`)
 * the response stays open, as Go keeps it while it writes memory.
 */
function goStream(
  items: Array<Item>,
  {
    close = false,
    thread = "agent_thread_1",
    headers = {},
  }: {
    close?: boolean
    thread?: string | null
    headers?: Record<string, string>
  } = {}
) {
  const state = { cancelled: false }
  const stream = new ReadableStream<Uint8Array>({
    start(controller) {
      controller.enqueue(encoder.encode(": keep-alive\n\n"))
      for (const item of items) controller.enqueue(encoder.encode(sse(item)))
      if (close) controller.close()
    },
    cancel() {
      state.cancelled = true
    },
  })
  return {
    state,
    response: new Response(stream, {
      headers: {
        "Content-Type": "text/event-stream",
        "X-Oryxa-Run": "run_1",
        ...(thread ? { "X-Oryxa-Thread": thread } : {}),
        ...headers,
      },
    }),
  }
}

function userMessage(text: string, id = "user_1"): UIMessage {
  return { id, role: "user", parts: [{ type: "text", text }] }
}

const workResult = {
  status: "completed",
  summary: "Pipeline is up 12% on last quarter.",
  tools_used: ["crm_search"],
  artifacts: [],
  handoffs: [{ target: "finance", request: "Check the forecast" }],
}
const resultText = JSON.stringify(workResult)

const started: Item = { type: "run_started", run_id: "run_1" }
const echoed: Item = {
  type: "message",
  id: "ev_1",
  author: "member",
  text: "Review the pipeline",
}
const jsonDeltas: Item[] = [
  { type: "delta", at: "t1", author: "Sales", text: '{"status":"compl' },
  { type: "delta", at: "t2", author: "Sales", text: 'eted","summary":"Pipe' },
  { type: "delta", at: "t3", author: "Sales", text: 'line is up"}' },
]
const answer: Item = {
  type: "message",
  id: "ev_2",
  author: "Sales",
  text: resultText,
}
const readableAnswer: Item = {
  ...answer,
  text: "Pipeline is up 12% on last quarter.",
  result: workResult as Item["result"],
}
const marker: Item = { type: "delta", at: "t1", author: "Sales", text: "" }
const completed: Item = { type: "run_completed", run_id: "run_1" }

/** Resolves with the promise, or rejects once `ms` pass: a stream that should end promptly must not hang the test. */
function within<T>(promise: Promise<T>, ms = 1000): Promise<T> {
  return Promise.race([
    promise,
    new Promise<T>((_, reject) =>
      setTimeout(() => reject(new Error(`still waiting after ${ms}ms`)), ms)
    ),
  ])
}

/** Starts a turn; `items` and `threads` fill as it goes, and `done` settles when it returns. */
function relay(options: Partial<RelaySpecialistRunOptions> = {}) {
  const items: Array<Item> = []
  const threads: Array<string> = []
  const done = relaySpecialistRun({
    apiOrigin,
    connection,
    threadId: undefined,
    messages: [userMessage("Review the pipeline")],
    body: undefined,
    signal: undefined,
    write: (item) => items.push(item),
    onThread: (id) => threads.push(id),
    ...options,
  })
  return { items, threads, done }
}

function sentBody(call = 0): unknown {
  const [, init] = fetchMock.mock.calls[call] as [URL, RequestInit]
  return JSON.parse(String(init.body))
}

let fetchMock: ReturnType<typeof vi.fn>

beforeEach(() => {
  fetchMock = vi.fn()
  vi.stubGlobal("fetch", fetchMock)
  current.connection = undefined
})

afterEach(() => {
  vi.unstubAllGlobals()
  vi.restoreAllMocks()
})

describe("specialistRunInput", () => {
  it("sends the member's latest message, trimmed, or their answers", () => {
    expect(
      specialistRunInput([userMessage("  Review it ")], undefined, undefined)
    ).toEqual({ message: "Review it" })
    expect(
      specialistRunInput(
        [userMessage(" ")],
        { confirmations: [{ id: "call_1", confirmed: true, extra: 1 }] },
        "agent_thread_1"
      )
    ).toEqual({ confirmations: [{ id: "call_1", confirmed: true }] })
  })

  it("refuses what Go would refuse only after allocating a conversation", () => {
    const tooLong = "a".repeat(MAX_MESSAGE_LENGTH + 1)
    expect(() =>
      specialistRunInput([userMessage(tooLong)], undefined, undefined)
    ).toThrow(
      expect.objectContaining({
        message: `A message holds at most ${MAX_MESSAGE_LENGTH} characters.`,
        code: "invalid_request",
      })
    )
    expect(() =>
      specialistRunInput(
        [userMessage(" ")],
        { confirmations: [{ id: "call_1", confirmed: true }] },
        undefined
      )
    ).toThrow(
      expect.objectContaining({
        message: "Nothing in this conversation is waiting for an answer.",
        code: "invalid_request",
      })
    )
  })

  it("refuses a turn with nothing to send, or malformed answers", () => {
    expect(() => specialistRunInput([], undefined, undefined)).toThrow(
      "Send a message to start a run."
    )
    expect(() =>
      specialistRunInput([userMessage("Hi")], { confirmations: [] }, "t")
    ).toThrow("Invalid confirmation answers.")
  })
})

describe("the specialist chat's words for Go's refusals", () => {
  it("names the specialist, and keeps Go's code", async () => {
    const cases: Array<[number, string, string]> = [
      [
        409,
        "agent_disabled",
        "The specialist is paused. Turn it back on to continue.",
      ],
      [
        409,
        "run_in_progress",
        "The specialist is still answering in this conversation.",
      ],
      [
        409,
        "confirmation_pending",
        "Answer the confirmation above to continue.",
      ],
      [503, "agents_unavailable", "Specialists aren't available right now."],
      [
        404,
        "not_found",
        "The specialist or this conversation no longer exists.",
      ],
      [
        429,
        "rate_limited",
        "Too many requests at once. Wait a moment and try again.",
      ],
    ]
    for (const [status, code, words] of cases) {
      const error = await runStartError(
        json({ error: { code, message: "Go's own words" } }, status),
        specialistRefusals
      )
      expect([error.message, error.code]).toEqual([words, code])
    }
  })

  it("keeps Go's words for an invalid request, and falls back to the status for any other code", async () => {
    const invalid = await runStartError(
      json(
        {
          error: {
            code: "invalid_request",
            message: "nothing in this thread is waiting for an answer",
          },
        },
        400
      ),
      specialistRefusals
    )
    expect(invalid.message).toBe(
      "Nothing in this thread is waiting for an answer."
    )
    for (const code of ["internal", "constructor", "toString"]) {
      const error = await runStartError(
        json({ error: { code, message: "x" } }, 500),
        specialistRefusals
      )
      expect([error.message, error.code]).toEqual([
        "The agent could not start (500).",
        code,
      ])
    }
  })
})

describe("relaySpecialistRun", () => {
  it("starts a new conversation as the member, and learns its id before any item", async () => {
    const go = goStream([started, echoed, ...jsonDeltas, answer, completed], {
      close: true,
    })
    fetchMock.mockResolvedValue(go.response)
    const seen: Array<string> = []
    const { items, done } = relay({
      write: (item) => seen.push(item.type),
      onThread: (id) => seen.push(`thread:${id}`),
    })
    await within(done)

    expect(items).toEqual([])
    expect(seen).toEqual([
      "thread:agent_thread_1",
      "run_started",
      "delta",
      "message",
      "run_completed",
    ])
    const [url, init] = fetchMock.mock.calls[0] as [URL, RequestInit]
    expect(String(url)).toBe(runsUrl)
    expect(init.method).toBe("POST")
    expect(init.headers).toEqual({
      "Content-Type": "application/json",
      Cookie: "oryxa_access=token",
      Origin: "http://localhost:3003",
    })
    expect(sentBody()).toEqual({ message: "Review the pipeline" })
  })

  it("streams a progress marker instead of raw JSON, then the readable result", async () => {
    const go = goStream([started, echoed, ...jsonDeltas, answer, completed])
    fetchMock.mockResolvedValue(go.response)
    const { items, done } = relay()
    await within(done)
    expect(items).toEqual([started, marker, readableAnswer, completed])
    expect(go.state.cancelled).toBe(true)
  })

  it("continues its conversation, and reports no new one", async () => {
    fetchMock.mockResolvedValue(goStream([started, answer, completed]).response)
    const { threads, done } = relay({ threadId: "agent_thread_1" })
    await within(done)
    expect(sentBody()).toEqual({
      thread_id: "agent_thread_1",
      message: "Review the pipeline",
    })
    expect(threads).toEqual([])
  })

  it("answers confirmations in its conversation, echoing them once Go accepts", async () => {
    fetchMock.mockResolvedValue(goStream([started, answer, completed]).response)
    const { items, done } = relay({
      threadId: "agent_thread_1",
      messages: [userMessage(" ")],
      body: {
        confirmations: [
          { id: "call_1", confirmed: true },
          { id: "call_2", confirmed: false },
        ],
      },
    })
    await within(done)
    expect(sentBody()).toEqual({
      thread_id: "agent_thread_1",
      confirmations: [
        { id: "call_1", confirmed: true },
        { id: "call_2", confirmed: false },
      ],
    })
    expect(items.slice(0, 2)).toEqual([
      { type: "confirmation", call_id: "call_1", status: "approved" },
      { type: "confirmation", call_id: "call_2", status: "declined" },
    ])
  })

  it("never calls Go for input that would leave an empty conversation behind", async () => {
    for (const options of [
      { messages: [userMessage("a".repeat(MAX_MESSAGE_LENGTH + 1))] },
      {
        messages: [userMessage(" ")],
        body: { confirmations: [{ id: "call_1", confirmed: true }] },
      },
      { messages: [] },
    ]) {
      const { items, threads, done } = relay(options)
      await within(done)
      expect(items).toEqual([expect.objectContaining({ type: "run_failed" })])
      expect(threads).toEqual([])
    }
    expect(fetchMock).not.toHaveBeenCalled()
  })

  it("shows a message that is not a work result as it is", async () => {
    const plain: Item = {
      type: "message",
      id: "ev_3",
      author: "Sales",
      text: "Here are three tips.",
    }
    fetchMock.mockResolvedValue(
      goStream([
        started,
        { type: "delta", author: "Sales", text: "Here are" },
        { type: "delta", author: "Sales", text: " three tips." },
        plain,
        completed,
      ]).response
    )
    const { items, done } = relay()
    await within(done)
    expect(items).toEqual([
      started,
      { type: "delta", author: "Sales", text: "" },
      plain,
      completed,
    ])
  })

  it("reports Go's refusals in the specialist's words, before anything ran", async () => {
    fetchMock.mockResolvedValueOnce(
      json({ error: { code: "unauthorized", message: "expired" } }, 401)
    )
    fetchMock.mockResolvedValueOnce(
      json(
        {
          error: {
            code: "agent_disabled",
            message: "managed agent is disabled",
          },
        },
        409
      )
    )
    const expired = relay()
    await within(expired.done)
    const paused = relay()
    await within(paused.done)
    expect([...expired.items, ...paused.items]).toEqual([
      {
        type: "run_failed",
        error: "Your session has ended. Sign in again to continue.",
        code: sessionExpiredCode,
      },
      {
        type: "run_failed",
        error: "The specialist is paused. Turn it back on to continue.",
        code: "agent_disabled",
      },
    ])
    expect([...expired.threads, ...paused.threads]).toEqual([])
  })

  it("asks the page for a new socket when this one holds no cookie", async () => {
    const { items, done } = relay({ connection: undefined })
    await within(done)
    expect(fetchMock).not.toHaveBeenCalled()
    expect(items).toEqual([
      expect.objectContaining({ type: "run_failed", code: sessionExpiredCode }),
    ])
  })

  it("ends a stream that stopped early with a run_failed", async () => {
    fetchMock.mockResolvedValue(
      goStream([started, ...jsonDeltas], { close: true }).response
    )
    const { items, done } = relay()
    await within(done)
    expect(items).toEqual([
      started,
      marker,
      {
        type: "run_failed",
        run_id: "run_1",
        error: "The specialist's answer was cut off before it finished.",
      },
    ])
  })

  it("reports a Go it cannot reach in plain words", async () => {
    const logged = vi.spyOn(console, "error").mockImplementation(() => {})
    fetchMock.mockRejectedValue(new TypeError("fetch failed"))
    const { items, done } = relay()
    await within(done)
    expect(items).toEqual([
      {
        type: "run_failed",
        error: "The specialist could not be reached. Try again.",
      },
    ])
    expect(logged).toHaveBeenCalledWith(
      "specialist chat: the run could not reach Go",
      expect.any(TypeError)
    )
  })

  it("keeps no new conversation for a turn already stopped, and writes nothing more", async () => {
    const controller = new AbortController()
    let answerGo!: (response: Response) => void
    fetchMock.mockImplementation(
      () =>
        new Promise<Response>((resolve) => {
          answerGo = resolve
        })
    )
    const { items, threads, done } = relay({ signal: controller.signal })
    await vi.waitFor(() => expect(answerGo).toBeDefined())
    controller.abort()
    answerGo(goStream([started, answer, completed]).response)
    await within(done)
    expect(threads).toEqual([])
    expect(items).toEqual([])
  })
})

/** The protected hooks under test. */
class TestAgent extends SpecialistChatAgent {
  recover() {
    return this.onChatRecovery()
  }

  sanitize(message: UIMessage) {
    return this.sanitizeMessageForPersistence(message)
  }
}

function makeAgent(): TestAgent {
  return new TestAgent({} as DurableObjectState, {
    ORYXA_API_ORIGIN: apiOrigin,
  })
}

/** What the stand-in base class recorded. */
function records(agent: SpecialistChatAgent) {
  return agent as unknown as {
    calls: Array<string>
    sdkMessages: Array<unknown>
    stored: unknown
    waitUntilStable: ReturnType<typeof vi.fn>
    persistMessages: ReturnType<typeof vi.fn>
    setState: ReturnType<typeof vi.fn>
  }
}

function socket() {
  let state: unknown = null
  return {
    close: vi.fn(),
    setState: vi.fn((next: unknown) => {
      state = next
    }),
    get state() {
      return state
    },
  }
}

function upgrade(headers: Record<string, string>): ConnectionContext {
  return {
    request: new Request("http://localhost:3003/agents/x", { headers }),
  }
}

const routedHeaders = {
  "X-Oryxa-Cookie": "oryxa_access=token",
  "X-Oryxa-Organization": "org_sim",
  "X-Oryxa-Agent": "agt_sales",
  Origin: "http://localhost:3003",
}

/** The `data-oryxa` items of a UI message stream response. */
async function streamedItems(response: Response): Promise<Array<Item>> {
  const text = await within(response.text())
  return text
    .split("\n")
    .filter((line) => line.startsWith("data: ") && line !== "data: [DONE]")
    .map((line) => JSON.parse(line.slice("data: ".length)))
    .filter((chunk: { type: string }) => chunk.type === "data-oryxa")
    .map((chunk: { data: Item }) => chunk.data)
}

function turn(agent: SpecialistChatAgent) {
  return agent.onChatMessage(async () => {}, {
    requestId: "request_1",
    abortSignal: new AbortController().signal,
  } satisfies OnChatMessageOptions)
}

describe("SpecialistChatAgent", () => {
  it("starts with no conversation, and never replays a run after a restart", async () => {
    const agent = makeAgent()
    expect(agent.state).toEqual({ threadId: null })
    expect(await agent.recover()).toEqual({ continue: false })
  })

  it("stores an answer without the progress marker that stood in for it", () => {
    const message = {
      id: "assistant_1",
      role: "assistant" as const,
      parts: [started, marker, readableAnswer, completed].map((data) => ({
        type: "data-oryxa" as const,
        data,
      })),
    }
    expect(makeAgent().sanitize(message).parts).toEqual(
      [started, readableAnswer, completed].map((data) => ({
        type: "data-oryxa",
        data,
      }))
    )
  })

  it("keeps the cookie, specialist and origin the Worker routed the socket with", async () => {
    const agent = makeAgent()
    const connected = socket()
    await agent.onConnect(
      connected as unknown as Connection,
      upgrade(routedHeaders)
    )
    expect(connected.close).not.toHaveBeenCalled()
    expect(connected.state).toEqual(connection)
  })

  it("closes a socket the Worker did not route, or routed for another specialist", async () => {
    const agent = makeAgent()
    const { "X-Oryxa-Cookie": _cookie, ...withoutCookie } = routedHeaders
    for (const headers of [
      withoutCookie,
      { ...routedHeaders, "X-Oryxa-Agent": "agt_other" },
      { ...routedHeaders, "X-Oryxa-Organization": "org_other" },
    ]) {
      const connected = socket()
      await agent.onConnect(
        connected as unknown as Connection,
        upgrade(headers)
      )
      expect(connected.close).toHaveBeenCalledWith(1008, "Unauthorized")
      expect(connected.setState).not.toHaveBeenCalled()
    }
  })

  it("keeps the conversation Go allocates on the first turn, and continues it on the next", async () => {
    const agent = makeAgent()
    agent.messages = [userMessage("Review the pipeline")]
    current.connection = { state: connection }
    fetchMock.mockResolvedValueOnce(
      goStream([started, echoed, ...jsonDeltas, answer, completed]).response
    )
    expect(await streamedItems(await turn(agent))).toEqual([
      started,
      marker,
      readableAnswer,
      completed,
    ])
    expect(agent.state).toEqual({ threadId: "agent_thread_1" })
    expect(sentBody(0)).toEqual({ message: "Review the pipeline" })

    agent.messages = [
      ...agent.messages,
      { id: "assistant_1", role: "assistant", parts: [] },
      userMessage("And last year?", "user_2"),
    ]
    fetchMock.mockResolvedValueOnce(
      goStream([started, answer, completed]).response
    )
    await streamedItems(await turn(agent))
    expect(sentBody(1)).toEqual({
      thread_id: "agent_thread_1",
      message: "And last year?",
    })
    expect(records(agent).setState).toHaveBeenCalledTimes(1)
  })

  it("streams a refusal as a run_failed part", async () => {
    const agent = makeAgent()
    agent.messages = [userMessage("Review the pipeline")]
    current.connection = { state: connection }
    fetchMock.mockResolvedValue(new Response(null, { status: 401 }))
    expect(await streamedItems(await turn(agent))).toEqual([
      {
        type: "run_failed",
        error: "Your session has ended. Sign in again to continue.",
        code: sessionExpiredCode,
      },
    ])
    expect(agent.state).toEqual({ threadId: null })
  })

  it("starts over on a purge: stops the turn, lets it settle, clears, then forgets the conversation", async () => {
    const agent = makeAgent()
    agent.setState({ threadId: "agent_thread_1" })
    const recorded = records(agent)
    recorded.calls.length = 0
    await agent.purgeTranscript()
    expect(recorded.calls).toEqual([
      "resetTurnState",
      "waitUntilStable",
      "clearMessages",
      "persistMessages",
      'setState {"threadId":null}',
    ])
    expect(recorded.waitUntilStable).toHaveBeenCalledWith({ timeout: 10_000 })
    expect(recorded.persistMessages).toHaveBeenCalledWith([])
    expect(agent.state).toEqual({ threadId: null })
  })

  it("starts over on useAgentChat's clearHistory() too, before the SDK clears the transcript", async () => {
    const agent = makeAgent()
    agent.setState({ threadId: "agent_thread_1" })
    const recorded = records(agent)
    const conn = socket() as unknown as Connection
    const clear = JSON.stringify({ type: "cf_agent_chat_clear" })

    await agent.onMessage(conn, JSON.stringify({ type: "cf_agent_chat_x" }))
    expect(agent.state).toEqual({ threadId: "agent_thread_1" })

    await agent.onMessage(conn, clear)
    expect(agent.state).toEqual({ threadId: null })
    expect(recorded.sdkMessages.at(-1)).toBe(clear)
    expect(recorded.calls.slice(-2)).toEqual([
      'setState {"threadId":null}',
      "sdk onMessage",
    ])
  })

  it("lets only itself choose the conversation", () => {
    const agent = makeAgent()
    const page = socket() as unknown as Connection
    expect(() =>
      agent.validateStateChange({ threadId: "agent_thread_9" }, page)
    ).toThrow()
    expect(() =>
      agent.validateStateChange({ threadId: "agent_thread_9" }, "server")
    ).not.toThrow()
  })
})
