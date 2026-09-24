// Rag: an organization's searchable knowledge, which the page calls knowledge
// bases. A corpus hangs on a member, a team or the organization exactly as a
// file does. Documents go in as text, are chunked and embedded in the
// background, and come back out of search and answer as cited passages.
//
// Handlers are in rag/internal/httpserver/server.go; the rules they enforce
// are in rag/internal/application/service.go and rag/internal/domain. The
// handler decodes every body with DisallowUnknownFields, so bodies below are
// built from named fields: one field it does not know is a 400.
import { infiniteQueryOptions, queryOptions } from "@tanstack/react-query"
import type { QueryClient } from "@tanstack/react-query"
import { ApiError, apiRequest } from "@/lib/api/client"
import type { Role } from "@/lib/auth/client"
import {
  defaultRetentionDays,
  deleteFile,
  uploadFile,
} from "@/lib/files/client"

export type CorpusScope = "member" | "team" | "organization"

export type Corpus = {
  id: string
  organization_id: string
  /** Unique in the organization, across every member's corpora. */
  slug: string
  name: string
  scope: CorpusScope
  team_id?: string
  /** The owner of a member corpus. */
  member_id?: string
  /** Pinned at creation; a corpus never changes model. */
  embedding: {
    provider: string
    model: string
    dimensions: number
    metric: string
  }
  backend: string
  chunking: { strategy: string; max_tokens: number; overlap_tokens: number }
  /**
   * Flat strings. `origin: "knowledge"` marks the corpus knowledge fills from
   * a member's connected apps. `description` is where this app keeps a
   * knowledge base's description: a corpus has no field of its own for one.
   */
  metadata?: Partial<Record<string, string>>
  document_count: number
  chunk_count: number
  created_by: string
  created_at: string
  /** Set at creation. Adding or removing a document does not move it. */
  updated_at: string
}

/**
 * `pending`: recorded and queued, or waiting to retry a failed attempt
 * (`attempts` > 0). `ingesting`: a worker holds it. `ready`: every chunk is
 * embedded and searchable. `failed`: five attempts gave up, or the document
 * could never be indexed; `failure_reason` says why.
 */
export type DocumentStatus = "pending" | "ingesting" | "ready" | "failed"

/**
 * `inline` is text handed straight to rag, with no reference. `external` is
 * text from outside Oryxa, kept with the URI it came from: a link a member
 * added, or a document knowledge indexed from a connected app. `file` is a
 * files object rag fetched as the caller when the document was added.
 */
export type SourceKind = "inline" | "external" | "file"

export type DocumentSource = {
  kind: SourceKind
  /** The URI for `external`, the files object id for `file`. */
  ref?: string
}

export type CorpusDocument = {
  id: string
  corpus_id: string
  organization_id: string
  source: DocumentSource
  title?: string
  content_type: string
  content_sha256: string
  byte_size: number
  /** For a connected app's document: `provider`, `knowledge_source_id`… */
  metadata?: Partial<Record<string, string>>
  status: DocumentStatus
  failure_reason?: string
  chunk_count: number
  attempts: number
  created_by: string
  created_at: string
  updated_at: string
  ingested_at?: string
}

/** One page of a corpus's documents, newest first. */
export type DocumentPage = {
  documents: CorpusDocument[]
  /** Absent on the last page; otherwise the next request's `cursor`. */
  next_cursor?: string
}

export type Usage = {
  input_tokens?: number
  output_tokens?: number
  embedded_texts?: number
  embedding_tokens?: number
}

export type Chunk = {
  id: string
  document_id: string
  corpus_id: string
  organization_id: string
  ordinal: number
  text: string
  token_count: number
  heading_path?: string[]
  start_byte: number
  end_byte: number
  sha256: string
  metadata?: Partial<Record<string, string>>
}

/** What travels with a match, so a citation renders without a second read. */
export type DocumentRef = { id: string; title?: string; source: DocumentSource }

