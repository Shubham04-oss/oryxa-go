import { afterEach, describe, expect, it, vi } from "vitest"
import { QueryClient } from "@tanstack/react-query"
import { ApiError } from "@/lib/api/client"
import {
  MAX_MESSAGE_LENGTH,
  artifactContentUrl,
  createThread,
  deleteThread,
  deleteThreadChat,
  fetchArtifactLink,
  getThread,
  getThreadChatMessages,
  listThreads,
  retryUnlessRefused,
  startRun,
  threadApprovalQueryOptions,
  threadDetailQueryOptions,
  threadKeys,
  threadListQueryOptions,
} from "./client"
import type { ConfirmationAnswer, Item, RunInput } from "./client"

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

/** A `text/event-stream` response delivered in the given chunks, as the network might split it. */
function streamResponse(
  chunks: string[],
  headers: Record<string, string> = {}
): Response {
  const stream = new ReadableStream<Uint8Array>({
    start(controller) {
      for (const chunk of chunks) controller.enqueue(encoder.encode(chunk))
      controller.close()
    },
  })
  return new Response(stream, {
    headers: { "Content-Type": "text/event-stream", ...headers },
  })
}

/** A stream the test feeds by hand, recording whether the reader cancelled it. */
function controlledStream() {
  let controller!: ReadableStreamDefaultController<Uint8Array>
  const state = { cancelled: false }
  const stream = new ReadableStream<Uint8Array>({
    start(start) {
      controller = start
    },
    cancel() {
      state.cancelled = true
    },
  })
  return {
    state,
    push: (text: string) => controller.enqueue(encoder.encode(text)),
    response: (headers: Record<string, string> = {}) =>
      new Response(stream, {
        headers: { "Content-Type": "text/event-stream", ...headers },
      }),
  }
}

const runsPath = "/v1/organizations/org_1/threads/th_1/runs"

afterEach(() => vi.unstubAllGlobals())

describe("thread requests", () => {
  it("lists the organization's threads", async () => {
    const fetchMock = vi.fn(async () =>
      json({ threads: [{ id: "th_1", title: "", updated_at: "x" }] })
    )
    vi.stubGlobal("fetch", fetchMock)

    expect(await listThreads("org/1")).toEqual([
      { id: "th_1", title: "", updated_at: "x" },
    ])
    expect(fetchMock).toHaveBeenCalledWith(
      "/v1/organizations/org%2F1/threads",
      expect.objectContaining({ method: "GET", credentials: "include" })
    )
  })

  it("creates a thread by posting an empty JSON object", async () => {
    const fetchMock = vi.fn(async () =>
      json({ id: "th_1", title: "", created_at: "x", updated_at: "x" }, 201)
    )
    vi.stubGlobal("fetch", fetchMock)

    expect((await createThread("org_1")).id).toBe("th_1")
    expect(fetchMock).toHaveBeenCalledWith(
      "/v1/organizations/org_1/threads",
      expect.objectContaining({
        method: "POST",
        body: "{}",
        headers: { "Content-Type": "application/json" },
      })
    )
  })

  it("reads one thread with its history", async () => {
    const detail = {
      id: "th 1",
      title: "Hi",
      updated_at: "x",
      items: [{ type: "message", id: "e1", author: "member", text: "Hi" }],
      pending_confirmations: [],
    }
    const fetchMock = vi.fn(async () => json(detail))
    vi.stubGlobal("fetch", fetchMock)

    expect(await getThread("org_1", "th 1")).toEqual(detail)
    expect(fetchMock).toHaveBeenCalledWith(
      "/v1/organizations/org_1/threads/th%201",
      expect.objectContaining({ method: "GET" })
    )
  })

  it("deletes a thread in Go", async () => {
    const fetchMock = vi.fn(async () => new Response(null, { status: 204 }))
    vi.stubGlobal("fetch", fetchMock)

    await expect(deleteThread("org_1", "th_1")).resolves.toBeUndefined()
    expect(fetchMock).toHaveBeenCalledWith(
      "/v1/organizations/org_1/threads/th_1",
      expect.objectContaining({ method: "DELETE" })
    )
  })

  it("deletes a Durable Object conversation through the Worker", async () => {
    const fetchMock = vi.fn(async () => new Response(null, { status: 204 }))
    vi.stubGlobal("fetch", fetchMock)

    await deleteThreadChat("org_1", "th_1")
    expect(fetchMock).toHaveBeenCalledWith(
      "/agents/thread-chat-agent/org_1~th_1",
      expect.objectContaining({ method: "DELETE", credentials: "include" })
    )
  })

  it("reads the Durable Object's stored transcript", async () => {
    const stored = [
      { id: "m1", role: "user", parts: [{ type: "text", text: "Hi" }] },
    ]
    const fetchMock = vi.fn(async () => json(stored))
    vi.stubGlobal("fetch", fetchMock)

    expect(await getThreadChatMessages("org_1", "th_1")).toEqual(stored)
    expect(fetchMock).toHaveBeenCalledWith(
      "/agents/thread-chat-agent/org_1~th_1/get-messages",
      expect.objectContaining({ method: "GET" })
    )
  })
})

