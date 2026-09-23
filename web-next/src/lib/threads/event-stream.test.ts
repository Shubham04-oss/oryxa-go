import { describe, expect, it, vi } from "vitest"
import { createEventStreamReader, readItemStream } from "./event-stream"
import type { ServerSentEvent } from "./event-stream"
import type { Item } from "./client"

const encoder = new TextEncoder()

function bytes(text: string): Uint8Array {
  return encoder.encode(text)
}

/** Feeds already-encoded chunks through a fresh reader and collects every event, `end()` included. */
function readAll(chunks: Uint8Array[]): ServerSentEvent[] {
  const reader = createEventStreamReader()
  const events: ServerSentEvent[] = []
  for (const chunk of chunks) events.push(...reader.push(chunk))
  events.push(...reader.end())
  return events
}

describe("createEventStreamReader", () => {
  it("parses one event terminated by a blank line", () => {
    expect(
      readAll([bytes('event: message\ndata: {"type":"message"}\n\n')])
    ).toEqual([{ data: '{"type":"message"}', event: "message" }])
  })

  it("joins several data lines with \\n", () => {
    expect(
      readAll([bytes("event: message\ndata: line one\ndata: line two\n\n")])
    ).toEqual([{ data: "line one\nline two", event: "message" }])
  })

  it("accepts \\r\\n line endings", () => {
    expect(
      readAll([
        bytes('event: run_started\r\ndata: {"type":"run_started"}\r\n\r\n'),
      ])
    ).toEqual([{ data: '{"type":"run_started"}', event: "run_started" }])
  })

  it("ignores keep-alive comment lines wherever they appear", () => {
    expect(
      readAll([
        bytes(
          ": keep-alive\n\nevent: message\n: keep-alive\ndata: hello\n\n: keep-alive\n\n"
        ),
      ])
    ).toEqual([{ data: "hello", event: "message" }])
  })

  it("parses several events delivered in one chunk", () => {
    expect(
      readAll([bytes("event: a\ndata: 1\n\nevent: b\ndata: 2\n\n")])
    ).toEqual([
      { data: "1", event: "a" },
      { data: "2", event: "b" },
    ])
  })

  it("reassembles an event split across chunks mid-line", () => {
    expect(
      readAll([bytes('event: message\ndata: {"text":"hel'), bytes('lo"}\n\n')])
    ).toEqual([{ data: '{"text":"hello"}', event: "message" }])
  })

  it("reassembles a line split at every possible byte offset", () => {
    const encoded = bytes('event: message\ndata: {"text":"hello world"}\n\n')
    for (let cut = 1; cut < encoded.length; cut += 1) {
      expect(readAll([encoded.slice(0, cut), encoded.slice(cut)])).toEqual([
        { data: '{"text":"hello world"}', event: "message" },
      ])
    }
  })

  it("does not mistake a chunk boundary inside \\r\\n for a blank line", () => {
    // The first chunk ends right after the "\r" half of "data: hello\r\n".
    expect(
      readAll([bytes("data: hello\r"), bytes("\ndata: world\n\n")])
    ).toEqual([{ data: "hello\nworld", event: "" }])
  })

  it("treats a trailing lone \\r as its own line ending once the stream ends", () => {
    const reader = createEventStreamReader()
    expect(reader.push(bytes("data: hello\r"))).toEqual([])
    expect(reader.end()).toEqual([{ data: "hello", event: "" }])
  })

  it("reassembles a multi-byte UTF-8 character split across chunks", () => {
    // "🙂" is F0 9F 99 82; cutting before 0x9F splits it after its first byte.
    const encoded = bytes('event: message\ndata: {"text":"hi 🙂"}\n\n')
    const cut = encoded.indexOf(0x9f)
    expect(cut).toBeGreaterThan(0)
    expect(readAll([encoded.slice(0, cut), encoded.slice(cut)])).toEqual([
      { data: '{"text":"hi 🙂"}', event: "message" },
    ])
  })

  it("dispatches a final event with no trailing blank line only at end()", () => {
    const reader = createEventStreamReader()
    expect(
      reader.push(bytes('event: run_completed\ndata: {"type":"run_completed"}'))
    ).toEqual([])
    expect(reader.end()).toEqual([
      { data: '{"type":"run_completed"}', event: "run_completed" },
    ])
  })

  it("dispatches a final event whose last data line does end in a newline", () => {
    const reader = createEventStreamReader()
    expect(reader.push(bytes("event: message\ndata: last\n"))).toEqual([])
    expect(reader.end()).toEqual([{ data: "last", event: "message" }])
  })

  it("emits nothing extra when the stream ends cleanly on a blank line", () => {
    const reader = createEventStreamReader()
    expect(reader.push(bytes("event: message\ndata: hi\n\n"))).toEqual([
      { data: "hi", event: "message" },
    ])
    expect(reader.end()).toEqual([])
  })

  it("mixes \\n and \\r\\n endings within the same stream", () => {
    expect(
      readAll([bytes("event: a\r\ndata: 1\r\n\r\nevent: b\ndata: 2\n\n")])
    ).toEqual([
      { data: "1", event: "a" },
      { data: "2", event: "b" },
    ])
  })

  it("dispatches no event that had no data line, as the SSE spec says", () => {
    expect(readAll([bytes("event: ping\n\nid: 7\nretry: 10\n\n")])).toEqual([])
  })

  it("keeps an empty data line, and strips only one space after the colon", () => {
    expect(readAll([bytes("data:\n\ndata:  two\n\n")])).toEqual([
      { data: "", event: "" },
      { data: " two", event: "" },
    ])
  })

  it("drops a byte order mark at the start of the stream", () => {
    expect(
      readAll([new Uint8Array([0xef, 0xbb, 0xbf]), bytes("data: x\n\n")])
    ).toEqual([{ data: "x", event: "" }])
  })

  it("reads a long line fed one byte at a time", () => {
    const text = `data: ${"é".repeat(2_000)}🙂\r\n\r\n`
    expect(
      readAll(Array.from(bytes(text), (byte) => Uint8Array.of(byte)))
    ).toEqual([{ data: `${"é".repeat(2_000)}🙂`, event: "" }])
  })
})

