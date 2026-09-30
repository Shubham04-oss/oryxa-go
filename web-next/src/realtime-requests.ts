// POST /internal/realtime/requests: how Go tells the Worker that an agent
// request between two members moved (agents.interaction.requested.v1,
// .decided.v1 or .completed.v1, which Go's realtime service consumes). The
// Worker notes it in the two people's direct message and in both their
// inboxes, so a page hears of it over its socket instead of at its next
// poll. A note is identities and a state: who asked whose agent, where the
// request was made, its revision and the state it reached, and when. Nothing
// of what was asked or answered is in it, and nothing in it is shown as
// content: a page reads the request from Go, as the member. No cookie: Go
// signs each request with the secret the purge's are signed with, over a
// string that starts with its own prefix. REALTIME.md holds the contract Go
// builds against.
import { getAgentByName } from "agents"
import type { ConversationEnv } from "./conversation-env"
import { directMessageName, inboxName, isNameId } from "./lib/dms/names"
import { readRequestNote } from "./lib/dms/request-notes"
import type { RequestNote } from "./lib/dms/request-notes"
import {
  minPurgeSecretLength,
  purgeRequestHeaders,
  purgeWindowSeconds,
  realtimeSignature,
  removalTime,
} from "./realtime-purge"

/** Where the Worker takes a request note. Not under `/agents/`: no page reaches it. */
export const REALTIME_REQUESTS_PATH = "/internal/realtime/requests"

/** The headers of a signed request note: a purge's, signed differently. */
export const requestNoteHeaders = purgeRequestHeaders

/**
 * What a note's signature is made over, ahead of its timestamp: `requests.`.
 * A purge's string starts with its timestamp, which is digits, and the
 * Worker's own events to Go with `events.`, so no signature made for one
 * verifies as another's.
 */
export const REQUESTS_SIGNATURE_PREFIX = "requests"

/** The largest body a note may have, in bytes: a real one is a few hundred. */
export const maxRequestNoteBodyBytes = 4 * 1024

/**
 * A note as Go sends it (contract v1), all eight keys present:
 * `{"organization_id","request_id","revision","asker_user_id","owner_user_id","origin","state","changed_at"}`.
 * Each id is 1 to 128 of `A-Z a-z 0-9 _ -` (a name may hold them), and the
 * asker and the owner are two different people; `revision` is the request's
 * own, from 1; `origin` a short lowercase word (`agent`, `slack`); `state`
 * one of `REQUEST_NOTE_STATES`; `changed_at` RFC 3339 (0 to 9 fractional
 * digits, as `removalTime` reads it). Keys the contract does not name are
 * ignored, so either side can deploy first.
 */