describe("startRun", () => {
  const run: Item[] = [
    { type: "run_started", run_id: "run_1" },
    { type: "message", id: "e1", author: "member", text: "hi" },
    { type: "delta", author: "Krude", text: "Hel" },
    { type: "delta", author: "Krude", text: "lo" },
    { type: "message", id: "e2", author: "Krude", text: "Hello" },
    {
      type: "run_completed",
      run_id: "run_1",
      usage: { input_tokens: 3, output_tokens: 1, total_tokens: 4 },
    },
  ]

  it("posts the message and delivers the stream's items in order", async () => {
    const whole = run.map(sse).join(": keep-alive\n\n")
    const cut1 = Math.floor(whole.length / 3)
    const cut2 = Math.floor((whole.length * 2) / 3)
    const fetchMock = vi.fn(async () =>
      streamResponse(
        [whole.slice(0, cut1), whole.slice(cut1, cut2), whole.slice(cut2)],
        { "X-Oryxa-Run": "run_1" }
      )
    )
    vi.stubGlobal("fetch", fetchMock)
    const items: Item[] = []

    const result = await startRun(
      "org_1",
      "th_1",
      { message: "hi" },
      { onItem: (item) => items.push(item) }
    )

    expect(items).toEqual(run)
    expect(result).toEqual({
      outcome: "completed",
      runId: "run_1",
      usage: { input_tokens: 3, output_tokens: 1, total_tokens: 4 },
    })
    expect(fetchMock).toHaveBeenCalledTimes(1)
    expect(fetchMock).toHaveBeenCalledWith(
      runsPath,
      expect.objectContaining({
        method: "POST",
        credentials: "include",
        headers: { "Content-Type": "application/json" },
        body: '{"message":"hi"}',
      })
    )
  })

  it("sends confirmation answers with only the fields Go knows", async () => {
    const fetchMock = vi.fn(async () =>
      streamResponse([sse({ type: "run_completed", run_id: "run_2" })])
    )
    vi.stubGlobal("fetch", fetchMock)
    const answers = [
      { id: "c1", confirmed: true, label: "Send it" },
      { id: "c2", confirmed: false },
    ] as ConfirmationAnswer[]

    await startRun(
      "org_1",
      "th_1",
      { confirmations: answers },
      { onItem: () => {} }
    )

    expect(fetchMock).toHaveBeenCalledWith(
      runsPath,
      expect.objectContaining({
        body: '{"confirmations":[{"id":"c1","confirmed":true},{"id":"c2","confirmed":false}]}',
      })
    )
  })

  it("refuses what Go would refuse, in Go's words, before sending anything", async () => {
    const fetchMock = vi.fn()
    vi.stubGlobal("fetch", fetchMock)
    const refused: Array<[unknown, string]> = [
      [
        { message: "  \n" },
        "send a message or answer the waiting confirmations",
      ],
      [
        { confirmations: [] },
        "send a message or answer the waiting confirmations",
      ],
      [
        { message: "hi", confirmations: [{ id: "c1", confirmed: true }] },
        "send a message or answer confirmations, not both at once",
      ],
      [
        { confirmations: [{ id: "c1" }] },
        "every confirmation needs an id and whether it is confirmed",
      ],
      [
        { message: "x".repeat(MAX_MESSAGE_LENGTH + 1) },
        "a message holds at most 32000 characters",
      ],
    ]

    for (const [input, message] of refused) {
      await expect(
        startRun("org_1", "th_1", input as RunInput, { onItem: () => {} })
      ).rejects.toMatchObject({ status: 400, code: "invalid_request", message })
    }
    expect(fetchMock).not.toHaveBeenCalled()
  })

  it("counts a message's length in characters, not UTF-16 units", async () => {
    const fetchMock = vi.fn(async () =>
      streamResponse([sse({ type: "run_completed" })])
    )
    vi.stubGlobal("fetch", fetchMock)
    // 32,000 emoji are 64,000 UTF-16 units but 32,000 characters to Go.
    await startRun(
      "org_1",
      "th_1",
      { message: "🙂".repeat(MAX_MESSAGE_LENGTH) },
      { onItem: () => {} }
    )
    expect(fetchMock).toHaveBeenCalledTimes(1)
  })

  it("refreshes an expired session once, then retries and streams", async () => {
    const fetchMock = vi
      .fn()
      .mockResolvedValueOnce(json({ error: { code: "unauthorized" } }, 401))
      .mockResolvedValueOnce(new Response(null, { status: 204 }))
      .mockResolvedValueOnce(streamResponse(run.map(sse)))
    vi.stubGlobal("fetch", fetchMock)
    const onItem = vi.fn()

    const result = await startRun(
      "org_1",
      "th_1",
      { message: "hi" },
      { onItem }
    )

    expect(result.outcome).toBe("completed")
    expect(onItem).toHaveBeenCalledTimes(run.length)
    expect(fetchMock.mock.calls.map(([url]) => url)).toEqual([
      runsPath,
      "/v1/auth/refresh",
      runsPath,
    ])
    expect(fetchMock.mock.calls[1][1]).toEqual({
      credentials: "include",
      method: "POST",
      // The refresh has a timeout (lib/api/client.ts).
      signal: expect.any(AbortSignal),
    })
  })

  it("keeps Go's code when it refuses the run, and delivers nothing", async () => {
    const refusals = [
      [409, "run_in_progress", "the run was already in progress"],
      [
        409,
        "confirmation_pending",
        "a tool is waiting for you to approve or decline it",
      ],
      [503, "agents_unavailable", "GOOGLE_CLOUD_PROJECT is not set"],
      [404, "not_found", "thread not found"],
    ] as const
    for (const [status, code, message] of refusals) {
      vi.stubGlobal(
        "fetch",
        vi.fn(async () => json({ error: { code, message } }, status))
      )
      const onItem = vi.fn()
      const refusal = startRun("org_1", "th_1", { message: "hi" }, { onItem })
      await expect(refusal).rejects.toBeInstanceOf(ApiError)
      await expect(refusal).rejects.toMatchObject({ status, code, message })
      expect(onItem).not.toHaveBeenCalled()
    }
  })

  it("reports a failed run through its result, not an exception", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async () =>
        streamResponse([
          sse({ type: "run_started", run_id: "run_3" }),
          sse({
            type: "run_failed",
            run_id: "run_3",
            error: "Another run took over this thread.",
          }),
        ])
      )
    )
    await expect(
      startRun("org_1", "th_1", { message: "hi" }, { onItem: () => {} })
    ).resolves.toEqual({
      outcome: "failed",
      runId: "run_3",
      error: "Another run took over this thread.",
    })
  })

  it("names the run from X-Oryxa-Run when the stream ends before saying", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async () => streamResponse([], { "X-Oryxa-Run": "run_4" }))
    )
    await expect(
      startRun("org_1", "th_1", { message: "hi" }, { onItem: () => {} })
    ).resolves.toEqual({ outcome: "incomplete", runId: "run_4" })
  })

  it("stops cleanly when aborted mid-stream", async () => {
    const body = controlledStream()
    const fetchMock = vi.fn(async () => body.response())
    vi.stubGlobal("fetch", fetchMock)
    const controller = new AbortController()
    const items: Item[] = []

    const running = startRun(
      "org_1",
      "th_1",
      { message: "hi" },
      { onItem: (item) => items.push(item), signal: controller.signal }
    )
    body.push(sse(run[0]) + sse(run[1]))
    await vi.waitFor(() => expect(items).toHaveLength(2))
    controller.abort()

    await expect(running).resolves.toEqual({
      outcome: "aborted",
      runId: "run_1",
    })
    expect(items).toHaveLength(2)
    expect(body.state.cancelled).toBe(true)
    expect(fetchMock).toHaveBeenCalledWith(
      runsPath,
      expect.objectContaining({ signal: controller.signal })
    )
  })

  it("resolves aborted when stopped before Go answers", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(
        (_url: string, init: RequestInit) =>
          new Promise<Response>((_resolve, reject) => {
            init.signal?.addEventListener("abort", () =>
              reject(
                new DOMException("The operation was aborted.", "AbortError")
              )
            )
          })
      )
    )
    const controller = new AbortController()
    const running = startRun(
      "org_1",
      "th_1",
      { message: "hi" },
      { onItem: () => {}, signal: controller.signal }
    )
    controller.abort()
    await expect(running).resolves.toEqual({ outcome: "aborted" })
  })

  it("rejects a network failure before any answer", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async () => {
        throw new TypeError("Failed to fetch")
      })
    )
    await expect(
      startRun("org_1", "th_1", { message: "hi" }, { onItem: () => {} })
    ).rejects.toThrow("Failed to fetch")
  })
})