export type Match = {
  chunk: Chunk
  document: DocumentRef
  score: number
  dense_score?: number
  lexical_score?: number
  rerank_score?: number
  dense_rank?: number
  lexical_rank?: number
}

export type SearchResults = {
  matches: Match[]
  usage: Usage
  reranked: boolean
}

/** A passage an answer leaned on; `index` is the `[n]` in the answer text. */
export type Citation = {
  index: number
  chunk_id: string
  document_id: string
  title?: string
  source: DocumentSource
  heading_path?: string[]
  start_byte: number
  end_byte: number
  score: number
}

export type Answer = {
  text: string
  /** Only the passages the text actually cites. */
  citations: Citation[]
  model: string
  usage: Usage
  /** False when nothing retrieved supports an answer. */
  grounded: boolean
}

export type Answered = { answer: Answer; matches: Match[] }

/** `GET /v1/capabilities`: what this deployment can serve. */
export type Capabilities = {
  embedding_providers: string[]
  backends: string[]
  /** `domain.ModelProfile` has no JSON tags, so its keys arrive Go-cased. */
  models: Array<{ Provider: string; Model: string; Dimensions: number[] }>
  /** What a document's `content_type` may be. */
  content_types: string[]
  /** Whether `answer` has a model; without one it is a 400. */
  answering: boolean
  reranking: boolean
}

export type ReadOptions = { signal?: AbortSignal }

/** What a document may be made of (`domain.textContentTypes`). */
export const indexableContentTypes = [
  "text/plain",
  "text/markdown",
  "text/html",
  "text/csv",
  "application/json",
] as const

/** `domain.Name`: a corpus name is 1–120 characters. */
export const maxCorpusNameLength = 120
/** `domain.Title`: a document title is at most 200 characters. */
export const maxDocumentTitleLength = 200
/** `domain.Source.Validate`: an external reference is at most 2,048 bytes. */
export const maxSourceRefLength = 2048
/** `domain.MaxMetadataValue`: one metadata value is at most 1,024 bytes. */
export const maxMetadataValueBytes = 1024
/** `domain.MaxDocumentBytes`: a document's text is at most 16 MiB. */
export const maxDocumentBytes = 16 << 20
/** `domain.MaxQueryCharacters`. */
export const maxQueryLength = 4096
/** The page size the document list asks for (the handler allows up to 200). */
export const documentsPageSize = 50

function organizationPath(organizationId: string, ...segments: string[]) {
  return `/v1/organizations/${[organizationId, ...segments]
    .map(encodeURIComponent)
    .join("/")}`
}

function corpusPath(
  organizationId: string,
  corpusId: string,
  ...rest: string[]
) {
  return organizationPath(organizationId, "corpora", corpusId, ...rest)
}

export function getCapabilities({ signal }: ReadOptions = {}) {
  return apiRequest<Capabilities>("/v1/capabilities", { signal })
}

/**
 * The corpora the caller may search by default: their own member corpora and
 * the organization's. Team corpora are only listed per team
 * (`?scope=team&team_id=`), which this app does not ask for yet.
 */
export async function listCorpora(
  organizationId: string,
  { signal }: ReadOptions = {}
): Promise<Corpus[]> {
  const { corpora } = await apiRequest<{ corpora: Corpus[] }>(
    organizationPath(organizationId, "corpora"),
    { signal }
  )
  return corpora
}

export function getCorpus(
  organizationId: string,
  corpusId: string,
  { signal }: ReadOptions = {}
) {
  return apiRequest<Corpus>(corpusPath(organizationId, corpusId), { signal })
}

export type CreateCorpusInput = {
  name: string
  description?: string
  /** Defaults to `member`. Only owners and admins may create `organization`. */
  scope?: CorpusScope
  /** Required for, and only sent with, `team`. */
  teamId?: string
}

