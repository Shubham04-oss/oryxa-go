// A stand-in for Go's `POST /v1/realtime/events` (src/realtime-events.ts),
// as the realtime service answers it: it checks each request's timestamp,
// signature (with its own HMAC of `events.{timestamp}.{body}`) and limits,
// then answers as the test says (204 once the test says nothing), and keeps
// every request it was sent. Use its `fetch` as the global one, or route
// the events path to it (`routeFetch`).
import { vi } from "vitest"

export const eventsOrigin = "http://go.test"
export const eventsSecret = "0123456789abcdef0123456789abcdef"

/** One event of a request, parsed. */
export type SentEvent = {
  id: string
  type: string
  detail: Record<string, unknown>
}

/** One request Go was sent. */
export type EventsRequest = {
  url: string
  method: string
  headers: Record<string, string>
  /** The body as sent, byte for byte. */
  body: string
  events: SentEvent[]
  /** When it arrived, by the test's clock. */
  at: number
  /** What Go answered. */
  status?: number
}

/** How Go answers a request: a status, a whole response, or an error thrown as the network failing. */
export type EventsAnswer = number | Response | Error

const encoder = new TextEncoder()

async function expectedSignature(
  secret: string,
  timestamp: string,
  body: string
): Promise<string> {
  const key = await crypto.subtle.importKey(
    "raw",
    encoder.encode(secret),
    { name: "HMAC", hash: "SHA-256" },
    false,
    ["sign"]
  )
  const mac = await crypto.subtle.sign(
    "HMAC",
    key,
    encoder.encode(`events.${timestamp}.${body}`)
  )
  return `v1=${Array.from(new Uint8Array(mac), (byte) => byte.toString(16).padStart(2, "0")).join("")}`
}

function refusal(status: number, code: string, message: string): Response {
  return Response.json({ error: { code, message } }, { status })
}

export class FakeGoEvents {
  readonly requests: EventsRequest[] = []
  /**
   * How the next requests are answered, in turn; a request not signed as Go
   * checks it is answered 401 whatever its turn says.
   */
  readonly answers: EventsAnswer[] = []
  private held: Promise<void> | undefined

  constructor(readonly secret: string = eventsSecret) {}

  /** Every event Go answered 204 for, in order. */
  get taken(): SentEvent[] {
    return this.requests
      .filter((request) => request.status === 204)
      .flatMap((request) => request.events)
  }

  /** Holds every answer until the returned release is called: a request stays on its way. */
  hold(): () => void {
    let release!: () => void
    this.held = new Promise<void>((resolve) => {
      release = resolve
    })
    return () => {
      this.held = undefined
      release()
    }
  }

  readonly fetch = vi.fn(
    async (input: URL | string, init: RequestInit = {}): Promise<Response> => {
      const headers = Object.fromEntries(new Headers(init.headers))
      const body = typeof init.body === "string" ? init.body : ""
      let events: SentEvent[] = []
      try {
        events = (JSON.parse(body) as { events: SentEvent[] }).events
      } catch {
        // Go says what it thinks of it below.
      }
      const request: EventsRequest = {
        url: String(input),
        method: init.method ?? "GET",
        headers,
        body,
        events,
        at: Date.now(),
      }
      this.requests.push(request)
      const response = await this.answer(request)
      request.status = response.status
      return response
    }
  )

  private async answer(request: EventsRequest): Promise<Response> {
    if (this.held) await this.held
    const next = this.answers.shift()
    if (next instanceof Error) throw next
    const timestamp = request.headers["x-oryxa-timestamp"] ?? ""
    const signature = request.headers["x-oryxa-signature"] ?? ""
    if (
      request.method !== "POST" ||
      request.headers["content-type"] !== "application/json" ||
      !/^\d+$/.test(timestamp) ||
      Math.abs(Date.now() / 1000 - Number(timestamp)) > 300 ||
      signature !==
        (await expectedSignature(this.secret, timestamp, request.body))
    ) {
      return refusal(401, "unauthorized", "The signature does not match.")
    }
    if (
      encoder.encode(request.body).length > 256 * 1024 ||
      request.events.length > 100
    ) {
      return refusal(413, "too_large", "At most 100 events and 256 KiB.")
    }
    if (next instanceof Response) return next
    if (next === undefined || next === 204) {
      return new Response(null, { status: 204 })
    }
    return refusal(next, "refused", `Go answered ${next}.`)
  }
}

/**
 * A `fetch` that sends the events path to `events` and everything else to
 * `other` (by default, an empty listing of agent requests, as a side
 * conversation reads Go).
 */
export function routeFetch(
  events: FakeGoEvents,
  other: (input: URL | string, init?: RequestInit) => Promise<Response> = () =>
    Promise.resolve(Response.json({ requests: [] }))
) {
  return vi.fn((input: URL | string, init?: RequestInit) =>
    new URL(String(input)).pathname === "/v1/realtime/events"
      ? events.fetch(input, init)
      : other(input, init)
  )
}
