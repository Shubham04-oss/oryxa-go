import { afterEach, describe, expect, it, vi } from "vitest"
import { QueryClient } from "@tanstack/react-query"
import {
  getSpecialistThread,
  listSpecialistThreads,
  specialistKeys,
  specialistThreadQueryOptions,
  specialistThreadsQueryOptions,
  startSpecialistRun,
} from "./client"
import type { Item } from "@/lib/threads/client"

const encoder = new TextEncoder()

function json(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  })
}

function sse(item: Item): string {
  return `event: ${item.type}\ndata: ${JSON.stringify(item)}\n\n`
}

function streamResponse(
  items: Item[],
  headers: Record<string, string> = {}
): Response {
  const stream = new ReadableStream<Uint8Array>({
    start(controller) {
      for (const item of items) controller.enqueue(encoder.encode(sse(item)))
      controller.close()
    },
  })
  return new Response(stream, {
    headers: { "Content-Type": "text/event-stream", ...headers },
  })
}

const result = JSON.stringify({
  status: "completed",
  summary: "Pipeline reviewed.",
  tools_used: [],
  artifacts: [],
  handoffs: [],
})
const run: Item[] = [
  { type: "run_started", run_id: "run_1" },
  { type: "message", id: "e1", author: "member", text: "Review this quarter" },
  { type: "message", id: "e2", author: "Sales", text: result },
  { type: "run_completed", run_id: "run_1" },
]
const runsPath = "/v1/organizations/org_1/agents/agt_sales/runs"

afterEach(() => vi.unstubAllGlobals())