// Mirrors rag's slugify (rag/internal/application/service.go): lowercase,
// runs of anything but a–z and 0–9 become one hyphen, trimmed, cut to 63.
function slugify(value: string): string {
  return value
    .toLowerCase()
    .replace(/[^a-z0-9]+/g, "-")
    .replace(/^-+|-+$/g, "")
    .slice(0, 63)
    .replace(/-+$/, "")
}

/**
 * The slug to ask for first. `domain.Slug` needs 2 to 63 characters, which a
 * name with fewer than two a–z or 0–9 characters would not slugify to.
 */
export function corpusSlugBase(name: string): string {
  const slug = slugify(name)
  return slug.length >= 2 ? slug : "knowledge-base"
}

const slugSuffixAlphabet = "abcdefghijklmnopqrstuvwxyz0123456789"

// crypto.randomUUID() needs a secure context, which a plain-http LAN address
// is not; getRandomValues works everywhere.
function randomSlugSuffix(): string {
  const bytes = new Uint8Array(6)
  crypto.getRandomValues(bytes)
  return Array.from(
    bytes,
    (byte) => slugSuffixAlphabet[byte % slugSuffixAlphabet.length]
  ).join("")
}

/**
 * Creates a corpus. Slugs are unique across the whole organization, so a name
 * another member already used is a 409 even though their corpus is not
 * visible here; the slug is retried with a random suffix, twice.
 */
export async function createCorpus(
  organizationId: string,
  input: CreateCorpusInput
): Promise<Corpus> {
  const name = input.name.trim()
  const scope = input.scope ?? "member"
  const description = input.description?.trim()
  const base = corpusSlugBase(name)
  let slug = base
  for (let attempt = 0; ; attempt += 1) {
    try {
      return await apiRequest<Corpus>(
        organizationPath(organizationId, "corpora"),
        {
          body: {
            name,
            slug,
            scope,
            ...(scope === "team" && input.teamId
              ? { team_id: input.teamId }
              : {}),
            ...(description ? { metadata: { description } } : {}),
          },
        }
      )
    } catch (error) {
      if (
        !(error instanceof ApiError) ||
        error.status !== 409 ||
        attempt >= 2
      ) {
        throw error
      }
      slug = `${base.slice(0, 56).replace(/-+$/, "")}-${randomSlugSuffix()}`
    }
  }
}

/** Removes the corpus, every document in it and everything indexed from them. */
export async function deleteCorpus(
  organizationId: string,
  corpusId: string
): Promise<void> {
  await apiRequest(corpusPath(organizationId, corpusId), { method: "DELETE" })
}

export type DocumentsFilter = ReadOptions & {
  cursor?: string
  limit?: number
  status?: DocumentStatus
}

export function listDocuments(
  organizationId: string,
  corpusId: string,
  { cursor, limit, status, signal }: DocumentsFilter = {}
): Promise<DocumentPage> {
  const search = new URLSearchParams()
  if (status) search.set("status", status)
  if (cursor) search.set("cursor", cursor)
  if (limit) search.set("limit", String(limit))
  const query = search.toString()
  const path = corpusPath(organizationId, corpusId, "documents")
  return apiRequest<DocumentPage>(query ? `${path}?${query}` : path, {
    signal,
  })
}

/**
 * A document, in the three forms `addDocument` accepts. There is no upload
 * and no fetching: rag takes text, and a `file` is a files object it reads.
 */
export type NewDocument =
  | {
      kind: "inline"
      text: string
      title?: string
      contentType?: string
    }
  | {
      kind: "external"
      /** Kept as the citation's source; rag does not fetch it. */
      url: string
      text: string
      title?: string
      contentType?: string
    }
  | {
      kind: "file"
      fileId: string
      /** Without one, rag uses the file's own name. */
      title?: string
      /** Without one, rag uses the content type the file was stored with. */
      contentType?: string
    }

function documentBody(input: NewDocument) {
  const ref =
    input.kind === "external"
      ? input.url.trim()
      : input.kind === "file"
        ? input.fileId
        : undefined
  const title = input.title?.trim()
  return {
    source:
      ref === undefined ? { kind: input.kind } : { kind: input.kind, ref },
    ...(title ? { title } : {}),
    ...(input.contentType ? { content_type: input.contentType } : {}),
    ...(input.kind === "file" ? {} : { text: input.text }),
  }
}

