import { queryOptions } from "@tanstack/react-query"
import {
  ApiError,
  apiFetch,
  apiRequest,
  apiUrl,
  requestError,
} from "@/lib/api/client"
import type { SpecialistResult } from "@/lib/specialists/result"
import { threadAgentName } from "./chat-messages"
import type { ThreadChatMessage } from "./chat-messages"
import { readItemStream } from "./event-stream"
import type { RunResult } from "./event-stream"

// Everything here mirrors agents/primary/internal/threads (items.go,
// threads.go, run.go) and the routes in agents/primary/internal/httpserver.

/** A member's conversation with their primary agent (Go: `threads.Thread`). */
export type Thread = {
  id: string
  /** The first line of the thread's first message, at most 80 characters; "" until then. */
  title: string
  /** RFC 3339. Left out for a thread the run API did not start. */
  created_at?: string
  /** RFC 3339. `listThreads` answers newest first by it. */
  updated_at: string
}

/** A thread and everything that happened in it (Go: `threads.Detail`). */
export type ThreadDetail = Thread & {
  /** Oldest first. The same items a run streams, without deltas or `run_*` items. */
  items: Item[]
  /**
   * Call ids of the confirmations waiting for the member, in the order they
   * were asked. The next run has to answer every one of them, together, and
   * a message is refused with `confirmation_pending` until it does.
   */
  pending_confirmations: string[]
}

export type ItemType =
  | "message"
  | "delta"
  | "tool_call"
  | "tool_result"
  | "confirmation"
  | "artifact"
  | "run_started"
  | "run_completed"
  | "run_failed"

export type ConfirmationStatus = "pending" | "approved" | "declined"

/** A run's token usage (Go: `agentsv1.Usage`). */
export type ItemUsage = {
  input_tokens: number
  output_tokens: number
  /** Input tokens served from a cache; part of `input_tokens`. */
  cached_tokens?: number
  total_tokens: number
}

/**
 * One thing that happened in a thread (Go: `threads.Item`). Go sends one flat
 * object whose `type` says which fields are set, and leaves out empty ones:
 *
 * | type            | fields                                                              |
 * | --------------- | ------------------------------------------------------------------- |
 * | `message`       | id, at, author, text, error                                         |
 * | `delta`         | at, author, text (a piece of the next `message`; never stored)     |
 * | `tool_call`     | id, at, author, call_id, tool, arguments (left out above 16 KiB)    |
 * | `tool_result`   | id, at, author, call_id, tool, failed, error (never any text)       |
 * | `confirmation`  | id, at, author, call_id, tool_call_id, tool, arguments, hint, status |
 * | `artifact`      | id, at, author, artifact, version                                   |
 * | `run_started`   | run_id, at                                                          |
 * | `run_completed` | run_id, at, usage                                                   |
 * | `run_failed`    | run_id, at, error; code only from the thread's Durable Object       |
 *
 * The chat Durable Objects add two fields Go never sets: `code` on a
 * `run_failed` they write, and `result` on a specialist's message.
 *
 * A thread's history carries the stored event's `id` on every item, so what
 * a client streamed can be matched against what it reads later. One stored
 * event can make several items (a message and the tool calls after it, or
 * several files), and they share that `id`.
 */
export type Item = {
  type: ItemType
  /** The stored event the item came from. Deltas and `run_*` items have none. */
  id?: string
  /** When it happened, RFC 3339 in UTC. */
  at?: string
  /**
   * `"member"`, `"apps"` for a batch of events from the member's connected
   * apps that woke the agent, or the display name of the agent that wrote it:
   * the member's primary agent, a specialist, or a sub-agent such as
   * `"researcher"` that the primary agent handed the turn to.
   */
  author?: string
  /** A message's or delta's text. */
  text?: string
  /**
   * On a message: why the model stopped without (fully) answering. On a
   * tool_result: the tool's error. On run_failed: words the member can act on.
   */
  error?: string
  /**
   * Set only by the thread's Durable Object, on a run_failed it writes itself:
   * `"session_expired"` (see `sessionExpiredCode`) when Go answered 401, or
   * Go's own refusal code (`run_in_progress`, `confirmation_pending`,
   * `not_found`, ...) when Go would not start the run. Go never sets it on
   * the items it streams.
   */
  code?: string
  /**
   * Set only by the specialist chat's Durable Object, on a specialist's
   * message that is a JSON work result: the result, parsed, while `text`
   * holds its summary (`readableSpecialistItem`). Go never sets it.
   */
  result?: SpecialistResult
  /** Pairs a tool_result with its tool_call. On a confirmation, the id the member's answer names. */
  call_id?: string
  /** On a confirmation: the tool_call it holds back. */
  tool_call_id?: string
  tool?: string
  /** What the tool was called with. A confirmation always carries them: nobody approves what they cannot see. */
  arguments?: Record<string, unknown>
  /** A tool_result that ended in an error. */
  failed?: boolean
  /** A confirmation's reason in the tool's own words; left out when ADK had only its developer-facing default. */
  hint?: string
  /** A confirmation's state. A streamed confirmation is always `pending`; history says how it was answered. */
  status?: ConfirmationStatus
  /** An artifact's file name, which never holds a "/". A `"user:"` prefix marks the member's own file, shared by all their threads. */
  artifact?: string
  /** An artifact's version, counted from 1. */
  version?: number
  run_id?: string
  usage?: ItemUsage
}

