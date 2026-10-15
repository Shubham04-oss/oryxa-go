import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"
import type { OnChatMessageOptions } from "@cloudflare/ai-chat"
import type { Connection, ConnectionContext } from "agents"
import type { UIMessage } from "ai"
import { sessionExpiredCode } from "./lib/threads/chat-messages"
import type { Item } from "./lib/threads/client"
import {
  RunStartError,
  ThreadChatAgent,
  confirmationInput,
  inputFromMessages,
  relayRun,
  runStartError,
} from "./thread-chat-agent"
import type { ChatConnectionState, RelayRunOptions } from "./thread-chat-agent"

// The Agents SDK only runs in workerd. These tests keep what the object does
// itself: a stand-in base class records what it is asked, and the socket the
// current turn belongs to is whatever `current.connection` holds.
const current = vi.hoisted(() => ({
  connection: undefined as { state: unknown } | undefined,
}))

vi.mock("agents", () => ({
  getCurrentAgent: () => ({ connection: current.connection }),
}))

vi.mock("@cloudflare/ai-chat", () => {
  class AIChatAgent {
    name = "org_sim~thread_1"
    env: unknown
    messages: Array<unknown> = []
    calls: Array<string> = []
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
    sessions = { session: () => ({ clearMessages: this.clearMessages }) }
    constructor(_ctx: unknown, env: unknown) {
      this.env = env
    }
  }
  return { AIChatAgent }
})

const encoder = new TextEncoder()
const apiOrigin = "http://go.test"
const runsUrl = "http://go.test/v1/organizations/org_sim/threads/thread_1/runs"

const connection: ChatConnectionState = {
  cookie: "oryxa_access=token",
  organizationId: "org_sim",
  threadId: "thread_1",
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
 * Go's answer to a run: the given items, then (unless `close`) the response
 * stays open, as Go keeps it while it writes the conversation to memory.
 */
function goStream(
  items: Array<Item>,
  {
    close = false,
    headers = {},
  }: { close?: boolean; headers?: Record<string, string> } = {}
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
      headers: { "Content-Type": "text/event-stream", ...headers },
    }),
  }
}

function userMessage(text: string, id = "user_1"): UIMessage {
  return { id, role: "user", parts: [{ type: "text", text }] }
}

const started: Item = { type: "run_started", run_id: "run_1" }
const delta: Item = { type: "delta", author: "Krude", text: "Hel" }
const echoed: Item = {
  type: "message",
  id: "ev_1",
  author: "member",
  text: "Hello",
}
const answer: Item = {
  type: "message",
  id: "ev_2",
  author: "Krude",
  text: "Hello there",
}
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

