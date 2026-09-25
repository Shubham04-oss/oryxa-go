// POST /internal/realtime/purge: how Go empties the conversations a member
// leaves behind (auth.member.removed.v1) or a whole organization's
// (auth.organization.deleted.v1). No cookie: Go signs each request with a
// secret the two share. REALTIME.md holds the contract Go builds against.
import { getAgentByName } from "agents"
import type { ConversationEnv } from "./conversation-env"
import { isNameId, registryName } from "./lib/dms/names"
import { maxPurgeEventId } from "./realtime-registry-agent"
import type { PurgeTarget } from "./realtime-registry-agent"

/** Where the Worker takes a purge. Not under `/agents/`: no page reaches it. */
export const REALTIME_PURGE_PATH = "/internal/realtime/purge"

/** The headers of a signed purge. */
export const purgeRequestHeaders = {
  /** When Go signed it, in Unix seconds. */
  timestamp: "X-Oryxa-Timestamp",
  /** `v1=` and the lowercase hex HMAC-SHA256 of `{timestamp}.{raw body}`. */
  signature: "X-Oryxa-Signature",
} as const

/** How far a request's timestamp may be from the Worker's clock, either way. */
export const purgeWindowSeconds = 300

/** The shortest secret the Worker signs with; a shorter one refuses every purge. */
export const minPurgeSecretLength = 32

/** The largest body a purge may have, in bytes. */
export const maxPurgeBodyBytes = 4 * 1024

/**
 * A purge, as Go sends it (contract v2).
 *
 * - `event_id` names the event behind it (1 to 200 characters, unique per
 *   event): the same event again does nothing.
 * - `scope` says what goes: `member` (the member `user_id`, never empty) or
 *   `organization` (every member; `user_id` is then `""`). An empty
 *   `user_id` never means the organization by itself.
 * - `removed_at` is when the member left, RFC 3339 with 0 to 9 fractional
 *   digits: in a member's conversations, only what is not newer than it
 *   goes. An organization's purge takes everything.
 */
export type PurgeRequest = {
  event_id: string
  organization_id: string
  scope: PurgeTarget["scope"]
  user_id: string
  removed_at: string
}

// RFC 3339's date-time, strictly: the date, `T`, the time, 1 to 9
// fractional digits, and `Z` or an offset. Its numbers are checked apart.
const rfc3339 =
  /^(\d{4})-(\d{2})-(\d{2})[Tt](\d{2}):(\d{2}):(\d{2})(?:\.(\d{1,9}))?(?:[Zz]|([+-])(\d{2}):(\d{2}))$/

/**
 * An RFC 3339 time as the purge hands it on (`toISOString`: UTC, to the
 * millisecond), or nothing for one that is not a real time (a 30th of
 * February, 24:00, a leap second, an offset past 23:59).
 *
 * The digits past the millisecond go. Messages record when they were written
 * to the millisecond, and for those, "at or before `removed_at`" is the same
 * as "at or before its millisecond": a message written in that millisecond
 * counts as at or before it, and one written in the next does not.
 */
export function removalTime(value: unknown): string | undefined {
  if (typeof value !== "string") return undefined
  const match = rfc3339.exec(value)
  if (!match) return undefined
  const [, year, month, day, hour, minute, second, fraction = ""] = match
  const [sign, offsetHours = "0", offsetMinutes = "0"] = match.slice(8)
  const numbers = [year, month, day, hour, minute, second].map(Number)
  const [y, mo, d, h, mi, se] = numbers
  const daysInMonth = new Date(Date.UTC(y, mo, 0)).getUTCDate()
  if (
    y < 1970 ||
    mo < 1 ||
    mo > 12 ||
    d < 1 ||
    d > daysInMonth ||
    h > 23 ||
    mi > 59 ||
    se > 59 ||
    Number(offsetHours) > 23 ||
    Number(offsetMinutes) > 59
  ) {
    return undefined
  }
  const offset =
    (sign === "-" ? -1 : 1) *
    (Number(offsetHours) * 60 + Number(offsetMinutes)) *
    60_000
  const milliseconds = Number(fraction.slice(0, 3).padEnd(3, "0"))
  return new Date(
    Date.UTC(y, mo - 1, d, h, mi, se, milliseconds) - offset
  ).toISOString()
}

