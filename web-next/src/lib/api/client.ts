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

let pendingRefresh: Promise<boolean> | undefined

function refreshSession(): Promise<boolean> {
  // The timeout aborts the fetch, which rejects and lands in the second
  // handler below; `finally` then clears the shared promise, so the next 401
  // starts a refresh of its own instead of waiting on this one.
  pendingRefresh ??= fetch(apiUrl("/v1/auth/refresh"), {
    credentials: "include",
    method: "POST",
    signal: AbortSignal.timeout(refreshTimeoutMs),
  })
    .then(
      (response) => response.ok,
      () => false
    )
    .finally(() => {
      pendingRefresh = undefined
    })
  return pendingRefresh
}

export async function apiFetch(
  path: string,
  { body, method, refreshSession: canRefresh = true, signal }: ApiOptions = {}
): Promise<Response> {
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
      signal,
    })

  let response = await send()
  if (response.status === 401 && canRefresh && (await refreshSession())) {
    response = await send()
  }
  return response
}

export async function apiRequest<T = void>(
  path: string,
  options: ApiOptions = {}
): Promise<T> {
  const response = await apiFetch(path, options)
  if (!response.ok) throw await requestError(response)
  if (!response.headers.get("Content-Type")?.includes("application/json")) {
    return undefined as T
  }
  const data: T = await response.json()
  return data
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
