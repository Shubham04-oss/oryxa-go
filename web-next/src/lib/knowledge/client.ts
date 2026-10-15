// What a member asks knowledge to index, and how its runs have gone.
// Handlers are in knowledge/internal/httpserver/server.go; the rules are in
// knowledge/internal/application/sources.go.
//
// Who may do what:
// - Registering reads through the caller's own active connection only
//   (409 `conflict` when it is not active, 403 `forbidden` when it is
//   someone else's). An agent can never register, remove or change a source.
// - Listing answers the caller's own sources; `scope: "organization"` widens
//   that to everyone's for an org admin and is ignored for anyone else.
// - Reading runs, syncing, removing and choosing kinds are for the source's
//   creator or an org admin; anyone else gets 404 `not_found`.
// - A removed connection retires its sources (their documents are retracted);
//   an expired one pauses them with `reason: "needs_reconnect"`.
//
// Error bodies are `{ error: { code, message } }`, like apps', so pages
// humanize them with `appsErrorMessage` from `lib/apps/client`.
import { queryOptions } from "@tanstack/react-query"
import { apiRequest } from "@/lib/api/client"

export type SourceStatus = "active" | "paused" | "removed"

export type SourceScope = "member" | "organization" | "team"

/** One thing a member asked their agents to index, such as a repository. */
export type Source = {
  id: string
  provider: string
  /** Absent for a source that reads no connection (the organization's files). */
  connection_id?: string
  /** What to index at the provider, in its own terms: `{ repository: "acme/api" }`. */
  target: Record<string, string>
  scope: SourceScope
  team_id?: string
  member_id?: string
  created_by: string
  status: SourceStatus
  /** Why a source is paused, as a code such as `needs_reconnect`; see `sourceReasonText`. */
  reason?: string
  interval_seconds: number
  /** The member's choice of kinds; absent means the default kinds. */
  include_kinds?: string[]
  /** When the next run is due. */
  sync_at?: string
  last_synced_at?: string
  created_at: string
  updated_at: string
}

export type RunStatus = "completed" | "failed" | "running"

/** One pass over a source. Counts are what the run did, not what the source holds. */
export type SyncRun = {
  id: string
  organization_id: string
  source_id: string
  kind: string
  status: RunStatus
  documents_seen: number
  documents_published: number
  documents_removed: number
  /** What the run judged not worth indexing: promotions, routine notices, empty files. */
  documents_excluded: number
  error?: string
  retryable?: boolean
  started_at: string
  finished_at?: string
}

/** A kind of document a member can choose to index, as knowledge names it (`domain.Kinds`). */
export type SourceKind = {
  name: string
  label: string
  /** What the kind covers, in a member's terms. */
  hint: string
  /** Whether a source indexes it unless its member chooses. */
  default: boolean
}

/** Knowledge sweeps a source every 5 minutes to 7 days; leaving it out asks for an hour. */
export const minSourceIntervalSeconds = 300
export const maxSourceIntervalSeconds = 604_800
export const defaultSourceIntervalSeconds = 3_600

/** The most runs one read answers; a larger or missing `limit` is read as this. */
export const maxRunsLimit = 50

function sourcesPath(organizationId: string): string {
  return `/v1/organizations/${encodeURIComponent(organizationId)}/sources`
}

function sourcePath(organizationId: string, sourceId: string): string {
  return `${sourcesPath(organizationId)}/${encodeURIComponent(sourceId)}`
}

function withQuery(
  path: string,
  params: Record<string, number | string | undefined>
): string {
  const search = new URLSearchParams()
  for (const [name, value] of Object.entries(params)) {
    if (value !== undefined && value !== "") search.set(name, String(value))
  }
  const query = search.toString()
  return query ? `${path}?${query}` : path
}

/** What this deployment can extract from, by provider key. Unauthenticated. */
export async function listProviders(): Promise<string[]> {
  const { providers } = await apiRequest<{ providers: string[] }>(
    "/v1/sources/providers"
  )
  return providers
}

/** The kinds of document a member chooses among, in the order they are shown. Unauthenticated. */
export async function listKinds(): Promise<SourceKind[]> {
  const { kinds } = await apiRequest<{ kinds: SourceKind[] }>(
    "/v1/sources/kinds"
  )
  return kinds
}

export type SourceFilter = {
  connectionId?: string
  provider?: string
  /** Everyone's sources, for an org admin; ignored for anyone else. */
  scope?: "organization"
}

/** The caller's sources. The server never lists a removed one. */
export async function listSources(
  organizationId: string,
  filter: SourceFilter = {}
): Promise<Source[]> {
  const { sources } = await apiRequest<{ sources: Source[] }>(
    withQuery(sourcesPath(organizationId), {
      connection_id: filter.connectionId,
      provider: filter.provider,
      scope: filter.scope,
    })
  )
  return sources
}

export type RegisterSourceInput = {
  /** The caller's own active connection to `provider`; left out only for `files`. */
  connectionId?: string
  provider: string
  target: Record<string, string>
  /** Left out asks for the default hour. */
  intervalSeconds?: number
}

/**
 * Registers something to index and schedules its first run now. The handler
 * refuses unknown fields, so the body names exactly the four it reads.
 */
export function registerSource(
  organizationId: string,
  input: RegisterSourceInput
): Promise<Source> {
  return apiRequest<Source>(sourcesPath(organizationId), {
    body: {
      connection_id: input.connectionId,
      provider: input.provider,
      target: input.target,
      interval_seconds: input.intervalSeconds,
    },
  })
}