/**
 * Accepts a document and queues it (202): it is `pending`, not searchable,
 * until the ingest worker marks it `ready`. The same content offered again is
 * the same document: an inline note is known by its text, a link or a file by
 * its reference, so re-adding a link replaces the text kept for it.
 */
export function addDocument(
  organizationId: string,
  corpusId: string,
  input: NewDocument
): Promise<CorpusDocument> {
  return apiRequest<CorpusDocument>(
    corpusPath(organizationId, corpusId, "documents"),
    { body: documentBody(input) }
  )
}

const contentTypesByExtension: Record<string, string> = {
  csv: "text/csv",
  htm: "text/html",
  html: "text/html",
  json: "application/json",
  markdown: "text/markdown",
  md: "text/markdown",
  text: "text/plain",
  txt: "text/plain",
}

/** What a file picker should offer: the extensions rag can index. */
export const indexableFileAccept = [
  ...Object.keys(contentTypesByExtension).map((extension) => `.${extension}`),
  ...indexableContentTypes,
].join(",")

/**
 * The content type rag should index `file` as, or undefined when rag cannot
 * index it (a PDF, an Office file, an image). The browser's own type is used
 * when rag takes it; otherwise the extension decides, since browsers report
 * Markdown as nothing at all and CSV as a spreadsheet.
 */
export function indexableContentType(
  file: Pick<File, "name" | "type">
): string | undefined {
  const type = file.type.split(";")[0]?.trim().toLowerCase() ?? ""
  if ((indexableContentTypes as readonly string[]).includes(type)) return type
  const extension = file.name.includes(".")
    ? file.name.split(".").pop()?.toLowerCase()
    : undefined
  return extension ? contentTypesByExtension[extension] : undefined
}

/**
 * Adds a file from the member's computer. Rag has no upload of its own: the
 * file goes to files first, in the corpus's own scope so it is visible to the
 * same people, and the document names it. Rag reads the file as the caller at
 * once and keeps its text, so indexing never depends on the file again. If
 * rag refuses the document, the upload is deleted rather than left behind.
 */
export async function addFileDocument(
  organizationId: string,
  corpus: Pick<Corpus, "id" | "scope" | "team_id">,
  { file, title }: { file: File; title?: string }
): Promise<CorpusDocument> {
  const contentType = indexableContentType(file)
  if (!contentType) {
    throw new Error(
      "Knowledge bases take text: a .txt, .md, .html, .csv or .json file."
    )
  }
  const stored = await uploadFile(organizationId, {
    file,
    retentionDays: defaultRetentionDays,
    scope: corpus.scope,
    teamId: corpus.team_id,
  })
  try {
    return await addDocument(organizationId, corpus.id, {
      kind: "file",
      fileId: stored.id,
      title,
      contentType,
    })
  } catch (error) {
    await deleteFile(organizationId, stored.id).catch(() => undefined)
    throw error
  }
}

/** Removes the document and everything indexed from it (204). */
export async function deleteDocument(
  organizationId: string,
  corpusId: string,
  documentId: string
): Promise<void> {
  await apiRequest(
    corpusPath(organizationId, corpusId, "documents", documentId),
    {
      method: "DELETE",
    }
  )
}

export type QueryInput = {
  query: string
  /** 1–100; the server's default is 8. */
  topK?: number
}

function queryBody({ query, topK }: QueryInput) {
  return { query: query.trim(), ...(topK ? { top_k: topK } : {}) }
}

/**
 * The passages that match, best first: hybrid (vector and full-text) by
 * default. A passage can come from a document that is still indexing, since
 * its full-text half is written before its embeddings.
 */
