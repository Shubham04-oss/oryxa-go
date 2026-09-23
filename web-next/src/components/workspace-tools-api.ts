import { apiFetch, ApiError, requestError } from "@/lib/api/client"

export { ApiError } from "@/lib/api/client"

export async function readApi<T>(
  path: string,
  options?: {
    body?: unknown
    method?: "POST" | "PUT" | "DELETE"
    signal?: AbortSignal
  }
): Promise<T> {
  const response = await apiFetch(path, options)
  if (!response.ok) throw await requestError(response)
  try {
    return (await response.json()) as T
  } catch {
    throw new ApiError("The service did not return JSON.", response.status)
  }
}

export async function writeApi<T>(
  path: string,
  method: "POST" | "PUT" | "DELETE",
  body?: unknown
): Promise<T> {
  const response = await apiFetch(path, { method, body })
  if (!response.ok) throw await requestError(response)
  if (response.status === 204) return undefined as T
  try {
    return (await response.json()) as T
  } catch {
    return null as T
  }
}