/** Stops indexing a source and retracts what it held. Answers 204. */
export async function removeSource(
  organizationId: string,
  sourceId: string
): Promise<void> {
  await apiRequest(sourcePath(organizationId, sourceId), { method: "DELETE" })
}

/**
 * Brings a source's next run forward to now. A paused source answers 409
 * `needs_reconnect` (its connection must be reconnected first); a removed
 * one, 409 `conflict`.
 */
export function syncSource(
  organizationId: string,
  sourceId: string
): Promise<Source> {
  return apiRequest<Source>(`${sourcePath(organizationId, sourceId)}/sync`, {
    method: "POST",
  })
}

/**
 * Records which kinds of document a source indexes; `null` returns it to the
 * default. An empty list is refused (400): to index nothing, remove it.
 */
export function setSourceKinds(
  organizationId: string,
  sourceId: string,
  include: readonly string[] | null
): Promise<Source> {
  return apiRequest<Source>(`${sourcePath(organizationId, sourceId)}/kinds`, {
    body: { include_kinds: include },
    method: "PUT",
  })
}

/** A source's recent runs, newest first. The server clamps `limit` to 50. */
export async function listRuns(
  organizationId: string,
  sourceId: string,
  limit?: number
): Promise<SyncRun[]> {
  const { runs } = await apiRequest<{ runs: SyncRun[] }>(
    withQuery(`${sourcePath(organizationId, sourceId)}/runs`, { limit })
  )
  return runs
}

/**
 * One document knowledge extracted from a connected app
 * (`GET /v1/organizations/{org}/documents/{id}`, getDocument in
 * knowledge/internal/httpserver/server.go): its text and where it lives.
 */
export type KnowledgeDocument = {
  id: string
  source_id: string
  external_id: string
  kind: string
  title: string
  /** The document at its app, when the app gives one. */
  url?: string
  mime: string
  body: string
  byte_size: number
  metadata?: Record<string, string>
  source_updated_at?: string
  extracted_at?: string
  removed_at?: string | null
}

export function getKnowledgeDocument(
  organizationId: string,
  documentId: string
): Promise<KnowledgeDocument> {
  return apiRequest<KnowledgeDocument>(
    `/v1/organizations/${encodeURIComponent(organizationId)}/documents/${encodeURIComponent(documentId)}`
  )
}

// ---------------------------------------------------------------------------
// Query keys and options
// ---------------------------------------------------------------------------

/** The prefix of everything cached from knowledge about one organization. */
export function knowledgeQueryKey(organizationId: string) {
  return ["knowledge", organizationId] as const
}

/** The providers change only when knowledge is deployed. */
export function providersQueryOptions() {
  return queryOptions({
    queryKey: ["knowledge", "providers"] as const,
    queryFn: listProviders,
    staleTime: Number.POSITIVE_INFINITY,
  })
}

/** Like the providers, the kinds change only when knowledge is deployed. */
export function kindsQueryOptions() {
  return queryOptions({
    queryKey: ["knowledge", "kinds"] as const,
    queryFn: listKinds,
    staleTime: Number.POSITIVE_INFINITY,
  })
}

export function sourcesQueryOptions(
  organizationId: string,
  filter: SourceFilter = {}
) {
  return queryOptions({
    queryKey: [
      ...knowledgeQueryKey(organizationId),
      "sources",
      filter.connectionId ?? "",
      filter.provider ?? "",
      filter.scope ?? "mine",
    ] as const,
    queryFn: () => listSources(organizationId, filter),
  })
}

/** The prefix of every cached run list for one source. */
export function sourceRunsQueryKey(organizationId: string, sourceId: string) {
  return [...knowledgeQueryKey(organizationId), "runs", sourceId] as const
}

export function sourceRunsQueryOptions(
  organizationId: string,
  sourceId: string,
  limit = 10
) {
  return queryOptions({
    queryKey: [...sourceRunsQueryKey(organizationId, sourceId), limit] as const,
    queryFn: () => listRuns(organizationId, sourceId, limit),
  })
}

export function knowledgeDocumentQueryOptions(
  organizationId: string,
  documentId: string
) {
  return queryOptions({
    queryKey: [
      ...knowledgeQueryKey(organizationId),
      "document",
      documentId,
    ] as const,
    queryFn: () => getKnowledgeDocument(organizationId, documentId),
    staleTime: 60_000,
  })
}

// ---------------------------------------------------------------------------
// Reading a source
// ---------------------------------------------------------------------------

/** What a source indexes: its member's choice, or else the default kinds. */
export function effectiveKinds(
  kinds: readonly SourceKind[],
  chosen: readonly string[] | undefined
): string[] {
  if (chosen) return [...chosen]
  return kinds.filter((kind) => kind.default).map((kind) => kind.name)
}

/**
 * Why a source is paused, in a member's words. `reason` is a code from
 * contracts/events/knowledge/v1 (`PausedReason*`), not prose.
 */
export function sourceReasonText(reason: string | undefined): string {
  switch (reason) {
    case "needs_reconnect":
      return "Paused until the app is reconnected."
    case "scope_required":
      return "Paused: the connection is missing a permission. Reconnect and allow it."
    case "disconnected":
      return "Paused: the app was disconnected."
    case "app_deleted":
      return "Paused: the app was removed."
    case undefined:
    case "":
      return "Paused."
    default:
      return `Paused: ${reason.replace(/_/g, " ")}.`
  }
}