export function searchCorpus(
  organizationId: string,
  corpusId: string,
  input: QueryInput
): Promise<SearchResults> {
  return apiRequest<SearchResults>(
    corpusPath(organizationId, corpusId, "search"),
    { body: queryBody(input) }
  )
}

/**
 * A grounded answer with the passages it cites. Only when
 * `capabilities.answering` is true: otherwise the handler answers 400.
 */
export function answerCorpus(
  organizationId: string,
  corpusId: string,
  input: QueryInput
): Promise<Answered> {
  return apiRequest<Answered>(corpusPath(organizationId, corpusId, "answer"), {
    body: queryBody(input),
  })
}

// ---------------------------------------------------------------------------
// Reading a corpus and its documents.

export function corpusDescription(corpus: Pick<Corpus, "metadata">): string {
  return corpus.metadata?.description?.trim() ?? ""
}

/** The corpus knowledge fills from a member's connected apps. */
export function isFromConnectedApps(corpus: Pick<Corpus, "metadata">): boolean {
  return corpus.metadata?.origin === "knowledge"
}

export function isIndexing(document: Pick<CorpusDocument, "status">): boolean {
  return document.status === "pending" || document.status === "ingesting"
}

/** Parses a rag timestamp, whose fraction can run past milliseconds. */
export function parseTimestamp(value: string | undefined): number {
  if (!value) return Number.NaN
  return Date.parse(value.replace(/(\.\d{3})\d+/, "$1"))
}

/** A source's link, when it is safe to render as one: `external` and http(s). */
export function externalSourceLink(source: DocumentSource): string | undefined {
  if (source.kind !== "external" || !source.ref) return undefined
  try {
    const url = new URL(source.ref)
    return url.protocol === "http:" || url.protocol === "https:"
      ? url.href
      : undefined
  } catch {
    return undefined
  }
}

const connectedAppNames: Record<string, string> = {
  github: "GitHub",
  gmail: "Gmail",
  googledrive: "Google Drive",
  notion: "Notion",
  slack: "Slack",
}

/** A knowledge provider's name, as `GET /v1/sources/providers` spells it. */
export function connectedAppName(provider: string): string {
  return (
    connectedAppNames[provider.toLowerCase()] ??
    provider.charAt(0).toUpperCase() + provider.slice(1)
  )
}

/**
 * Where a document came from, for the page to say so: a note typed here, a
 * link a member added with its text, a file they uploaded, or a connected
 * app's document that knowledge indexed (which carries its provider).
 */
export type DocumentOrigin =
  | { kind: "note" }
  | { kind: "link"; href?: string; host?: string }
  | { kind: "file"; fileId?: string }
  | { kind: "app"; provider: string; name: string; href?: string }

export function documentOrigin(
  document: Pick<CorpusDocument, "source" | "metadata">
): DocumentOrigin {
  const { source } = document
  if (source.kind === "file") return { kind: "file", fileId: source.ref }
  if (source.kind === "external") {
    const href = externalSourceLink(source)
    const provider = document.metadata?.provider?.trim()
    if (provider) {
      return { kind: "app", provider, name: connectedAppName(provider), href }
    }
    return { kind: "link", href, host: href ? new URL(href).host : undefined }
  }
  return { kind: "note" }
}

const contentTypeNames: Record<string, string> = {
  "application/json": "JSON",
  "text/csv": "CSV",
  "text/html": "HTML",
  "text/markdown": "Markdown",
  "text/plain": "Text",
}

export function contentTypeName(contentType: string): string {
  return contentTypeNames[contentType] ?? contentType
}

// ---------------------------------------------------------------------------
// Who may do what. The service decides; these mirror mayWrite, mayDelete and
// mayCreate in rag/internal/application/service.go so controls a member
// cannot use are not offered.

export type RagViewer = { userId?: string; role?: Role }

function isOrgAdmin(viewer: RagViewer): boolean {
  return viewer.role === "owner" || viewer.role === "admin"
}

