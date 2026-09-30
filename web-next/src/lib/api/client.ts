type ErrorBody = {
  error?: string | { code?: string; message?: string }
}

export class ApiError extends Error {
  constructor(
    message: string,
    readonly status: number,
    readonly code?: string
  ) {
    super(message)
    this.name = "ApiError"
  }
}

type ApiOptions = {
  body?: unknown
  method?: "DELETE" | "GET" | "PATCH" | "POST" | "PUT"
  refreshSession?: boolean
  signal?: AbortSignal
}

export function apiUrl(path: string): string {
  const base = import.meta.env.VITE_API_URL || ""
  return `${base.replace(/\/$/, "")}${path}`
}

/**
 * How long a session refresh may take. Every request that meets a 401 waits
 * on the one refresh in flight, so one that never answered would hold them
 * all; past this it counts as failed, like a refresh that could not connect.
 */
const refreshTimeoutMs = 10_000

type BrowserSession = {
  controller: AbortController
  ended: boolean
  generation: number
  pendingRefresh?: Promise<boolean>
  listeners: Set<() => void>
}

// A window owns its refresh and terminal state. SSR requests share neither;
// a fresh OAuth return document must verify its session before showing work.
const browserSessions = new WeakMap<Window, BrowserSession>()
const responseSessions = new WeakMap<
  Response,
  {
    session: BrowserSession
    generation: number
    signal: AbortSignal
  }
>()

function browserSession(): BrowserSession | undefined {
  if (typeof window === "undefined" || typeof document === "undefined") return
  let session = browserSessions.get(window)
  if (!session) {
    session = {
      controller: new AbortController(),
      ended: false,
      generation: 0,
      listeners: new Set(),
    }
    browserSessions.set(window, session)
  }
  return session
}

export function sessionHasEnded(): boolean {
  return browserSession()?.ended ?? false
}

export function subscribeSessionEnded(listener: () => void): () => void {
  const session = browserSession()
  session?.listeners.add(listener)
  return () => {
    session?.listeners.delete(listener)
  }
}

/** Called only after the password sign-in API has succeeded. */
export function resumeSessionAfterSignIn(): void {
  const session = browserSession()
  if (!session) return
  session.generation++
  session.controller.abort(
    new DOMException("The request belongs to an earlier session.", "AbortError")
  )
  session.controller = new AbortController()
  session.pendingRefresh = undefined
  session.ended = false
  for (const listener of session.listeners) listener()
}

const sessionEndedMessage = "Your session has ended. Sign in again to continue."

function endedResponse(): Response {
  return Response.json(
    { error: { code: "session_ended", message: sessionEndedMessage } },
    { status: 401 }
  )
}

function assertCurrentSession(
  session?: BrowserSession,
  generation?: number
): void {
  if (session && session.generation !== generation) {
    throw new DOMException(
      "The request belongs to an earlier session.",
      "AbortError"
    )
  }
}

function assertActiveSession(
  session?: BrowserSession,
  generation?: number
): void {
  assertCurrentSession(session, generation)
  if (session?.ended)
    throw new ApiError(sessionEndedMessage, 401, "session_ended")
}

/** A checkpoint for private stream callbacks and custom response readers. */
export function assertApiResponseCurrent(response: Response): void {
  const scope = responseSessions.get(response)
  assertActiveSession(scope?.session, scope?.generation)
}

/** Keep the headers' session bound to delayed JSON, errors, files or streams. */
export async function readApiResponse<T>(
  response: Response,
  read: (response: Response, sessionSignal?: AbortSignal) => Promise<T>
): Promise<T> {
  assertApiResponseCurrent(response)
  let result: T
  try {
    result = await read(response, responseSessions.get(response)?.signal)
  } catch (error) {
    assertApiResponseCurrent(response)
    throw error
  }
  assertApiResponseCurrent(response)
  return result
}

