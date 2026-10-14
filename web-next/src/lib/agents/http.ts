// Request helpers shared by lib/agents, lib/teamspaces and lib/rooms.
//
// The agents service (agents/primary/internal/httpserver/server.go) decodes
// every body with DisallowUnknownFields, nested objects included: one field
// it does not know is a 400. Bodies are therefore built from named fields,
// never passed through, so a page that hands over a whole response object
// (a team's members, say) still sends only what the handler reads.

export type ReadOptions = { signal?: AbortSignal }

/** Builds `/v1/organizations/{organizationId}/…`, encoding every segment. */
export function organizationPath(
  organizationId: string,
  ...segments: string[]
): string {
  return `/v1/organizations/${[organizationId, ...segments]
    .map(encodeURIComponent)
    .join("/")}`
}

/** Appends the parameters that are set as a query string. */
export function withSearch(
  path: string,
  params: Record<string, number | string | undefined>
): string {
  const search = new URLSearchParams()
  for (const [name, value] of Object.entries(params)) {
    if (value !== undefined) search.set(name, String(value))
  }
  const query = search.toString()
  return query ? `${path}?${query}` : path
}

/** Copies the listed fields the caller set, and nothing else. */
export function pick<TInput extends object, TKey extends keyof TInput>(
  input: TInput,
  keys: readonly TKey[]
): Partial<Pick<TInput, TKey>> {
  const picked: Partial<Pick<TInput, TKey>> = {}
  for (const key of keys) {
    if (input[key] !== undefined) picked[key] = input[key]
  }
  return picked
}