describe("artifacts", () => {
  it("builds a plain URL with the whole name as one path segment", () => {
    expect(artifactContentUrl("org_1", "th_1", "summary.png", 2)).toBe(
      "/v1/organizations/org_1/threads/th_1/artifacts/summary.png?version=2"
    )
    expect(artifactContentUrl("org_1", "th_1", "user:notes v2.txt", 1)).toBe(
      "/v1/organizations/org_1/threads/th_1/artifacts/user%3Anotes%20v2.txt?version=1"
    )
    // Go routes `{name}` as one segment and ADK refuses a "/" in a name.
    expect(artifactContentUrl("org_1", "th_1", "a/b.txt", 1)).toBe(
      "/v1/organizations/org_1/threads/th_1/artifacts/a%2Fb.txt?version=1"
    )
  })

  it("asks for the latest version rather than one Go would refuse", () => {
    for (const version of [undefined, 0, -1, 1.5]) {
      expect(artifactContentUrl("org_1", "th_1", "a.png", version)).toBe(
        "/v1/organizations/org_1/threads/th_1/artifacts/a.png"
      )
    }
  })

  it("refreshes the session before handing the browser the plain URL", async () => {
    const body = controlledStream()
    body.push("png-bytes")
    const fetchMock = vi
      .fn()
      .mockResolvedValueOnce(json({ error: { code: "unauthorized" } }, 401))
      .mockResolvedValueOnce(new Response(null, { status: 204 }))
      .mockResolvedValueOnce(
        body.response({
          "Content-Type": "image/png",
          "Content-Disposition": "inline; filename=chart.png",
        })
      )
    vi.stubGlobal("fetch", fetchMock)
    const path =
      "/v1/organizations/org_1/threads/th_1/artifacts/chart.png?version=3"

    await expect(
      fetchArtifactLink("org_1", "th_1", { name: "chart.png", version: 3 })
    ).resolves.toEqual({ url: path, contentType: "image/png", inline: true })
    expect(fetchMock.mock.calls.map(([url]) => url)).toEqual([
      path,
      "/v1/auth/refresh",
      path,
    ])
    // Only the headers were wanted: the file itself is not read twice.
    expect(body.state.cancelled).toBe(true)
  })

  it("says a file downloads rather than opening in place", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(
        async () =>
          new Response("<html>", {
            headers: {
              "Content-Type": "text/html",
              "Content-Disposition": "attachment; filename=report.html",
            },
          })
      )
    )
    await expect(
      fetchArtifactLink("org_1", "th_1", { name: "report.html" })
    ).resolves.toEqual({
      url: "/v1/organizations/org_1/threads/th_1/artifacts/report.html",
      contentType: "text/html",
      inline: false,
    })
  })

  it("fails in the page for a file that is gone", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async () =>
        json({ error: { code: "not_found", message: "thread not found" } }, 404)
      )
    )
    await expect(
      fetchArtifactLink("org_1", "th_1", { name: "gone.png", version: 1 })
    ).rejects.toMatchObject({ status: 404, code: "not_found" })
  })
})