/** The member's decision on one waiting confirmation. */
export type ConfirmationAnswer = {
  /** The confirmation's `call_id`. */
  id: string
  confirmed: boolean
}

/** What a run is sent: a message, or answers to every waiting confirmation. Never both. */
export type RunInput =
  { message: string } | { confirmations: ConfirmationAnswer[] }

/** The error codes the thread and run routes answer with (httpserver `ErrorCode`), as `ApiError.code`. */
export type ThreadErrorCode =
  | "invalid_request"
  | "unauthorized"
  | "forbidden"
  | "not_found"
  | "run_in_progress"
  | "confirmation_pending"
  | "agent_disabled"
  | "agents_unavailable"
  | "rate_limited"
  | "unavailable"
  | "internal"

/** The longest message Go accepts, in Unicode code points after trimming (threads.go `maxMessageRunes`). */
export const MAX_MESSAGE_LENGTH = 32_000

type RequestOptions = { signal?: AbortSignal }

const organizationPath = (organizationId: string) =>
  `/v1/organizations/${encodeURIComponent(organizationId)}`
const threadsPath = (organizationId: string) =>
  `${organizationPath(organizationId)}/threads`
const threadPath = (organizationId: string, threadId: string) =>
  `${threadsPath(organizationId)}/${encodeURIComponent(threadId)}`

/** GET /v1/organizations/{org}/threads, most recently active first. agents/primary/internal/httpserver/server.go listThreads */
export async function listThreads(
  organizationId: string,
  { signal }: RequestOptions = {}
): Promise<Thread[]> {
  const { threads } = await apiRequest<{ threads: Thread[] }>(
    threadsPath(organizationId),
    { signal }
  )
  return threads
}

/** POST /v1/organizations/{org}/threads, answering 201 with the new, empty thread. agents/primary/internal/httpserver/server.go createThread */
export function createThread(organizationId: string): Promise<Thread> {
  // Go reads no field yet and refuses unknown ones. It sends an empty object
  // rather than no body: createThread decodes any body whose length is not
  // exactly 0, so a bodyless POST that reached it with an unknown length
  // would be refused.
  return apiRequest<Thread>(threadsPath(organizationId), { body: {} })
}

/** GET /v1/organizations/{org}/threads/{thread}, with its history. agents/primary/internal/httpserver/server.go getThread */
export function getThread(
  organizationId: string,
  threadId: string,
  { signal }: RequestOptions = {}
): Promise<ThreadDetail> {
  return apiRequest<ThreadDetail>(threadPath(organizationId, threadId), {
    signal,
  })
}

/**
 * DELETE /v1/organizations/{org}/threads/{thread} (204), with the files its
 * runs kept; the member's own `user:` files stay. This is Go alone: a thread
 * the chat Durable Object has held also has a transcript there, which
 * `deleteThreadChat` removes too. agents/primary/internal/httpserver/server.go deleteThread
 */
export async function deleteThread(
  organizationId: string,
  threadId: string
): Promise<void> {
  await apiRequest(threadPath(organizationId, threadId), { method: "DELETE" })
}

/** Where the thread's chat Durable Object answers HTTP, on the app's own origin (`/agents/{class}/{name}`). */
export function threadChatPath(
  organizationId: string,
  threadId: string
): string {
  return `/agents/thread-chat-agent/${encodeURIComponent(threadAgentName(organizationId, threadId))}`
}

/**
 * DELETE /agents/thread-chat-agent/{org}~{thread}: the Worker (src/server.ts)
 * forwards Go's DELETE and, only once Go has deleted the thread, clears the
 * Durable Object's stored transcript; it answers with Go's response, so a
 * refusal carries Go's code. Not a Go route. Delete through this whenever
 * the conversation used the Durable Object.
 */
export async function deleteThreadChat(
  organizationId: string,
  threadId: string
): Promise<void> {
  await apiRequest(threadChatPath(organizationId, threadId), {
    method: "DELETE",
  })
}