export type RequestNoteBody = {
  organization_id: string
  request_id: string
  revision: number
  asker_user_id: string
  owner_user_id: string
  origin: string
  state: string
  changed_at: string
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

/**
 * The `X-Oryxa-Signature` value for a note: `v1=` and the lowercase hex
 * HMAC-SHA256, keyed with `secret`, of `requests.{timestamp}.{body}`. Go
 * computes the same; the tests and the local smoke test sign with it.
 */
export function signRequests(
  secret: string,
  timestamp: string,
  body: string | Uint8Array
): Promise<string> {
  return realtimeSignature(
    secret,
    `${REQUESTS_SIGNATURE_PREFIX}.${timestamp}`,
    body
  )
}

/** Whether two strings are equal, in a time that does not depend on where they differ. */
function sameText(left: string, right: string): boolean {
  const a = encoder.encode(left)
  const b = encoder.encode(right)
  if (a.length !== b.length) return false
  let difference = 0
  for (let index = 0; index < a.length; index += 1) {
    difference |= a[index] ^ b[index]
  }
  return difference === 0
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

/** A note with the organization it is in: what a verified body asks the Worker to note. */
export type ParsedRequestNote = { organizationId: string; note: RequestNote }

/**
 * The note a verified body asks for, or nothing for one that is not a note:
 * not a JSON object, an id a name cannot hold, the two people the same, a
 * revision under 1, an origin or a state that is not one, or a time that is
 * not a real one. Its time is read as a purge's is (`removalTime`), to the
 * millisecond.
 */
export function parseRequestNote(
  body: Uint8Array
): ParsedRequestNote | undefined {
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
  const organizationId = fields.organization_id
  const changedAt = removalTime(fields.changed_at)
  if (!isNameId(organizationId) || changedAt === undefined) return undefined
  const note = readRequestNote({
    requestId: fields.request_id,
    revision: fields.revision,
    askerId: fields.asker_user_id,
    ownerId: fields.owner_user_id,
    origin: fields.origin,
    state: fields.state,
    changedAt,
  })
  return note ? { organizationId, note } : undefined
}

/**
 * `POST /internal/realtime/requests`, from Go. Checked in this order, as the
 * purge's is: the method (405), the Worker's secret, trimmed (503 when it is
 * not set or shorter than `minPurgeSecretLength`), both headers (401), the
 * timestamp within `purgeWindowSeconds` of now (401), the signature's form
 * (401), the body at most `maxRequestNoteBodyBytes` (413), the signature
 * itself over `requests.{timestamp}.{body}`, compared in constant time (401),
 * and the body (400). A purge's signature of the same body fails at the
 * signature, and a note's on the purge's route.
 *
 * Then the two people's direct message and both their inboxes are told, at
 * once (`DirectMessageAgent.noteRequest`, `InboxAgent.noteRequest`), each of
 * which keeps a request's highest revision and does nothing with a note no
 * newer: 204 once every one has, whether or not it changed anything, and
 * whether this note was told before; 503 when any failed, and Go sends the
 * note again, which the ones that did not fail take as a repeat. A note's
 * time is never later than the Worker's clock: a request that reached its
 * state "in the future" (Go's clock ahead) is noted as of now, so it cannot
 * hold an inbox's top for good.
 */
export async function handleRequests(
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
      "realtime requests: REALTIME_PURGE_SECRET is missing or shorter than 32 characters"
    )
    return refuse(503, "unavailable", "Request notes are not configured.")
  }
  const timestamp = request.headers.get(requestNoteHeaders.timestamp)
  const signature = request.headers.get(requestNoteHeaders.signature)
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
  if (!/^v1=[0-9a-fA-F]{64}$/.test(signature)) {
    return refuse(401, "invalid_signature", "The signature does not match.")
  }
  const body = await readAtMost(request, maxRequestNoteBodyBytes)
  if (!body) {
    return refuse(413, "too_large", "A request note's body is at most 4 KB.")
  }
  const expected = await signRequests(secret, timestamp, body)
  if (!sameText(expected, signature.toLowerCase())) {
    return refuse(401, "invalid_signature", "The signature does not match.")
  }
  const parsed = parseRequestNote(body)
  if (!parsed) {
    return refuse(
      400,
      "invalid_request",
      "A request note needs organization_id, request_id, revision, asker_user_id and owner_user_id (two different people), origin, state and changed_at."
    )
  }
  const { organizationId, note } = parsed
  const latest = new Date(now).toISOString()
  const told: RequestNote =
    note.changedAt > latest ? { ...note, changedAt: latest } : note
  const conversation = directMessageName(
    organizationId,
    told.askerId,
    told.ownerId
  )
  const outcomes = await Promise.allSettled([
    getAgentByName(env.DirectMessageAgent, conversation).then((dm) =>
      dm.noteRequest(told)
    ),
    getAgentByName(
      env.InboxAgent,
      inboxName(organizationId, told.askerId)
    ).then((inbox) =>
      inbox.noteRequest(
        { kind: "dm", name: conversation, peerId: told.ownerId },
        told
      )
    ),
    getAgentByName(
      env.InboxAgent,
      inboxName(organizationId, told.ownerId)
    ).then((inbox) =>
      inbox.noteRequest(
        { kind: "dm", name: conversation, peerId: told.askerId },
        told
      )
    ),
  ])
  for (const outcome of outcomes) {
    if (outcome.status === "rejected") {
      console.error("realtime requests: a note did not finish", outcome.reason)
      return refuse(503, "unavailable", "The note did not finish. Try again.")
    }
  }
  return new Response(null, { status: 204 })
}