function sse(item: Item): string {
  return `event: ${item.type}\ndata: ${JSON.stringify(item)}\n\n`
}

/** A body the test feeds by hand, which records whether the reader cancelled it. */
function controlledBody() {
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
    stream,
    state,
    push: (text: string) => controller.enqueue(bytes(text)),
    close: () => controller.close(),
    fail: (error: Error) => controller.error(error),
  }
}

function bodyOf(chunks: string[]): ReadableStream<Uint8Array> {
  return new ReadableStream({
    start(controller) {
      for (const chunk of chunks) controller.enqueue(bytes(chunk))
      controller.close()
    },
  })
}

describe("readItemStream", () => {
  const run: Item[] = [
    { type: "run_started", run_id: "run_1" },
    { type: "message", id: "e1", author: "member", text: "hi" },
    { type: "delta", author: "Krude", text: "Hel" },
    { type: "delta", author: "Krude", text: "lo" },
    { type: "message", id: "e2", author: "Krude", text: "Hello" },
    {
      type: "run_completed",
      run_id: "run_1",
      usage: { input_tokens: 5, output_tokens: 2, total_tokens: 7 },
    },
  ]

  it("delivers every item in order and resolves how the run ended", async () => {
    const whole = run.map(sse).join(": keep-alive\n\n")
    const cut = Math.floor(whole.length / 2)
    const items: Item[] = []

    const result = await readItemStream(
      bodyOf([whole.slice(0, cut), whole.slice(cut)]),
      { onItem: (item) => items.push(item) }
    )

    expect(items).toEqual(run)
    expect(result).toEqual({
      outcome: "completed",
      runId: "run_1",
      usage: { input_tokens: 5, output_tokens: 2, total_tokens: 7 },
    })
  })

  it("resolves at run_completed and closes the stream Go keeps open to remember", async () => {
    const body = controlledBody()
    body.push(run.map(sse).join(""))
    // Nothing more is pushed and the stream is never closed: Go holds the
    // response open after run_completed while it writes memory.
    const result = await readItemStream(body.stream, { onItem: () => {} })

    expect(result.outcome).toBe("completed")
    expect(body.state.cancelled).toBe(true)
  })

  it("resolves a failed run with Go's words and stops there", async () => {
    const onItem = vi.fn()
    const result = await readItemStream(
      bodyOf([
        sse({ type: "run_started", run_id: "run_2" }),
        sse({
          type: "run_failed",
          run_id: "run_2",
          error: "Krude ran into a problem and stopped. Try again.",
        }),
      ]),
      { onItem }
    )

    expect(result).toEqual({
      outcome: "failed",
      runId: "run_2",
      error: "Krude ran into a problem and stopped. Try again.",
    })
    expect(onItem).toHaveBeenCalledTimes(2)
  })

  it("carries the Durable Object's code on a failure it wrote", async () => {
    const result = await readItemStream(
      bodyOf([
        sse({ type: "run_failed", error: "expired", code: "session_expired" }),
      ]),
      { onItem: () => {} }
    )
    expect(result).toEqual({
      outcome: "failed",
      runId: undefined,
      error: "expired",
      code: "session_expired",
    })
  })

  it("resolves incomplete when the stream ends before the run does", async () => {
    const result = await readItemStream(bodyOf(run.slice(0, 3).map(sse)), {
      onItem: () => {},
    })
    expect(result).toEqual({ outcome: "incomplete", runId: "run_1" })
  })

  it("resolves incomplete when the connection breaks mid-run", async () => {
    const body = controlledBody()
    const items: Item[] = []
    const reading = readItemStream(body.stream, {
      onItem: (item) => items.push(item),
    })
    body.push(sse(run[0]))
    await vi.waitFor(() => expect(items).toHaveLength(1))
    body.fail(new TypeError("network error"))

    await expect(reading).resolves.toEqual({
      outcome: "incomplete",
      runId: "run_1",
    })
  })

  it("stops cleanly on abort: resolves, cancels the stream, delivers nothing more", async () => {
    const body = controlledBody()
    const controller = new AbortController()
    const items: Item[] = []
    const reading = readItemStream(body.stream, {
      onItem: (item) => items.push(item),
      signal: controller.signal,
    })
    body.push(sse(run[0]) + sse(run[1]))
    await vi.waitFor(() => expect(items).toHaveLength(2))

    controller.abort()
    await expect(reading).resolves.toEqual({
      outcome: "aborted",
      runId: "run_1",
    })
    expect(body.state.cancelled).toBe(true)
    expect(items).toHaveLength(2)
  })

  it("stops mid-chunk when onItem itself aborts", async () => {
    const controller = new AbortController()
    const items: Item[] = []
    const result = await readItemStream(bodyOf([run.map(sse).join("")]), {
      onItem: (item) => {
        items.push(item)
        if (item.type === "message") controller.abort()
      },
      signal: controller.signal,
    })

    expect(result.outcome).toBe("aborted")
    expect(items.map((item) => item.type)).toEqual(["run_started", "message"])
  })

  it("reads nothing when the signal has already aborted", async () => {
    const body = controlledBody()
    const onItem = vi.fn()
    body.push(sse(run[0]))

    const result = await readItemStream(body.stream, {
      onItem,
      signal: AbortSignal.abort(),
    })

    expect(result).toEqual({ outcome: "aborted" })
    expect(onItem).not.toHaveBeenCalled()
    expect(body.state.cancelled).toBe(true)
  })

  it("skips events that carry no item instead of stopping the run", async () => {
    const items: Item[] = []
    const result = await readItemStream(
      bodyOf([
        "data: not json\n\n",
        'data: {"no":"type"}\n\n',
        "event: ping\n\n",
        ...run.map(sse),
      ]),
      { onItem: (item) => items.push(item) }
    )
    expect(items).toEqual(run)
    expect(result.outcome).toBe("completed")
  })

  it("cancels the stream and rethrows when onItem throws", async () => {
    const body = controlledBody()
    body.push(sse(run[0]))
    await expect(
      readItemStream(body.stream, {
        onItem: () => {
          throw new Error("render failed")
        },
      })
    ).rejects.toThrow("render failed")
    expect(body.state.cancelled).toBe(true)
  })
})