/**
 * GET /agents/thread-chat-agent/{org}~{thread}/get-messages: the transcript
 * the Durable Object stored, which `reconcileChatMessages` merges with
 * `getThread`'s history. Through `apiFetch`, so an expired cookie is
 * refreshed first (the Worker checks it with Go before answering).
 */
export async function getThreadChatMessages(
  organizationId: string,
  threadId: string,
  { signal }: RequestOptions = {}
): Promise<ThreadChatMessage[]> {
  const messages = await apiRequest<ThreadChatMessage[] | undefined>(
    `${threadChatPath(organizationId, threadId)}/get-messages`,
    { signal }
  )
  return messages ?? []
}

function invalidRun(message: string): ApiError {
  return new ApiError(message, 400, "invalid_request")
}

/**
 * The body a run is sent, checked the way Go checks it (run.go `StartRun`, and
 * the handler's confirmation check), with Go's own words. Only the known
 * fields go out: Go refuses a body with any other field. Checking here first
 * matters most for a specialist, where Go allocates a new conversation before
 * it looks at the input.
 */
export function runRequestBody(input: RunInput): RunInput {
  // Read as data rather than trusted as typed: pages build it from state.
  const { message, confirmations } = input as {
    message?: unknown
    confirmations?: unknown
  }
  const text = typeof message === "string" ? message : ""
  const answers: unknown[] = Array.isArray(confirmations) ? confirmations : []
  if (text.trim() !== "" && answers.length > 0) {
    throw invalidRun("send a message or answer confirmations, not both at once")
  }
  if (answers.length > 0) {
    if (!answers.every(isConfirmationAnswer)) {
      throw invalidRun(
        "every confirmation needs an id and whether it is confirmed"
      )
    }
    return {
      confirmations: answers.map(({ id, confirmed }) => ({ id, confirmed })),
    }
  }
  if (text.trim() === "") {
    throw invalidRun("send a message or answer the waiting confirmations")
  }
  if (Array.from(text.trim()).length > MAX_MESSAGE_LENGTH) {
    throw invalidRun(`a message holds at most ${MAX_MESSAGE_LENGTH} characters`)
  }
  return { message: text }
}

function isConfirmationAnswer(value: unknown): value is ConfirmationAnswer {
  if (typeof value !== "object" || value === null) return false
  const { id, confirmed } = value as { id?: unknown; confirmed?: unknown }
  return typeof id === "string" && id !== "" && typeof confirmed === "boolean"
}

export type StartRunOptions = {
  /** Called with every item the run streams, in order, until it ends. */
  onItem: (item: Item) => void
  /**
   * Stops the run: the request and its stream are cancelled, Go sees the
   * member leave and ends the run ("The run was cancelled."), and the call
   * resolves `{ outcome: "aborted" }` rather than throwing.
   */
  signal?: AbortSignal
}

/**
 * POSTs a run and reads its stream. Shared by `startRun` and
 * `startSpecialistRun`; `onResponse` sees the accepted response's headers
 * before the first item.
 */
export async function streamRunRequest(
  path: string,
  body: Record<string, unknown>,
  {
    onItem,
    onResponse,
    signal,
  }: StartRunOptions & { onResponse?: (response: Response) => void }
): Promise<RunResult> {
  let response: Response
  try {
    // apiFetch refreshes an expired session and sends again once, all before
    // Go opens the stream: a refusal is always a plain JSON error.
    response = await apiFetch(path, { body, method: "POST", signal })
  } catch (error) {
    if (signal?.aborted) return { outcome: "aborted" }
    throw error
  }
  if (signal?.aborted) {
    response.body?.cancel().catch(() => undefined)
    return { outcome: "aborted" }
  }
  if (!response.ok) throw await requestError(response)
  const runId = response.headers.get("X-Oryxa-Run") ?? undefined
  onResponse?.(response)
  if (!response.body) return { outcome: "incomplete", runId }
  const result = await readItemStream(response.body, { onItem, signal })
  return result.runId || !runId ? result : { ...result, runId }
}

/**
 * POST /v1/organizations/{org}/threads/{thread}/runs, reading the answer as
 * it streams. agents/primary/internal/httpserver/server.go run
 *
 * Throws `ApiError` when Go refuses the run before streaming, with its code:
 * `invalid_request` (400, also checked here first), `not_found` (404),
 * `run_in_progress` or `confirmation_pending` (409), `agents_unavailable`
 * (503). A session that expired is refreshed and the request sent once more
 * before any of that. Once the stream opens, the run reports its own end: it
 * resolves with `RunResult`, `failed` carrying `run_failed`'s words. It
 * rejects only for a network failure before any answer, or an error thrown
 * by `onItem`.
 *
 * This is the direct browser transport. The primary chat runs through the
 * thread's Durable Object, which calls Go itself and reads the stream with
 * `readItemStream`; see the README.
 */