const encoder = new TextEncoder()

/** An error in Go's shape, as the rest of the Worker answers. */
function refuse(
  status: number,
  code: string,
  message: string,
  headers?: HeadersInit
): Response {
  return Response.json({ error: { code, message } }, { status, headers })
}

async function hmacSha256(
  secret: string,
  data: Uint8Array<ArrayBuffer>
): Promise<Uint8Array> {
  const key = await crypto.subtle.importKey(
    "raw",
    encoder.encode(secret),
    { name: "HMAC", hash: "SHA-256" },
    false,
    ["sign"]
  )
  return new Uint8Array(await crypto.subtle.sign("HMAC", key, data))
}

/**
 * What is signed: `prefix`, a dot, and the body's bytes as sent. A purge's
 * prefix is its timestamp as sent.
 */
function signedBytes(
  prefix: string,
  body: Uint8Array
): Uint8Array<ArrayBuffer> {
  const head = encoder.encode(`${prefix}.`)
  const bytes = new Uint8Array(head.length + body.length)
  bytes.set(head)
  bytes.set(body, head.length)
  return bytes
}

function hex(bytes: Uint8Array): string {
  return Array.from(bytes, (byte) => byte.toString(16).padStart(2, "0")).join(
    ""
  )
}

function fromHex(text: string): Uint8Array {
  const bytes = new Uint8Array(text.length / 2)
  for (let index = 0; index < bytes.length; index += 1) {
    bytes[index] = parseInt(text.slice(index * 2, index * 2 + 2), 16)
  }
  return bytes
}

/** Whether two byte strings are equal, in a time that does not depend on where they differ. */
function sameBytes(left: Uint8Array, right: Uint8Array): boolean {
  if (left.length !== right.length) return false
  let difference = 0
  for (let index = 0; index < left.length; index += 1) {
    difference |= left[index] ^ right[index]
  }
  return difference === 0
}

/**
 * An `X-Oryxa-Signature` value between Go and the Worker: `v1=` and the
 * lowercase hex HMAC-SHA256, keyed with `secret`, of `{prefix}.{body}`. A
 * purge signs its timestamp as the prefix (`signPurge`); the conversation
 * events the Worker sends Go sign `events.{timestamp}` (`signEvents`, in
 * src/realtime-events.ts), so neither direction's signature ever verifies
 * as the other's.
 */
export async function realtimeSignature(
  secret: string,
  prefix: string,
  body: string | Uint8Array
): Promise<string> {
  const bytes = typeof body === "string" ? encoder.encode(body) : body
  return `v1=${hex(await hmacSha256(secret, signedBytes(prefix, bytes)))}`
}

/**
 * The `X-Oryxa-Signature` value for a purge: `v1=` and the hex
 * HMAC-SHA256, keyed with `secret`, of `{timestamp}.{body}`. Go computes the
 * same; the tests and the local smoke test sign with it.
 */
export function signPurge(
  secret: string,
  timestamp: string,
  body: string | Uint8Array
): Promise<string> {
  return realtimeSignature(secret, timestamp, body)
}

/** The body, or nothing when it holds more than `limit` bytes. */
async function readAtMost(
  request: Request,
  limit: number
): Promise<Uint8Array | undefined> {
  const reader = request.body?.getReader()
  if (!reader) return new Uint8Array()
  const chunks: Uint8Array[] = []
  let size = 0
  for (;;) {
    const { done, value } = await reader.read()
    if (done) break
    size += value.length
    if (size > limit) {
      await reader.cancel().catch(() => undefined)
      return undefined
    }
    chunks.push(value)
  }
  const body = new Uint8Array(size)
  let offset = 0
  for (const chunk of chunks) {
    body.set(chunk, offset)
    offset += chunk.length
  }
  return body
}