/** `mayCreate`: anyone may make a member corpus; the organization's is admins'. */
export function canCreateOrganizationCorpus(viewer: RagViewer): boolean {
  return isOrgAdmin(viewer)
}

/** `mayWrite`: add or remove documents. */
export function canWriteCorpus(
  corpus: Pick<Corpus, "scope" | "member_id">,
  viewer: RagViewer
): boolean {
  switch (corpus.scope) {
    case "organization":
      return isOrgAdmin(viewer)
    case "member":
      return (
        isOrgAdmin(viewer) ||
        (viewer.userId !== undefined && corpus.member_id === viewer.userId)
      )
    case "team":
      // Seeing a team corpus is enough to write to it.
      return true
    default:
      return false
  }
}

/**
 * `mayDelete`. A team corpus may also be deleted by whoever manages the team,
 * which this app does not know without asking teams; the service still lets
 * them, the page just does not offer it.
 */
export function canDeleteCorpus(
  corpus: Pick<Corpus, "scope" | "member_id" | "created_by">,
  viewer: RagViewer
): boolean {
  if (isOrgAdmin(viewer)) return true
  if (viewer.userId === undefined) return false
  switch (corpus.scope) {
    case "member":
      return corpus.member_id === viewer.userId
    case "team":
      return corpus.created_by === viewer.userId
    default:
      return false
  }
}

function sentence(message: string): string {
  const trimmed = message.trim()
  const capitalized = trimmed.charAt(0).toUpperCase() + trimmed.slice(1)
  return /[.!?]$/.test(capitalized) ? capitalized : `${capitalized}.`
}

/**
 * Copy for a failed rag call. Rag's own 400 and 409 messages name the
 * problem, so they are shown; the rest are said plainly.
 */
export function ragErrorMessage(
  error: unknown,
  fallback: string,
  { notFound }: { notFound?: string } = {}
): string {
  if (!(error instanceof ApiError) || error.status >= 500) return fallback
  switch (error.status) {
    case 401:
      return "Your session has ended. Sign in again to continue."
    case 403:
      return "You don't have permission to do that here."
    case 404:
      return notFound ?? fallback
    case 413:
      return "That's too large to add. Split it into smaller documents."
    case 429:
      return "Too many requests. Try again in a minute."
  }
  const message = error.message.trim()
  if (!message || message === "Request failed") return fallback
  return sentence(message)
}

// ---------------------------------------------------------------------------
// Query keys, options and cache updates. A corpus's documents are keyed under
// the corpus, so invalidating the corpus refreshes them too.

function organizationKey(organizationId: string) {
  return ["rag", "organization", organizationId] as const
}

function corporaKey(organizationId: string) {
  return [...organizationKey(organizationId), "corpora"] as const
}

function corpusKey(organizationId: string, corpusId: string) {
  return [...organizationKey(organizationId), "corpus", corpusId] as const
}

function documentsKey(organizationId: string, corpusId: string) {
  return [...corpusKey(organizationId, corpusId), "documents"] as const
}

function capabilitiesKey() {
  return ["rag", "capabilities"] as const
}

export const ragKeys = {
  organization: organizationKey,
  corpora: corporaKey,
  corpus: corpusKey,
  documents: documentsKey,
  capabilities: capabilitiesKey,
}

/** Capabilities follow the deployment's configuration; five minutes is fresh. */
export function capabilitiesQueryOptions() {
  return queryOptions({
    queryKey: ragKeys.capabilities(),
    queryFn: ({ signal }) => getCapabilities({ signal }),
    staleTime: 5 * 60_000,
  })
}

export function corporaQueryOptions(organizationId: string) {
  return queryOptions({
    queryKey: ragKeys.corpora(organizationId),
    queryFn: ({ signal }) => listCorpora(organizationId, { signal }),
  })
}

export function corpusQueryOptions(organizationId: string, corpusId: string) {
  return queryOptions({
    queryKey: ragKeys.corpus(organizationId, corpusId),
    queryFn: ({ signal }) => getCorpus(organizationId, corpusId, { signal }),
  })
}