function refreshSession(session?: BrowserSession): Promise<boolean> {
  if (session?.ended) return Promise.resolve(false)
  if (session?.pendingRefresh) return session.pendingRefresh
  const generation = session?.generation
  const pending = fetch(apiUrl("/v1/auth/refresh"), {
    credentials: "include",
    method: "POST",
    signal: session
      ? AbortSignal.any([
          session.controller.signal,
          AbortSignal.timeout(refreshTimeoutMs),
        ])
      : AbortSignal.timeout(refreshTimeoutMs),
  })
    .then(async (response) => {
      assertCurrentSession(session, generation)
      if (response.status === 401) {
        if (session) {
          session.ended = true
          session.controller.abort(
            new ApiError(sessionEndedMessage, 401, "session_ended")
          )
          for (const listener of session.listeners) listener()
        }
        return false
      }
      if (!response.ok) throw await requestError(response)
      return true
    })
    .finally(() => {
      if (session && session.pendingRefresh === pending)
        session.pendingRefresh = undefined
    })
  if (session) session.pendingRefresh = pending
  return pending
}

export async function apiFetch(
  path: string,
  { body, method, refreshSession: canRefresh = true, signal }: ApiOptions = {}
): Promise<Response> {
  const session = canRefresh ? browserSession() : undefined
  const generation = session?.generation
  const sessionSignal = session?.controller.signal
  const boundResponse = (response: Response) => {
    if (session && sessionSignal)
      responseSessions.set(response, {
        session,
        generation: generation!,
        signal: sessionSignal,
      })
    return response
  }
  if (session?.ended) return boundResponse(endedResponse())
  // A FormData body (a multipart upload) is sent exactly as given:
  // stringifying it would lose the file, and setting Content-Type ourselves
  // would drop the boundary the browser generates when it serializes the
  // body. FormData is a plain value, not a stream, so the same instance can
  // be read again on a refresh-and-retry below.
  const isFormData = body instanceof FormData
  const send = () =>
    fetch(apiUrl(path), {
      body:
        body === undefined
          ? undefined
          : isFormData
            ? body
            : JSON.stringify(body),
      credentials: "include",
      headers:
        body === undefined || isFormData
          ? undefined
          : { "Content-Type": "application/json" },
      method: method || (body === undefined ? "GET" : "POST"),
      signal: sessionSignal
        ? signal
          ? AbortSignal.any([signal, sessionSignal])
          : sessionSignal
        : signal,
    }).catch((error: unknown) => {
      assertActiveSession(session, generation)
      throw error
    })

  let response = await send()
  assertCurrentSession(session, generation)
  if (session?.ended) return boundResponse(endedResponse())
  if (response.status === 401 && canRefresh) {
    let refreshed: boolean
    try {
      refreshed = await refreshSession(session)
    } catch (error) {
      assertCurrentSession(session, generation)
      throw error
    }
    assertCurrentSession(session, generation)
    if (session?.ended) return boundResponse(endedResponse())
    if (refreshed) response = await send()
  }
  assertCurrentSession(session, generation)
  if (session?.ended) return boundResponse(endedResponse())
  return boundResponse(response)
}

/** Retry a transient read twice, with backoff; refusals and terminal auth stop. */
export function retryApiRead(failureCount: number, error: unknown): boolean {
  if (sessionHasEnded()) return false
  if (
    (error instanceof Error || error instanceof DOMException) &&
    error.name === "AbortError"
  )
    return false
  if (error instanceof ApiError && error.status >= 400 && error.status < 500)
    return false
  return failureCount < 2
}

export function apiReadRetryDelay(attempt: number): number {
  return Math.min(1000 * 2 ** attempt, 30_000)
}

export async function apiRequest<T = void>(
  path: string,
  options: ApiOptions = {}
): Promise<T> {
  const response = await apiFetch(path, options)
  return readApiResponse(response, async (current) => {
    if (!current.ok) throw await requestError(current)
    if (!current.headers.get("Content-Type")?.includes("application/json")) {
      return undefined as T
    }
    return current.json()
  })
}

export async function requestError(response: Response): Promise<ApiError> {
  let body: ErrorBody = {}
  try {
    body = await response.json()
  } catch {
    // The HTTP status is still useful when a proxy returns no JSON.
  }
  if (typeof body.error === "string") {
    return new ApiError(body.error, response.status)
  }
  return new ApiError(
    body.error?.message || "Request failed",
    response.status,
    body.error?.code
  )
}