/** The purge a verified body asks for, or nothing for one that is not a purge. */
export function parsePurgeRequest(body: Uint8Array): PurgeRequest | undefined {
  let value: unknown
  try {
    value = JSON.parse(new TextDecoder("utf-8", { fatal: true }).decode(body))
  } catch {
    return undefined
  }
  if (typeof value !== "object" || value === null || Array.isArray(value)) {
    return undefined
  }
  const fields = value as Record<string, unknown>
  const eventId = fields.event_id
  const organizationId = fields.organization_id
  const scope = fields.scope
  const userId = fields.user_id
  const removedAt = fields.removed_at
  if (
    typeof eventId !== "string" ||
    eventId === "" ||
    eventId.length > maxPurgeEventId ||
    !isNameId(organizationId) ||
    // The scope says what goes; `user_id` must agree with it. An empty
    // `user_id` alone never means the organization.
    !(
      (scope === "member" && isNameId(userId)) ||
      (scope === "organization" && userId === "")
    ) ||
    typeof removedAt !== "string" ||
    removalTime(removedAt) === undefined
  ) {
    return undefined
  }
  return {
    event_id: eventId,
    organization_id: organizationId,
    scope,
    user_id: userId,
    removed_at: removedAt,
  }
}

/**
 * `POST /internal/realtime/purge`, from Go. Checked in this order: the
 * method (405), the Worker's secret, trimmed (503 when it is not set or
 * shorter than `minPurgeSecretLength`), both headers (401), the timestamp
 * within `purgeWindowSeconds` of now (401), the signature's form (401), the
 * body at most `maxPurgeBodyBytes` (413), the signature itself, compared in
 * constant time (401), and the body (400).
 * Then the organization's registry carries the purge out
 * (`RealtimeRegistryAgent.purge`): 204 once it is done, or was done already
 * for this event; 503 when part of it failed, and Go sends it again.
 */
export async function handlePurge(
  request: Request,
  env: ConversationEnv,
  now: number = Date.now()
): Promise<Response> {
  if (request.method !== "POST") {
    return refuse(405, "method_not_allowed", "Method not allowed.", {
      Allow: "POST",
    })
  }
  // Trimmed, as Go trims its own: a secret stored with a trailing newline on
  // either side still signs and verifies the same.
  const secret = env.REALTIME_PURGE_SECRET?.trim()
  if (!secret || secret.length < minPurgeSecretLength) {
    console.error(
      "realtime purge: REALTIME_PURGE_SECRET is missing or shorter than 32 characters"
    )
    return refuse(503, "unavailable", "Purging is not configured.")
  }
  const timestamp = request.headers.get(purgeRequestHeaders.timestamp)
  const signature = request.headers.get(purgeRequestHeaders.signature)
  if (!timestamp || !signature) {
    return refuse(401, "unauthorized", "A signed request is required.")
  }
  if (
    !/^\d{1,12}$/.test(timestamp) ||
    Math.abs(now / 1000 - Number(timestamp)) > purgeWindowSeconds
  ) {
    return refuse(
      401,
      "stale_request",
      "The request's timestamp is not within five minutes of now."
    )
  }
  const given = /^v1=([0-9a-fA-F]{64})$/.exec(signature)
  if (!given) {
    return refuse(401, "invalid_signature", "The signature does not match.")
  }
  const body = await readAtMost(request, maxPurgeBodyBytes)
  if (!body) {
    return refuse(413, "too_large", "A purge's body is at most 4 KB.")
  }
  const expected = await hmacSha256(secret, signedBytes(timestamp, body))
  if (!sameBytes(expected, fromHex(given[1]))) {
    return refuse(401, "invalid_signature", "The signature does not match.")
  }
  const purge = parsePurgeRequest(body)
  if (!purge) {
    return refuse(
      400,
      "invalid_request",
      "A purge needs event_id, organization_id, a scope that agrees with user_id, and removed_at."
    )
  }
  const target: PurgeTarget =
    purge.scope === "organization"
      ? { scope: "organization" }
      : {
          scope: "member",
          userId: purge.user_id,
          removedAt: removalTime(purge.removed_at) ?? "",
        }
  try {
    const registry = await getAgentByName(
      env.RealtimeRegistryAgent,
      registryName(purge.organization_id)
    )
    await registry.purge(purge.event_id, target)
  } catch (error) {
    console.error("realtime purge: a purge did not finish", error)
    return refuse(503, "unavailable", "The purge did not finish. Try again.")
  }
  return new Response(null, { status: 204 })
}