/** Starts a turn; `items` fills as it writes, and `done` settles when it returns. */
function relay(options: Partial<RelayRunOptions> = {}) {
  const items: Array<Item> = []
  const done = relayRun({
    apiOrigin,
    connection,
    messages: [userMessage("Hello")],
    body: undefined,
    signal: undefined,
    write: (item) => items.push(item),
    ...options,
  })
  return { items, done }
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

describe("confirmationInput", () => {
  it("keeps only each answer's id and decision", () => {
    expect(
      confirmationInput([
        { id: "call_1", confirmed: true, note: "extra" },
        { id: "call_2", confirmed: false },
      ])
    ).toEqual([
      { id: "call_1", confirmed: true },
      { id: "call_2", confirmed: false },
    ])
  })

  it("refuses anything but a non-empty list of well-formed answers", () => {
    for (const value of [
      undefined,
      null,
      "call_1",
      {},
      [],
      [null],
      [{ id: "call_1" }],
      [{ id: "", confirmed: true }],
      [{ id: 7, confirmed: true }],
      [{ id: "call_1", confirmed: "yes" }],
      [{ id: "call_1", confirmed: true }, "call_2"],
    ]) {
      expect(confirmationInput(value)).toBeUndefined()
    }
  })
})

describe("inputFromMessages", () => {
  it("sends the member's latest message, trimmed", () => {
    const messages: Array<UIMessage> = [
      userMessage("First", "user_0"),
      { id: "assistant_0", role: "assistant", parts: [] },
      {
        id: "user_1",
        role: "user",
        parts: [
          { type: "text", text: "  Hello " },
          { type: "text", text: "again  " },
        ],
      },
    ]
    expect(inputFromMessages(messages, undefined)).toEqual({
      message: "Hello again",
    })
  })

  it("sends answers from the body instead of the placeholder message", () => {
    const messages = [userMessage(" ")]
    expect(
      inputFromMessages(messages, {
        confirmations: [{ id: "call_1", confirmed: false }],
      })
    ).toEqual({ confirmations: [{ id: "call_1", confirmed: false }] })
  })

  it("refuses malformed answers rather than sending the message", () => {
    expect(() =>
      inputFromMessages([userMessage("Hello")], { confirmations: [] })
    ).toThrow(new RunStartError("Invalid confirmation answers."))
  })

  it("refuses a turn with no words to send", () => {
    for (const messages of [
      [],
      [userMessage("   ")],
      [{ id: "a", role: "assistant", parts: [] } satisfies UIMessage],
    ]) {
      expect(() => inputFromMessages(messages, undefined)).toThrow(
        "Send a message to start a run."
      )
    }
  })
})

describe("runStartError", () => {
  it("turns a 401 into session_expired, whatever the body", async () => {
    const error = await runStartError(new Response("nope", { status: 401 }))
    expect(error).toBeInstanceOf(RunStartError)
    expect(error.code).toBe(sessionExpiredCode)
    expect(error.message).toBe(
      "Your session has ended. Sign in again to continue."
    )
  })

  it("puts Go's refusals in words, keeping Go's code", async () => {
    const cases: Array<[number, string, string]> = [
      [
        409,
        "run_in_progress",
        "Your primary agent is still answering in this conversation.",
      ],
      [
        409,
        "confirmation_pending",
        "Answer the confirmation above to continue.",
      ],
      [
        503,
        "agents_unavailable",
        "Your primary agent isn't available right now.",
      ],
      [404, "not_found", "This conversation no longer exists."],
      [
        429,
        "rate_limited",
        "Too many requests at once. Wait a moment and try again.",
      ],
    ]
    for (const [status, code, words] of cases) {
      const error = await runStartError(
        json({ error: { code, message: "Go's own words" } }, status)
      )
      expect([error.message, error.code]).toEqual([words, code])
    }
  })

  it("passes on Go's words for an invalid request, as a sentence", async () => {
    const error = await runStartError(
      json(
        {
          error: {
            code: "invalid_request",
            message: "a message holds at most 32000 characters",
          },
        },
        400
      )
    )
    expect(error.message).toBe("A message holds at most 32000 characters.")
    expect(error.code).toBe("invalid_request")
  })

  it("falls back to the status for anything else", async () => {
    const gateway = await runStartError(
      new Response("<html>Bad gateway</html>", { status: 502 })
    )
    expect([gateway.message, gateway.code]).toEqual([
      "The agent could not start (502).",
      undefined,
    ])
    const internal = await runStartError(
      json(
        { error: { code: "internal", message: "internal server error" } },
        500
      )
    )
    expect([internal.message, internal.code]).toEqual([
      "The agent could not start (500).",
      "internal",
    ])
  })
})

describe("relayRun", () => {
  it("posts the run as the member and relays Go's items, leaving out the member's echo", async () => {
    const go = goStream([started, echoed, delta, answer, completed], {
      close: true,
    })
    fetchMock.mockResolvedValue(go.response)
    const { items, done } = relay()
    await within(done)

    expect(fetchMock).toHaveBeenCalledTimes(1)
    const [url, init] = fetchMock.mock.calls[0] as [URL, RequestInit]
    expect(String(url)).toBe(runsUrl)
    expect(init.method).toBe("POST")
    expect(init.headers).toEqual({
      "Content-Type": "application/json",
      Cookie: "oryxa_access=token",
      Origin: "http://localhost:3003",
    })
    expect(JSON.parse(String(init.body))).toEqual({ message: "Hello" })
    expect(items).toEqual([started, delta, answer, completed])
  })

  it("ends at the run's last item while Go still holds the response open", async () => {
    const go = goStream([started, answer, completed])
    fetchMock.mockResolvedValue(go.response)
    const { items, done } = relay()
    await within(done)
    expect(items).toEqual([started, answer, completed])
    expect(go.state.cancelled).toBe(true)
  })

  it("echoes the member's answers once Go accepts them", async () => {
    const go = goStream([started, answer, completed])
    fetchMock.mockResolvedValue(go.response)
    const { items, done } = relay({
      messages: [userMessage(" ")],
      body: {
        confirmations: [
          { id: "call_1", confirmed: true },
          { id: "call_2", confirmed: false },
        ],
      },
    })
    await within(done)
    const [, init] = fetchMock.mock.calls[0] as [URL, RequestInit]
    expect(JSON.parse(String(init.body))).toEqual({
      confirmations: [
        { id: "call_1", confirmed: true },
        { id: "call_2", confirmed: false },
      ],
    })
    expect(items).toEqual([
      { type: "confirmation", call_id: "call_1", status: "approved" },
      { type: "confirmation", call_id: "call_2", status: "declined" },
      started,
      answer,
      completed,
    ])
  })

  it("reports Go's 401 as session_expired, before anything ran", async () => {
    fetchMock.mockResolvedValue(
      json({ error: { code: "unauthorized", message: "expired" } }, 401)
    )
    const { items, done } = relay()
    await within(done)
    expect(items).toEqual([
      {
        type: "run_failed",
        error: "Your session has ended. Sign in again to continue.",
        code: sessionExpiredCode,
      },
    ])
  })

  it("asks the page for a new socket when this one holds no cookie", async () => {
    const { items, done } = relay({ connection: null })
    await within(done)
    expect(fetchMock).not.toHaveBeenCalled()
    expect(items).toEqual([
      expect.objectContaining({ type: "run_failed", code: sessionExpiredCode }),
    ])
  })

  it("reports a refused run with Go's code, and no echo of the answers", async () => {
    fetchMock.mockResolvedValue(
      json({ error: { code: "run_in_progress", message: "busy" } }, 409)
    )
    const { items, done } = relay({
      body: { confirmations: [{ id: "call_1", confirmed: true }] },
    })
    await within(done)
    expect(items).toEqual([
      {
        type: "run_failed",
        error: "Your primary agent is still answering in this conversation.",
        code: "run_in_progress",
      },
    ])
  })

  it("reports a turn with nothing to send without calling Go", async () => {
    const { items, done } = relay({ messages: [] })
    await within(done)
    expect(fetchMock).not.toHaveBeenCalled()
    expect(items).toEqual([
      { type: "run_failed", error: "Send a message to start a run." },
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
        error: "Your primary agent could not be reached. Try again.",
      },
    ])
    expect(logged).toHaveBeenCalled()
  })

  it("ends a stream that stopped early with a run_failed, so no draft is left open", async () => {
    const go = goStream([started, delta], {
      close: true,
      headers: { "X-Oryxa-Run": "run_1" },
    })
    fetchMock.mockResolvedValue(go.response)
    const { items, done } = relay()
    await within(done)
    expect(items).toEqual([
      started,
      delta,
      {
        type: "run_failed",
        run_id: "run_1",
        error: "Your primary agent's answer was cut off before it finished.",
      },
    ])
  })

  it("passes on Go's own run_failed and adds nothing", async () => {
    const failed: Item = {
      type: "run_failed",
      run_id: "run_1",
      error: "The run was cancelled.",
    }
    fetchMock.mockResolvedValue(goStream([started, failed]).response)
    const { items, done } = relay()
    await within(done)
    expect(items).toEqual([started, failed])
  })

  it("writes nothing more once the turn is aborted", async () => {
    const controller = new AbortController()
    const go = goStream([started, delta])
    fetchMock.mockResolvedValue(go.response)
    const { items, done } = relay({ signal: controller.signal })
    await vi.waitFor(() => expect(items).toEqual([started, delta]))
    controller.abort()
    await within(done)
    expect(items).toEqual([started, delta])
    expect(go.state.cancelled).toBe(true)
  })

  it("stops the run in Go when the turn is aborted, since a run outlives its stream", async () => {
    const controller = new AbortController()
    const go = goStream([started, delta], {
      headers: { "X-Oryxa-Run": "run_1" },
    })
    fetchMock.mockResolvedValueOnce(go.response)
    fetchMock.mockResolvedValueOnce(new Response(null, { status: 204 }))
    const { items, done } = relay({ signal: controller.signal })
    await vi.waitFor(() => expect(items).toEqual([started, delta]))
    controller.abort()
    await within(done)
    await vi.waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(2))
    const [url, init] = fetchMock.mock.calls[1] as [URL, RequestInit]
    expect(url.pathname).toMatch(
      /^\/v1\/organizations\/[^/]+\/runs\/run_1\/stop$/
    )
    expect(url.pathname.split("/")[3]).toBe(
      (fetchMock.mock.calls[0][0] as URL).pathname.split("/")[3]
    )
    expect(init.method).toBe("POST")
    expect(init.headers).toEqual(
      expect.objectContaining({ Cookie: expect.any(String) })
    )
  })

  it("does not stop a run that ended on its own", async () => {
    fetchMock.mockResolvedValue(
      goStream([started, delta, { type: "run_completed", run_id: "run_1" }], {
        headers: { "X-Oryxa-Run": "run_1" },
      }).response
    )
    const controller = new AbortController()
    const { done } = relay({ signal: controller.signal })
    await within(done)
    controller.abort()
    expect(fetchMock).toHaveBeenCalledTimes(1)
  })

  it("writes nothing when the turn is aborted before Go answers", async () => {
    const controller = new AbortController()
    fetchMock.mockImplementation(
      (_url: URL, init: RequestInit) =>
        new Promise((_, reject) => {
          init.signal?.addEventListener("abort", () =>
            reject(new DOMException("aborted", "AbortError"))
          )
        })
    )
    const { items, done } = relay({ signal: controller.signal })
    controller.abort()
    await within(done)
    expect(items).toEqual([])
  })
})

