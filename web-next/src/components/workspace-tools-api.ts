export class ApiError extends Error {
  constructor(
    message: string,
    readonly status: number
  ) {
    super(message)
  }
}

export async function readApi<T>(
  url: string,
  options?: RequestInit
): Promise<T> {
  const response = await fetch(url, { credentials: "include", ...options })
  let body: unknown
  try {
    body = await response.json()
  } catch {
    throw new ApiError("The service did not return JSON.", response.status)
  }
  if (!response.ok) {
    const error = (body as { error?: { message?: string } }).error
    throw new ApiError(
      error?.message || "This request could not be completed.",
      response.status
    )
  }
  return body as T
}

export async function writeApi<T>(
  url: string,
  method: "POST" | "PUT" | "DELETE",
  body?: unknown
): Promise<T> {
  const response = await fetch(url, {
    credentials: "include",
    method,
    headers:
      body === undefined ? undefined : { "Content-Type": "application/json" },
    body: body === undefined ? undefined : JSON.stringify(body),
  })
  if (response.status === 204) return undefined as T
  const data = (await response.json().catch(() => null)) as {
    error?: { message?: string }
  } | null
  if (!response.ok)
    throw new ApiError(
      data?.error?.message || "This request could not be completed.",
      response.status
    )
  return data as T
}