describe("query helpers", () => {
  it("shares approval reads for one member across Home and history, without reusing another member's data", async () => {
    const fetch = vi.fn(async () =>
      json({ id: "th_1", items: [], pending_confirmations: ["approve"] })
    )
    vi.stubGlobal("fetch", fetch)
    const client = new QueryClient()
    const options = () => threadApprovalQueryOptions("org_1", "th_1", "user_a")
    const [home, history] = await Promise.all([
      client.fetchQuery(options()),
      client.fetchQuery(options()),
    ])
    expect(home).toBe(history)
    await client.fetchQuery(options())
    expect(fetch).toHaveBeenCalledOnce()
    expect(
      client.getQueryData(
        threadApprovalQueryOptions("org_1", "th_1", "user_b").queryKey
      )
    ).toBeUndefined()
    await client.invalidateQueries({
      queryKey: threadKeys.detail("org_1", "th_1"),
    })
    await client.fetchQuery(options())
    expect(fetch).toHaveBeenCalledTimes(2)
    client.clear()
  })
  it("scopes keys by organization, with the list and a thread apart", () => {
    expect(threadKeys.all("org_1")).toEqual(["threads", "org_1"])
    expect(threadKeys.list("org_1")).toEqual(["threads", "org_1", "list"])
    expect(threadKeys.detail("org_1", "th_1")).toEqual([
      "threads",
      "org_1",
      "detail",
      "th_1",
    ])
    expect(threadListQueryOptions("org_1").queryKey).toEqual(
      threadKeys.list("org_1")
    )
    expect(threadDetailQueryOptions("org_1", "th_1").queryKey).toEqual(
      threadKeys.detail("org_1", "th_1")
    )
  })

  it("keeps each member's list apart, under a key that invalidating the organization's lists still reaches", async () => {
    expect(threadListQueryOptions("org_1", "user_a").queryKey).toEqual([
      "threads",
      "org_1",
      "list",
      "user_a",
    ])
    // Without a member it is the organization's list, as before.
    expect(threadListQueryOptions("org_1", undefined).queryKey).toEqual(
      threadKeys.list("org_1")
    )

    const client = new QueryClient()
    const lists = {
      user_a: [
        { id: "th_a", title: "Mine", updated_at: "2026-09-30T10:00:00Z" },
      ],
      user_b: [],
    }
    for (const [viewer, threads] of Object.entries(lists)) {
      client.setQueryData(
        threadListQueryOptions("org_1", viewer).queryKey,
        threads
      )
    }
    // One member's list is not the other's.
    expect(
      client.getQueryData(threadListQueryOptions("org_1", "user_b").queryKey)
    ).toEqual([])
    expect(
      client.getQueryData(threadListQueryOptions("org_1", "user_a").queryKey)
    ).toHaveLength(1)
    // A member with nothing cached reads nothing of the others'.
    expect(
      client.getQueryData(threadListQueryOptions("org_1", "user_c").queryKey)
    ).toBeUndefined()
    expect(
      client.getQueryData(threadListQueryOptions("org_1").queryKey)
    ).toBeUndefined()

    // What invalidates the organization's lists reaches every member's.
    await client.invalidateQueries({ queryKey: threadKeys.list("org_1") })
    expect(
      client.getQueryState(threadListQueryOptions("org_1", "user_a").queryKey)
        ?.isInvalidated
    ).toBe(true)
    expect(
      client.getQueryState(threadListQueryOptions("org_1", "user_b").queryKey)
        ?.isInvalidated
    ).toBe(true)
  })

  it("fetches the list and a thread through the query client", async () => {
    const fetchMock = vi.fn(async (url: string) =>
      url.endsWith("/threads")
        ? json({ threads: [] })
        : json({ id: "th_1", items: [], pending_confirmations: [] })
    )
    vi.stubGlobal("fetch", fetchMock)
    const client = new QueryClient()

    await client.fetchQuery(threadListQueryOptions("org_1"))
    await client.fetchQuery(threadDetailQueryOptions("org_1", "th_1"))

    expect(fetchMock.mock.calls.map(([url]) => url)).toEqual([
      "/v1/organizations/org_1/threads",
      "/v1/organizations/org_1/threads/th_1",
    ])
  })

  it("retries a failed read once, but never a refusal", () => {
    expect(retryUnlessRefused(0, new ApiError("thread not found", 404))).toBe(
      false
    )
    expect(retryUnlessRefused(0, new ApiError("forbidden", 403))).toBe(false)
    expect(retryUnlessRefused(0, new ApiError("internal", 500))).toBe(true)
    expect(retryUnlessRefused(0, new TypeError("Failed to fetch"))).toBe(true)
    expect(retryUnlessRefused(1, new ApiError("internal", 500))).toBe(false)
  })
})