/** The protected hooks under test. */
class TestAgent extends ThreadChatAgent {
  recover() {
    return this.onChatRecovery()
  }

  sanitize(message: UIMessage) {
    return this.sanitizeMessageForPersistence(message)
  }
}

function makeAgent(): TestAgent {
  return new TestAgent({} as never, { ORYXA_API_ORIGIN: apiOrigin })
}

/** What the stand-in base class recorded. */
function records(agent: ThreadChatAgent) {
  return agent as unknown as {
    calls: Array<string>
    waitUntilStable: ReturnType<typeof vi.fn>
    persistMessages: ReturnType<typeof vi.fn>
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
  "X-Oryxa-Thread": "thread_1",
  Origin: "http://localhost:3003",
}

/** The `data-oryxa` items of a UI message stream response, and whether it ended. */
async function streamedItems(response: Response): Promise<Array<Item>> {
  const text = await within(response.text())
  return text
    .split("\n")
    .filter((line) => line.startsWith("data: ") && line !== "data: [DONE]")
    .map((line) => JSON.parse(line.slice("data: ".length)))
    .filter((chunk: { type: string }) => chunk.type === "data-oryxa")
    .map((chunk: { data: Item }) => chunk.data)
}

describe("ThreadChatAgent", () => {
  it("never replays a run after a restart", async () => {
    expect(await makeAgent().recover()).toEqual({ continue: false })
  })

  it("stores a finished message without the deltas that drafted it", () => {
    const message = {
      id: "assistant_1",
      role: "assistant" as const,
      parts: [
        { type: "data-oryxa" as const, data: delta },
        { type: "data-oryxa" as const, data: answer },
      ],
    }
    expect(makeAgent().sanitize(message).parts).toEqual([
      { type: "data-oryxa", data: answer },
    ])
  })

  it("keeps the cookie, conversation and origin the Worker routed the socket with", async () => {
    const agent = makeAgent()
    const connected = socket()
    await agent.onConnect(
      connected as unknown as Connection,
      upgrade(routedHeaders)
    )
    expect(connected.close).not.toHaveBeenCalled()
    expect(connected.state).toEqual(connection)
  })

  it("closes a socket the Worker did not route, or routed for another thread", async () => {
    const agent = makeAgent()
    const { "X-Oryxa-Cookie": _cookie, ...withoutCookie } = routedHeaders
    for (const headers of [
      withoutCookie,
      { ...routedHeaders, "X-Oryxa-Thread": "thread_2" },
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

  it("streams a turn as data-oryxa parts and ends it at the run's last item", async () => {
    const agent = makeAgent()
    agent.messages = [userMessage("Hello")]
    current.connection = { state: connection }
    const go = goStream([started, echoed, delta, answer, completed])
    fetchMock.mockResolvedValue(go.response)

    const response = await agent.onChatMessage(async () => {}, {
      requestId: "request_1",
      abortSignal: new AbortController().signal,
    } satisfies OnChatMessageOptions)

    expect(response.headers.get("Content-Type")).toContain("text/event-stream")
    expect(await streamedItems(response)).toEqual([
      started,
      delta,
      answer,
      completed,
    ])
    expect(go.state.cancelled).toBe(true)
    const [url] = fetchMock.mock.calls[0] as [URL]
    expect(String(url)).toBe(runsUrl)
  })

  it("streams a refusal as a run_failed part", async () => {
    const agent = makeAgent()
    agent.messages = [userMessage("Hello")]
    current.connection = { state: connection }
    fetchMock.mockResolvedValue(new Response(null, { status: 401 }))

    const response = await agent.onChatMessage(async () => {}, {
      requestId: "request_1",
    })
    expect(await streamedItems(response)).toEqual([
      {
        type: "run_failed",
        error: "Your session has ended. Sign in again to continue.",
        code: sessionExpiredCode,
      },
    ])
  })

  it("purges by stopping the turn, letting it settle, then clearing and telling the sockets", async () => {
    const agent = makeAgent()
    await agent.purgeTranscript()
    const recorded = records(agent)
    expect(recorded.calls).toEqual([
      "resetTurnState",
      "waitUntilStable",
      "clearMessages",
      "persistMessages",
    ])
    expect(recorded.waitUntilStable).toHaveBeenCalledWith({ timeout: 10_000 })
    expect(recorded.persistMessages).toHaveBeenCalledWith([])
  })
})
