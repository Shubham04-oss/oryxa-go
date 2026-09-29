// POST /internal/realtime/purge: how Go empties the conversations a member
// leaves behind (auth.member.removed.v1), a whole organization's
// (auth.organization.deleted.v1), and a teamspace's side conversations,
// those of a member who left its team (agents.teamspace.member.removed.v1)
// or all of them once it is deleted (agents.teamspace.deleted.v1). The same
// purges reach the teamspaces' rooms: a member who left is shut out of them,
// and a deleted organization's or teamspace's let go of what they held. No
// cookie: Go signs each request with a secret the two share. REALTIME.md
// holds the contract Go builds against.
import { getAgentByName } from "agents"
import type { ConversationEnv } from "./conversation-env"
import { inboxName, isNameId, registryName } from "./lib/dms/names"
import { teamspaceRoomAgentName } from "./lib/rooms/room-agent"
import { isPurgeEventId } from "./realtime-registry-agent"
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
 * A purge, as Go sends it (contract v3).
 *
 * - `event_id` names the event behind it (1 to 200 characters, unique per
 *   event): the same event again does nothing. A `teamspace_member` purge
 *   is carried out once by what it removes instead, as Go may send the
 *   same removal under a new event.
 * - `scope` says what goes, and `user_id` and `teamspace_id` must agree
 *   with it. An empty `user_id` never means everybody by itself.
 *   - `member`: the conversations of the member `user_id` (never empty).
 *   - `organization`: every member's; `user_id` is `""`.
 *   - `teamspace_member`: the side conversations in the teamspace
 *     `teamspace_id` of the member `user_id`, who left its team.
 *   - `teamspace`: every side conversation in the teamspace `teamspace_id`,
 *     which was deleted; `user_id` is `""`.
 * - `teamspace_id` (an id a name may hold) comes with the two teamspace
 *   scopes, and never with the others: Go leaves it out of their bodies.
 * - `removed_at` is when the member left, or the organization or the
 *   teamspace went, RFC 3339 with 0 to 9 fractional digits: in a member's
 *   conversations, only what is not newer than it goes. An organization's
 *   or a teamspace's purge takes everything.
 */
export type PurgeRequest = {
  event_id: string
  organization_id: string
  user_id: string
  removed_at: string
} & (
  | { scope: "member" | "organization"; teamspace_id?: never }
  | { scope: "teamspace_member" | "teamspace"; teamspace_id: string }
)

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
  const teamspaceId = fields.teamspace_id
  const removedAt = fields.removed_at
  if (
    !isPurgeEventId(eventId) ||
    !isNameId(organizationId) ||
    typeof userId !== "string" ||
    typeof removedAt !== "string" ||
    removalTime(removedAt) === undefined
  ) {
    return undefined
  }
  // The scope says what goes; `user_id` and `teamspace_id` must agree with
  // it. An empty `user_id` alone never means everybody.
  const common = { event_id: eventId, organization_id: organizationId }
  switch (scope) {
    case "member":
    case "organization":
      if (
        !(scope === "member" ? isNameId(userId) : userId === "") ||
        // Even an empty or null one: Go leaves it out.
        Object.prototype.hasOwnProperty.call(fields, "teamspace_id")
      ) {
        return undefined
      }
      return { ...common, scope, user_id: userId, removed_at: removedAt }
    case "teamspace_member":
    case "teamspace":
      if (
        !(scope === "teamspace_member" ? isNameId(userId) : userId === "") ||
        !isNameId(teamspaceId)
      ) {
        return undefined
      }
      return {
        ...common,
        scope,
        user_id: userId,
        teamspace_id: teamspaceId,
        removed_at: removedAt,
      }
    default:
      return undefined
  }
}

/**
 * `POST /internal/realtime/purge`, from Go. Checked in this order: the
 * method (405), the Worker's secret, trimmed (503 when it is not set or
 * shorter than `minPurgeSecretLength`), both headers (401), the timestamp
 * within `purgeWindowSeconds` of now (401), the signature's form (401), the
 * body at most `maxPurgeBodyBytes` (413), the signature itself, compared in
 * constant time (401), and the body (400).
 * Then the purge is carried out: a member's or an organization's by the
 * organization's registry (`RealtimeRegistryAgent.purge`), through every
 * room registered and every member's inbox; a teamspace's by the registry
 * too, through its room and every member's inbox
 * (`RealtimeRegistryAgent.purgeTeamspace`); and a member's in one teamspace
 * by that member's inbox (`InboxAgent.purgeTeamspace`) and the teamspace's
 * room (`TeamspaceRoomAgent.purgeMember`), both asked whichever fails. 204
 * once it is done, or was done already for this event, or found nothing to
 * purge; 503 when part of it failed, and Go sends it again.
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
      "A purge needs event_id, organization_id, a scope that user_id and teamspace_id agree with, and removed_at."
    )
  }
  const removedAt = removalTime(purge.removed_at) ?? ""
  try {
    if (purge.scope === "teamspace_member") {
      // The member's own inbox finds their side conversations there, and
      // remembers the removal itself, whatever the event that brings it.
      // The teamspace's room shuts them out. Both are asked, whichever
      // fails, and either failing asks Go for the event again.
      const done = await Promise.allSettled([
        getAgentByName(
          env.InboxAgent,
          inboxName(purge.organization_id, purge.user_id)
        ).then((inbox) => inbox.purgeTeamspace(purge.teamspace_id, removedAt)),
        getAgentByName(
          env.TeamspaceRoomAgent,
          teamspaceRoomAgentName(purge.organization_id, purge.teamspace_id)
        ).then((room) => room.purgeMember(purge.user_id, removedAt)),
      ])
      for (const outcome of done) {
        if (outcome.status === "rejected") throw outcome.reason
      }
    } else {
      const registry = await getAgentByName(
        env.RealtimeRegistryAgent,
        registryName(purge.organization_id)
      )
      if (purge.scope === "teamspace") {
        await registry.purgeTeamspace(purge.event_id, purge.teamspace_id)
      } else {
        const target: PurgeTarget =
          purge.scope === "organization"
            ? { scope: "organization" }
            : { scope: "member", userId: purge.user_id, removedAt }
        await registry.purge(purge.event_id, target)
      }
    }
  } catch (error) {
    console.error("realtime purge: a purge did not finish", error)
    return refuse(503, "unavailable", "The purge did not finish. Try again.")
  }
  return new Response(null, { status: 204 })
}