describe("specialist conversations", () => {
  it("lists and reads only the selected specialist's conversations", async () => {
    const fetchMock = vi.fn(async (url: string) =>
      url.endsWith("/threads")
        ? json({ threads: [{ id: "th 1", title: "Review", updated_at: "x" }] })
        : json({
            id: "th 1",
            title: "Review",
            updated_at: "x",
            items: [],
            pending_confirmations: [],
          })
    )
    vi.stubGlobal("fetch", fetchMock)

    expect(await listSpecialistThreads("org_1", "agt/sales")).toHaveLength(1)
    expect((await getSpecialistThread("org_1", "agt/sales", "th 1")).id).toBe(
      "th 1"
    )
    expect(fetchMock.mock.calls.map(([url]) => url)).toEqual([
      "/v1/organizations/org_1/agents/agt%2Fsales/threads",
      "/v1/organizations/org_1/agents/agt%2Fsales/threads/th%201",
    ])
    expect(fetchMock).toHaveBeenCalledWith(
      expect.any(String),
      expect.objectContaining({ method: "GET", credentials: "include" })
    )
  })

  it("starts a new conversation and learns its id before the first item", async () => {
    const fetchMock = vi.fn(async () =>
      streamResponse(run, {
        "X-Oryxa-Thread": "agent_thread_1",
        "X-Oryxa-Run": "run_1",
      })
    )
    vi.stubGlobal("fetch", fetchMock)
    const seen: string[] = []

    const outcome = await startSpecialistRun(
      "org_1",
      "agt_sales",
      { message: "Review this quarter" },
      {
        onThread: (id) => seen.push(`thread:${id}`),
        onItem: (item) => seen.push(item.type),
      }
    )

    expect(seen).toEqual([
      "thread:agent_thread_1",
      "run_started",
      "message",
      "message",
      "run_completed",
    ])
    expect(outcome).toEqual({
      outcome: "completed",
      runId: "run_1",
      threadId: "agent_thread_1",
    })
    expect(fetchMock).toHaveBeenCalledWith(
      runsPath,
      expect.objectContaining({
        method: "POST",
        credentials: "include",
        body: '{"message":"Review this quarter"}',
      })
    )
  })

  it("continues a conversation and answers its confirmations there", async () => {
    const fetchMock = vi.fn(async () =>
      streamResponse([{ type: "run_completed", run_id: "run_2" }], {
        "X-Oryxa-Thread": "agent_thread_1",
      })
    )
    vi.stubGlobal("fetch", fetchMock)

    const outcome = await startSpecialistRun(
      "org_1",
      "agt_sales",
      {
        thread_id: "agent_thread_1",
        confirmations: [{ id: "confirm_1", confirmed: true }],
      },
      { onItem: () => {} }
    )

    expect(outcome.threadId).toBe("agent_thread_1")
    expect(fetchMock).toHaveBeenCalledWith(
      runsPath,
      expect.objectContaining({
        body: '{"thread_id":"agent_thread_1","confirmations":[{"id":"confirm_1","confirmed":true}]}',
      })
    )
  })

  it("never sends an empty message, which would leave an empty conversation behind", async () => {
    const fetchMock = vi.fn()
    vi.stubGlobal("fetch", fetchMock)

    await expect(
      startSpecialistRun(
        "org_1",
        "agt_sales",
        { message: "   " },
        { onItem: () => {} }
      )
    ).rejects.toMatchObject({ status: 400, code: "invalid_request" })
    expect(fetchMock).not.toHaveBeenCalled()
  })

  it("keeps Go's code for a paused specialist", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async () =>
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
    )
    const onThread = vi.fn()

    await expect(
      startSpecialistRun(
        "org_1",
        "agt_sales",
        { message: "hi" },
        { onThread, onItem: () => {} }
      )
    ).rejects.toMatchObject({ status: 409, code: "agent_disabled" })
    expect(onThread).not.toHaveBeenCalled()
  })

  it("refreshes an expired session once before the stream starts", async () => {
    const fetchMock = vi
      .fn()
      .mockResolvedValueOnce(json({ error: { code: "unauthorized" } }, 401))
      .mockResolvedValueOnce(new Response(null, { status: 204 }))
      .mockResolvedValueOnce(
        streamResponse(run, { "X-Oryxa-Thread": "agent_thread_1" })
      )
    vi.stubGlobal("fetch", fetchMock)

    const outcome = await startSpecialistRun(
      "org_1",
      "agt_sales",
      { message: "hi" },
      { onItem: () => {} }
    )

    expect(outcome.outcome).toBe("completed")
    expect(fetchMock.mock.calls.map(([url]) => url)).toEqual([
      runsPath,
      "/v1/auth/refresh",
      runsPath,
    ])
  })

  it("stops cleanly when aborted mid-stream, and still knows the conversation", async () => {
    let controller!: ReadableStreamDefaultController<Uint8Array>
    const stream = new ReadableStream<Uint8Array>({
      start(start) {
        controller = start
      },
    })
    vi.stubGlobal(
      "fetch",
      vi.fn(
        async () =>
          new Response(stream, {
            headers: { "X-Oryxa-Thread": "agent_thread_1" },
          })
      )
    )
    const abort = new AbortController()
    const items: Item[] = []

    const running = startSpecialistRun(
      "org_1",
      "agt_sales",
      { message: "hi" },
      { onItem: (item) => items.push(item), signal: abort.signal }
    )
    controller.enqueue(encoder.encode(sse(run[0])))
    await vi.waitFor(() => expect(items).toHaveLength(1))
    abort.abort()

    await expect(running).resolves.toEqual({
      outcome: "aborted",
      runId: "run_1",
      threadId: "agent_thread_1",
    })
    expect(items).toHaveLength(1)
  })
})

describe("specialist query helpers", () => {
  it("scopes keys by organization and specialist, with the list and a conversation apart", () => {
    expect(specialistKeys.all("org_1")).toEqual(["specialists", "org_1"])
    expect(specialistKeys.agent("org_1", "agt_1")).toEqual([
      "specialists",
      "org_1",
      "agt_1",
    ])
    expect(specialistKeys.threads("org_1", "agt_1")).toEqual([
      "specialists",
      "org_1",
      "agt_1",
      "threads",
    ])
    expect(specialistKeys.thread("org_1", "agt_1", "th_1")).toEqual([
      "specialists",
      "org_1",
      "agt_1",
      "thread",
      "th_1",
    ])
  })

  it("fetches through the query client", async () => {
    const fetchMock = vi.fn(async (url: string) =>
      url.endsWith("/threads")
        ? json({ threads: [] })
        : json({ id: "th_1", items: [], pending_confirmations: [] })
    )
    vi.stubGlobal("fetch", fetchMock)
    const client = new QueryClient()

    await client.fetchQuery(specialistThreadsQueryOptions("org_1", "agt_1"))
    await client.fetchQuery(
      specialistThreadQueryOptions("org_1", "agt_1", "th_1")
    )

    expect(fetchMock.mock.calls.map(([url]) => url)).toEqual([
      "/v1/organizations/org_1/agents/agt_1/threads",
      "/v1/organizations/org_1/agents/agt_1/threads/th_1",
    ])
  })
})
