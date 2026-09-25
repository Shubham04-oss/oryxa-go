import type { Item, ItemUsage } from "./client"

/**
 * One `event: <type>\ndata: <payload>\n\n` record off the wire. `event` is ""
 * when the stream left the field out; `data` joins every `data:` line with
 * "\n", as the SSE spec says.
 */
export type ServerSentEvent = {
  data: string
  event: string
}

const lineFeed = 10
const carriageReturn = 13

/**
 * A pure, incremental parser for a `text/event-stream` body read as raw bytes.
 * A run is a POST's response, so `EventSource` cannot read it; a
 * `ReadableStream` reader feeds this instead. Call `push` with every chunk as
 * it arrives, in order, and `end` once when the stream closes, since the last
 * event may have no blank line after it.
 *
 * Chunks may split anywhere: mid-line, between the two halves of `\r\n`, or
 * inside a multi-byte UTF-8 character (the streaming `TextDecoder` holds those
 * bytes back, and also drops a leading byte order mark). Lines end in `\n`,
 * `\r\n` or a lone `\r`. Comment lines (`: keep-alive`) are skipped, and, as
 * the spec says, an event with no `data` line is not dispatched.
 */
export function createEventStreamReader(): {
  push: (chunk: Uint8Array) => ServerSentEvent[]
  end: () => ServerSentEvent[]
} {
  const decoder = new TextDecoder()
  let buffer = ""
  // Nothing before this index of `buffer` ends a line, so a long line that
  // arrives in many chunks is scanned once, not once per chunk.
  let scanFrom = 0
  let dataLines: string[] = []
  let eventType = ""

  function dispatch(events: ServerSentEvent[]) {
    if (dataLines.length > 0) {
      events.push({ data: dataLines.join("\n"), event: eventType })
    }
    dataLines = []
    eventType = ""
  }

  function takeLine(line: string, events: ServerSentEvent[]) {
    if (line === "") {
      dispatch(events)
      return
    }
    if (line.startsWith(":")) return
    const colon = line.indexOf(":")
    const field = colon === -1 ? line : line.slice(0, colon)
    let value = colon === -1 ? "" : line.slice(colon + 1)
    if (value.startsWith(" ")) value = value.slice(1)
    if (field === "event") eventType = value
    else if (field === "data") dataLines.push(value)
    // `id`, `retry` and unknown fields are ignored: nothing here reconnects.
  }

  function drain(atEnd: boolean): ServerSentEvent[] {
    const events: ServerSentEvent[] = []
    let start = 0
    let index = scanFrom
    while (index < buffer.length) {
      const code = buffer.charCodeAt(index)
      if (code === lineFeed) {
        takeLine(buffer.slice(start, index), events)
        start = index + 1
      } else if (code === carriageReturn) {
        // A "\r" last in the buffer may be the first half of a "\r\n" whose
        // "\n" is still on its way.
        if (index === buffer.length - 1 && !atEnd) break
        takeLine(buffer.slice(start, index), events)
        start =
          buffer.charCodeAt(index + 1) === lineFeed ? index + 2 : index + 1
        index = start
        continue
      }
      index += 1
    }
    buffer = buffer.slice(start)
    scanFrom = Math.max(0, index - start)
    if (atEnd) {
      // A last line with no line ending still counts, and so does a last
      // event with no blank line after it.
      if (buffer !== "") takeLine(buffer, events)
      buffer = ""
      scanFrom = 0
      dispatch(events)
    }
    return events
  }

  return {
    push(chunk) {
      buffer += decoder.decode(chunk, { stream: true })
      return drain(false)
    },
    end() {
      buffer += decoder.decode()
      return drain(true)
    },
  }
}

/**
 * How a run's stream ended, as far as the reader saw it.
 *
 * - `completed`: `run_completed` arrived.
 * - `failed`: `run_failed` arrived; `error` is Go's words for the member.
 * - `aborted`: the caller's signal stopped reading.
 * - `incomplete`: the stream closed or broke before either of those. The run
 *   may have stopped half way; read the thread again to see what it kept.
 */
export type RunResult =
  | { outcome: "completed"; runId?: string; usage?: ItemUsage }
  | { outcome: "failed"; runId?: string; error: string; code?: string }
  | { outcome: "aborted"; runId?: string }
  | { outcome: "incomplete"; runId?: string }

export type ReadItemStreamOptions = {
  /** Called with every item, in order. Never called again once `signal` aborts. */
  onItem: (item: Item) => void
  /** Stops reading and cancels the stream. Go treats that as the member leaving and stops the run. */
  signal?: AbortSignal
}

/** An event's item, or nothing for an event that does not carry one. */
function itemOf(event: ServerSentEvent): Item | undefined {
  if (event.data === "") return undefined
  let value: unknown
  try {
    value = JSON.parse(event.data)
  } catch {
    return undefined
  }
  if (
    typeof value !== "object" ||
    value === null ||
    typeof (value as { type?: unknown }).type !== "string"
  ) {
    return undefined
  }
  return value as Item
}

/**
 * Reads a run's `text/event-stream` body item by item: the one loop behind
 * `startRun`, `startSpecialistRun` and the thread's Durable Object, which
 * calls Go itself.
 *
 * It resolves as soon as `run_completed` or `run_failed` arrives, and closes
 * the stream then: nothing follows either one, but Go keeps the response open
 * after `run_completed` while it hands the conversation to memory (up to 15
 * seconds, on a context the member leaving does not cancel). It resolves
 * `aborted` once `signal` aborts and `incomplete` when the stream ends or
 * breaks early, so neither throws. An event that is not an item JSON is
 * skipped rather than cancelling the run. An error thrown by `onItem` cancels
 * the stream and is rethrown.
 */
export async function readItemStream(
  body: ReadableStream<Uint8Array>,
  { onItem, signal }: ReadItemStreamOptions
): Promise<RunResult> {
  const reader = body.getReader()
  const cancel = () => {
    reader.cancel().catch(() => undefined)
  }
  let runId: string | undefined
  if (signal?.aborted) {
    cancel()
    reader.releaseLock()
    return { outcome: "aborted" }
  }
  signal?.addEventListener("abort", cancel, { once: true })

  const parser = createEventStreamReader()
  let ended: RunResult | undefined
  const deliver = (events: ServerSentEvent[]) => {
    for (const event of events) {
      if (signal?.aborted || ended) return
      const item = itemOf(event)
      if (!item) continue
      runId ??= item.run_id
      if (item.type === "run_completed") {
        ended = { outcome: "completed", runId, usage: item.usage }
      } else if (item.type === "run_failed") {
        ended = { outcome: "failed", runId, error: item.error ?? "" }
        if (item.code) ended.code = item.code
      }
      onItem(item)
    }
  }

  try {
    for (;;) {
      let chunk: ReadableStreamReadResult<Uint8Array>
      try {
        chunk = await reader.read()
      } catch {
        if (signal?.aborted) return { outcome: "aborted", runId }
        return { outcome: "incomplete", runId }
      }
      if (signal?.aborted) return { outcome: "aborted", runId }
      deliver(chunk.done ? parser.end() : parser.push(chunk.value))
      if (signal?.aborted) return { outcome: "aborted", runId }
      if (ended) {
        cancel()
        return ended
      }
      if (chunk.done) return { outcome: "incomplete", runId }
    }
  } catch (error) {
    cancel()
    throw error
  } finally {
    signal?.removeEventListener("abort", cancel)
    reader.releaseLock()
  }
}