/** While something was just added or picked up: a few seconds. */
export const INDEXING_POLL_MS = 3_000
/** While something has been indexing for minutes. */
export const SLOW_INDEXING_POLL_MS = 15_000
/**
 * While the only work left is waiting on a retry. Rag waits 30 s after a
 * failed attempt and four times longer after each one after that.
 */
export const RETRY_POLL_MS = 30_000
/** How long after its last change a document counts as fresh work. */
export const FRESH_INDEXING_MS = 2 * 60_000

/**
 * How often to re-read the document list: never once nothing is pending or
 * ingesting, and less often the longer indexing takes, so a document stuck
 * behind a half-hour retry is not polled every few seconds.
 */
export function documentsPollInterval(
  pages: readonly DocumentPage[] | undefined,
  now = Date.now()
): number | false {
  let interval: number | false = false
  for (const page of pages ?? []) {
    for (const document of page.documents) {
      if (!isIndexing(document)) continue
      const changed = parseTimestamp(document.updated_at)
      const wait =
        document.status === "pending" && document.attempts > 0
          ? RETRY_POLL_MS
          : Number.isNaN(changed) || now - changed < FRESH_INDEXING_MS
            ? INDEXING_POLL_MS
            : SLOW_INDEXING_POLL_MS
      interval = interval === false ? wait : Math.min(interval, wait)
    }
  }
  return interval
}

/**
 * A corpus's documents, newest first, fifty at a time. Re-read while any
 * loaded document is still indexing (see `documentsPollInterval`), and only
 * while the page is visible.
 */
export function documentsInfiniteQueryOptions(
  organizationId: string,
  corpusId: string
) {
  return infiniteQueryOptions({
    queryKey: ragKeys.documents(organizationId, corpusId),
    queryFn: ({ pageParam, signal }) =>
      listDocuments(organizationId, corpusId, {
        cursor: pageParam,
        limit: documentsPageSize,
        signal,
      }),
    initialPageParam: undefined as string | undefined,
    getNextPageParam: (lastPage: DocumentPage) =>
      lastPage.next_cursor || undefined,
    refetchInterval: (query) => documentsPollInterval(query.state.data?.pages),
    refetchIntervalInBackground: false,
  })
}

/** After `createCorpus`: caches the corpus and puts it first in the list. */
export async function afterCreateCorpus(
  queryClient: QueryClient,
  organizationId: string,
  created: Corpus
): Promise<void> {
  queryClient.setQueryData(ragKeys.corpus(organizationId, created.id), created)
  queryClient.setQueryData<Corpus[]>(ragKeys.corpora(organizationId), (list) =>
    list ? [created, ...list.filter((item) => item.id !== created.id)] : list
  )
  await queryClient.invalidateQueries({
    queryKey: ragKeys.corpora(organizationId),
    exact: true,
  })
}

/**
 * After `deleteCorpus`: takes it out of the list. Its own queries are left
 * for garbage collection once the detail unmounts: removing them while it is
 * still mounted would have its observers read the deleted corpus again.
 */
export async function afterDeleteCorpus(
  queryClient: QueryClient,
  organizationId: string,
  corpusId: string
): Promise<void> {
  queryClient.setQueryData<Corpus[]>(ragKeys.corpora(organizationId), (list) =>
    list?.filter((item) => item.id !== corpusId)
  )
  await queryClient.invalidateQueries({
    queryKey: ragKeys.corpora(organizationId),
    exact: true,
  })
}

/** After a document is added or removed: its corpus, documents and the list. */
export async function afterDocumentsChange(
  queryClient: QueryClient,
  organizationId: string,
  corpusId: string
): Promise<void> {
  await Promise.all([
    queryClient.invalidateQueries({
      queryKey: ragKeys.corpus(organizationId, corpusId),
    }),
    queryClient.invalidateQueries({
      queryKey: ragKeys.corpora(organizationId),
      exact: true,
    }),
  ])
}