export async function startRun(
  organizationId: string,
  threadId: string,
  input: RunInput,
  options: StartRunOptions
): Promise<RunResult> {
  return await streamRunRequest(
    `${threadPath(organizationId, threadId)}/runs`,
    runRequestBody(input),
    options
  )
}

function artifactPath(
  organizationId: string,
  threadId: string,
  name: string,
  version?: number
): string {
  // The whole name is one path segment: Go's route is `{name}`, and ADK
  // refuses a name with "/" in it, so "%2F" at worst answers 404.
  const query =
    version !== undefined && Number.isInteger(version) && version >= 1
      ? `?version=${version}`
      : ""
  return `${threadPath(organizationId, threadId)}/artifacts/${encodeURIComponent(name)}${query}`
}

/**
 * GET /v1/organizations/{org}/threads/{thread}/artifacts/{name}?version=, as
 * a plain URL for `<img src>` or `<a href>`: the browser sends the session
 * cookie itself. With no version, Go serves the latest. Go serves images,
 * PDFs and plain text inline and everything else as a download, always
 * sandboxed. agents/primary/internal/httpserver/server.go artifact
 *
 * The browser's own request cannot refresh an expired session. Right after a
 * page read the thread through `apiFetch` the cookie is fresh; for a click
 * that may come much later, use `fetchArtifactLink`.
 */
export function artifactContentUrl(
  organizationId: string,
  threadId: string,
  name: string,
  version?: number
): string {
  return apiUrl(artifactPath(organizationId, threadId, name, version))
}

export type ArtifactLink = {
  /** The same URL `artifactContentUrl` gives, with the session cookie just refreshed. */
  url: string
  /** The file's media type, as Go serves it. */
  contentType: string
  /** Whether Go lets the browser show it in place (images, PDF, plain text); anything else downloads. */
  inline: boolean
}

/**
 * Reads an artifact's headers through `apiFetch`, then hands back its plain
 * URL for the browser to open or download. agents/primary/internal/httpserver/server.go artifact
 *
 * The browser's own navigation cannot refresh an expired session; this
 * request can, and it fails in the page, as an `ApiError` (`not_found`), for
 * a file that is gone, instead of on a browser error page. The trade-off: Go
 * reads the file twice (the body here is cancelled after the headers, so it
 * is not downloaded twice), and the cookie could still expire in the moment
 * between this and the navigation. It does not fetch the bytes into a
 * `blob:` URL instead: that URL would run in the app's own origin, outside
 * the sandbox Go serves every artifact with, and HTML a run kept could then
 * run as the app.
 */
export async function fetchArtifactLink(
  organizationId: string,
  threadId: string,
  artifact: { name: string; version?: number },
  { signal }: RequestOptions = {}
): Promise<ArtifactLink> {
  const path = artifactPath(
    organizationId,
    threadId,
    artifact.name,
    artifact.version
  )
  const response = await apiFetch(path, { signal })
  if (!response.ok) throw await requestError(response)
  await response.body?.cancel().catch(() => undefined)
  return {
    url: apiUrl(path),
    contentType:
      response.headers.get("Content-Type") ?? "application/octet-stream",
    inline: /^\s*inline\b/i.test(
      response.headers.get("Content-Disposition") ?? ""
    ),
  }
}

/**
 * Query keys, organization-scoped. `list` and `detail` do not prefix each
 * other, so invalidating the list leaves an open conversation alone; `all`
 * covers both.
 */
export const threadKeys = {
  all: (organizationId: string) => ["threads", organizationId] as const,
  list: (organizationId: string) =>
    ["threads", organizationId, "list"] as const,
  detail: (organizationId: string, threadId: string) =>
    ["threads", organizationId, "detail", threadId] as const,
}

/**
 * Retries a failed read once, but not a 4xx: a thread that is gone or not
 * the member's stays that way, and an expired session was already refreshed.
 */
export function retryUnlessRefused(
  failureCount: number,
  error: unknown
): boolean {
  if (error instanceof ApiError && error.status >= 400 && error.status < 500) {
    return false
  }
  return failureCount < 1
}

export function threadListQueryOptions(organizationId: string) {
  return queryOptions({
    queryKey: threadKeys.list(organizationId),
    queryFn: ({ signal }) => listThreads(organizationId, { signal }),
    retry: retryUnlessRefused,
  })
}

export function threadDetailQueryOptions(
  organizationId: string,
  threadId: string
) {
  return queryOptions({
    queryKey: threadKeys.detail(organizationId, threadId),
    queryFn: ({ signal }) => getThread(organizationId, threadId, { signal }),
    retry: retryUnlessRefused,
  })
}
