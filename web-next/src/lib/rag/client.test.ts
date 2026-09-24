import { afterEach, describe, expect, it, vi } from "vitest"
import { QueryClient } from "@tanstack/react-query"
import { ApiError } from "@/lib/api/client"
import {
  FRESH_INDEXING_MS,
  INDEXING_POLL_MS,
  RETRY_POLL_MS,
  SLOW_INDEXING_POLL_MS,
  addDocument,
  addFileDocument,
  afterCreateCorpus,
  afterDeleteCorpus,
  afterDocumentsChange,
  answerCorpus,
  canCreateOrganizationCorpus,
  canDeleteCorpus,
  canWriteCorpus,
  capabilitiesQueryOptions,
  connectedAppName,
  corporaQueryOptions,
  corpusDescription,
  corpusQueryOptions,
  corpusSlugBase,
  createCorpus,
  deleteCorpus,
  deleteDocument,
  documentOrigin,
  documentsInfiniteQueryOptions,
  documentsPageSize,
  documentsPollInterval,
  externalSourceLink,
  getCapabilities,
  getCorpus,
  indexableContentType,
  indexableContentTypes,
  indexableFileAccept,
  isFromConnectedApps,
  listCorpora,
  listDocuments,
  parseTimestamp,
  ragErrorMessage,
  ragKeys,
  searchCorpus,
} from "./client"
import type { Corpus, CorpusDocument, DocumentPage } from "./client"

function jsonResponse(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  })
}

function stubFetch(...responses: Response[]) {
  const fetchMock = vi.fn<typeof fetch>(async () => {
    const next = responses.shift()
    if (!next) throw new Error("unexpected request")
    return next
  })
  vi.stubGlobal("fetch", fetchMock)
  return fetchMock
}

function sentBody(fetchMock: ReturnType<typeof stubFetch>, call = 0) {
  const init = fetchMock.mock.calls[call]?.[1]
  return typeof init?.body === "string" ? JSON.parse(init.body) : init?.body
}

afterEach(() => vi.unstubAllGlobals())

function corpus(overrides: Partial<Corpus> = {}): Corpus {
  return {
    id: "cor_1",
    organization_id: "org_1",
    slug: "handbook",
    name: "Handbook",
    scope: "member",
    member_id: "user-1",
    embedding: {
      provider: "vertex",
      model: "text-embedding-005",
      dimensions: 768,
      metric: "cosine",
    },
    backend: "pgvector",
    chunking: { strategy: "structure", max_tokens: 512, overlap_tokens: 76 },
    document_count: 0,
    chunk_count: 0,
    created_by: "user-1",
    created_at: "2026-09-20T10:00:00Z",
    updated_at: "2026-09-20T10:00:00Z",
    ...overrides,
  }
}

function document(overrides: Partial<CorpusDocument> = {}): CorpusDocument {
  return {
    id: "doc_1",
    corpus_id: "cor_1",
    organization_id: "org_1",
    source: { kind: "inline" },
    title: "Note",
    content_type: "text/plain",
    content_sha256: "",
    byte_size: 10,
    status: "ready",
    chunk_count: 1,
    attempts: 1,
    created_by: "user-1",
    created_at: "2026-09-20T10:00:00Z",
    updated_at: "2026-09-20T10:00:00Z",
    ...overrides,
  }
}

describe("constants verified against the Go domain", () => {
  it("indexes only extracted text (domain.textContentTypes)", () => {
    expect(indexableContentTypes).toEqual([
      "text/plain",
      "text/markdown",
      "text/html",
      "text/csv",
      "application/json",
    ])
    expect(indexableFileAccept).toContain(".md")
    expect(indexableFileAccept).toContain("text/csv")
  })

  it("asks for fifty documents a page", () => {
    expect(documentsPageSize).toBe(50)
  })
})

describe("corpora", () => {
  it("lists the caller's corpora from the wrapped response", async () => {
    const fetchMock = stubFetch(jsonResponse({ corpora: [corpus()] }))
    await expect(listCorpora("org 1")).resolves.toEqual([corpus()])
    expect(fetchMock).toHaveBeenCalledWith(
      "/v1/organizations/org%201/corpora",
      expect.objectContaining({ credentials: "include", method: "GET" })
    )
  })

  it("reads one corpus by id", async () => {
    const fetchMock = stubFetch(jsonResponse(corpus()))
    await getCorpus("org_1", "cor/1")
    expect(fetchMock).toHaveBeenCalledWith(
      "/v1/organizations/org_1/corpora/cor%2F1",
      expect.objectContaining({ method: "GET" })
    )
  })

  it("creates a member corpus with a slug and its description in metadata", async () => {
    const fetchMock = stubFetch(jsonResponse(corpus(), 201))
    await createCorpus("org_1", {
      name: "  Launch playbook ",
      description: " What we ship and when. ",
    })
    expect(fetchMock).toHaveBeenCalledWith(
      "/v1/organizations/org_1/corpora",
      expect.objectContaining({ method: "POST" })
    )
    expect(sentBody(fetchMock)).toEqual({
      name: "Launch playbook",
      slug: "launch-playbook",
      scope: "member",
      metadata: { description: "What we ship and when." },
    })
  })

  it("sends no metadata without a description, and the scope asked for", async () => {
    const fetchMock = stubFetch(
      jsonResponse(corpus({ scope: "organization" }), 201)
    )
    await createCorpus("org_1", {
      name: "Company wiki",
      description: "   ",
      scope: "organization",
    })
    expect(sentBody(fetchMock)).toEqual({
      name: "Company wiki",
      slug: "company-wiki",
      scope: "organization",
    })
  })

  it("names the team only for a team corpus", async () => {
    const fetchMock = stubFetch(
      jsonResponse(corpus({ scope: "team" }), 201),
      jsonResponse(corpus(), 201)
    )
    await createCorpus("org_1", {
      name: "Team notes",
      scope: "team",
      teamId: "team_1",
    })
    await createCorpus("org_1", { name: "Mine", teamId: "team_1" })
    expect(sentBody(fetchMock, 0)).toMatchObject({
      scope: "team",
      team_id: "team_1",
    })
    expect(sentBody(fetchMock, 1)).not.toHaveProperty("team_id")
  })

  it("retries a slug another corpus holds with a random suffix", async () => {
    const fetchMock = stubFetch(
      jsonResponse({ error: "a corpus with the slug already exists" }, 409),
      jsonResponse(corpus(), 201)
    )
    await createCorpus("org_1", { name: "Handbook" })
    expect(sentBody(fetchMock, 0).slug).toBe("handbook")
    expect(sentBody(fetchMock, 1).slug).toMatch(/^handbook-[a-z0-9]{6}$/)
  })

  it("gives up after three slugs, and never retries another error", async () => {
    const taken = () =>
      jsonResponse({ error: "a corpus with the slug already exists" }, 409)
    const fetchMock = stubFetch(taken(), taken(), taken())
    await expect(createCorpus("org_1", { name: "Handbook" })).rejects.toThrow(
      ApiError
    )
    expect(fetchMock).toHaveBeenCalledTimes(3)

    const forbidden = stubFetch(
      jsonResponse({ error: "you may not do that here" }, 403)
    )
    await expect(
      createCorpus("org_1", { name: "Wiki", scope: "organization" })
    ).rejects.toMatchObject({ status: 403 })
    expect(forbidden).toHaveBeenCalledTimes(1)
  })

  it("derives a slug rag accepts from any name", () => {
    expect(corpusSlugBase("Q4 — Customer Insights!")).toBe(
      "q4-customer-insights"
    )
    expect(corpusSlugBase("日本語")).toBe("knowledge-base")
    expect(corpusSlugBase("A")).toBe("knowledge-base")
    const long = corpusSlugBase(`${"a".repeat(62)} b`)
    expect(long.length).toBeLessThanOrEqual(63)
    expect(long).toMatch(/^[a-z0-9][a-z0-9-]{1,62}$/)
    expect(long.endsWith("-")).toBe(false)
  })

  it("deletes a corpus", async () => {
    const fetchMock = stubFetch(new Response(null, { status: 204 }))
    await deleteCorpus("org_1", "cor_1")
    expect(fetchMock).toHaveBeenCalledWith(
      "/v1/organizations/org_1/corpora/cor_1",
      expect.objectContaining({ method: "DELETE" })
    )
  })
})

describe("documents", () => {
  it("lists a page with the cursor, limit and status given", async () => {
    const fetchMock = stubFetch(
      jsonResponse({ documents: [], next_cursor: "b" }),
      jsonResponse({ documents: [] })
    )
    await listDocuments("org_1", "cor_1", {
      cursor: "a",
      limit: 50,
      status: "failed",
    })
    await listDocuments("org_1", "cor_1")
    expect(fetchMock.mock.calls[0]?.[0]).toBe(
      "/v1/organizations/org_1/corpora/cor_1/documents?status=failed&cursor=a&limit=50"
    )
    expect(fetchMock.mock.calls[1]?.[0]).toBe(
      "/v1/organizations/org_1/corpora/cor_1/documents"
    )
  })

  it("adds a note as inline text with no reference", async () => {
    const fetchMock = stubFetch(jsonResponse(document(), 202))
    await addDocument("org_1", "cor_1", {
      kind: "inline",
      title: " Decision log ",
      text: "We chose pgvector.",
    })
    expect(fetchMock).toHaveBeenCalledWith(
      "/v1/organizations/org_1/corpora/cor_1/documents",
      expect.objectContaining({ method: "POST" })
    )
    expect(sentBody(fetchMock)).toEqual({
      source: { kind: "inline" },
      title: "Decision log",
      text: "We chose pgvector.",
    })
  })

  it("adds a link as external text that keeps its address", async () => {
    const fetchMock = stubFetch(jsonResponse(document(), 202))
    await addDocument("org_1", "cor_1", {
      kind: "external",
      url: " https://example.com/guide ",
      text: "The guide.",
    })
    expect(sentBody(fetchMock)).toEqual({
      source: { kind: "external", ref: "https://example.com/guide" },
      text: "The guide.",
    })
  })

  it("adds a files object by reference, with no text of its own", async () => {
    const fetchMock = stubFetch(jsonResponse(document(), 202))
    await addDocument("org_1", "cor_1", {
      kind: "file",
      fileId: "file_1",
      contentType: "text/markdown",
    })
    expect(sentBody(fetchMock)).toEqual({
      source: { kind: "file", ref: "file_1" },
      content_type: "text/markdown",
    })
  })

  it("deletes a document", async () => {
    const fetchMock = stubFetch(new Response(null, { status: 204 }))
    await deleteDocument("org_1", "cor_1", "doc_1")
    expect(fetchMock).toHaveBeenCalledWith(
      "/v1/organizations/org_1/corpora/cor_1/documents/doc_1",
      expect.objectContaining({ method: "DELETE" })
    )
  })
})

describe("addFileDocument", () => {
  const stored = {
    id: "file_9",
    organization_id: "org_1",
    scope: "team",
    filename: "notes.md",
    content_type: "application/octet-stream",
    purpose: "upload",
    size_bytes: 12,
    sha256: "",
    created_by: "user-1",
    created_at: "2026-09-20T10:00:00Z",
    expires_at: "2026-10-20T10:00:00Z",
  }

  it("uploads to files in the corpus's scope, then adds the file by reference", async () => {
    const fetchMock = stubFetch(
      jsonResponse(stored, 201),
      jsonResponse(document({ source: { kind: "file", ref: "file_9" } }), 202)
    )
    const file = new File(["# Notes"], "notes.md", { type: "" })
    await addFileDocument(
      "org_1",
      { id: "cor_1", scope: "team", team_id: "team_1" },
      { file, title: "Team notes" }
    )

    expect(fetchMock.mock.calls[0]?.[0]).toBe("/v1/organizations/org_1/files")
    const form = sentBody(fetchMock, 0) as FormData
    expect(form.get("scope")).toBe("team")
    expect(form.get("team_id")).toBe("team_1")
    expect(form.get("purpose")).toBe("upload")
    expect(form.get("retention_days")).toBe("30")

    expect(fetchMock.mock.calls[1]?.[0]).toBe(
      "/v1/organizations/org_1/corpora/cor_1/documents"
    )
    expect(sentBody(fetchMock, 1)).toEqual({
      source: { kind: "file", ref: "file_9" },
      title: "Team notes",
      content_type: "text/markdown",
    })
  })

  it("deletes the upload when rag refuses the document", async () => {
    const fetchMock = stubFetch(
      jsonResponse(stored, 201),
      jsonResponse({ error: "document text must be valid UTF-8" }, 400),
      new Response(null, { status: 204 })
    )
    const file = new File(["\u0000"], "notes.txt", { type: "text/plain" })
    await expect(
      addFileDocument("org_1", { id: "cor_1", scope: "member" }, { file })
    ).rejects.toMatchObject({ status: 400 })
    expect(fetchMock.mock.calls[2]?.[0]).toBe(
      "/v1/organizations/org_1/files/file_9"
    )
    expect(fetchMock.mock.calls[2]?.[1]).toMatchObject({ method: "DELETE" })
  })

  it("refuses a file rag cannot index before uploading anything", async () => {
    const fetchMock = stubFetch()
    const file = new File(["%PDF"], "brief.pdf", { type: "application/pdf" })
    await expect(
      addFileDocument("org_1", { id: "cor_1", scope: "member" }, { file })
    ).rejects.toThrow("Knowledge bases take text")
    expect(fetchMock).not.toHaveBeenCalled()
  })

  it("reads a file's content type from its type, then its extension", () => {
    expect(indexableContentType({ name: "a.md", type: "" })).toBe(
      "text/markdown"
    )
    expect(
      indexableContentType({ name: "a.csv", type: "application/vnd.ms-excel" })
    ).toBe("text/csv")
    expect(
      indexableContentType({ name: "page", type: "text/html; charset=utf-8" })
    ).toBe("text/html")
    expect(indexableContentType({ name: "a.JSON", type: "" })).toBe(
      "application/json"
    )
    expect(
      indexableContentType({ name: "a.pdf", type: "application/pdf" })
    ).toBeUndefined()
    expect(indexableContentType({ name: "README", type: "" })).toBeUndefined()
  })
})

describe("search and answer", () => {
  it("searches with the query trimmed and top_k when given", async () => {
    const fetchMock = stubFetch(
      jsonResponse({ matches: [], usage: {}, reranked: false })
    )
    await searchCorpus("org_1", "cor_1", { query: " launch date ", topK: 5 })
    expect(fetchMock).toHaveBeenCalledWith(
      "/v1/organizations/org_1/corpora/cor_1/search",
      expect.objectContaining({ method: "POST" })
    )
    expect(sentBody(fetchMock)).toEqual({ query: "launch date", top_k: 5 })
  })

  it("answers with only the query when top_k is left to the server", async () => {
    const answered = {
      answer: {
        text: "The launch is on 14 October [1].",
        citations: [],
        model: "gemini",
        usage: {},
        grounded: true,
      },
      matches: [],
    }
    const fetchMock = stubFetch(jsonResponse(answered))
    await expect(
      answerCorpus("org_1", "cor_1", { query: "When?" })
    ).resolves.toEqual(answered)
    expect(fetchMock.mock.calls[0]?.[0]).toBe(
      "/v1/organizations/org_1/corpora/cor_1/answer"
    )
    expect(sentBody(fetchMock)).toEqual({ query: "When?" })
  })

  it("reads the deployment's capabilities", async () => {
    const fetchMock = stubFetch(
      jsonResponse({
        embedding_providers: ["vertex"],
        backends: ["pgvector"],
        models: [],
        content_types: ["text/plain"],
        answering: true,
        reranking: false,
      })
    )
    await expect(getCapabilities()).resolves.toMatchObject({ answering: true })
    expect(fetchMock.mock.calls[0]?.[0]).toBe("/v1/capabilities")
  })
})

describe("reading corpora and documents", () => {
  it("finds the description and the connected-apps corpus in metadata", () => {
    expect(corpusDescription(corpus())).toBe("")
    expect(
      corpusDescription(corpus({ metadata: { description: " Specs. " } }))
    ).toBe("Specs.")
    expect(
      isFromConnectedApps(corpus({ metadata: { origin: "knowledge" } }))
    ).toBe(true)
    expect(isFromConnectedApps(corpus())).toBe(false)
  })

  it("says where each document came from", () => {
    expect(documentOrigin(document())).toEqual({ kind: "note" })
    expect(
      documentOrigin(document({ source: { kind: "file", ref: "file_1" } }))
    ).toEqual({ kind: "file", fileId: "file_1" })
    expect(
      documentOrigin(
        document({
          source: { kind: "external", ref: "https://example.com/a?b=1" },
        })
      )
    ).toEqual({
      kind: "link",
      href: "https://example.com/a?b=1",
      host: "example.com",
    })
    expect(
      documentOrigin(
        document({
          source: { kind: "external", ref: "knowledge:kdoc_1" },
          metadata: { provider: "googledrive" },
        })
      )
    ).toEqual({
      kind: "app",
      provider: "googledrive",
      name: "Google Drive",
      href: undefined,
    })
  })

  it("links only http and https sources", () => {
    expect(
      externalSourceLink({ kind: "external", ref: "https://example.com" })
    ).toBe("https://example.com/")
    expect(
      externalSourceLink({ kind: "external", ref: "javascript:alert(1)" })
    ).toBeUndefined()
    expect(
      externalSourceLink({ kind: "external", ref: "knowledge:kdoc_1" })
    ).toBeUndefined()
    expect(
      externalSourceLink({ kind: "file", ref: "https://example.com" })
    ).toBeUndefined()
  })

  it("names connected apps the way their brands do", () => {
    expect(connectedAppName("github")).toBe("GitHub")
    expect(connectedAppName("googledrive")).toBe("Google Drive")
    expect(connectedAppName("dropbox")).toBe("Dropbox")
  })

  it("parses timestamps whose fraction runs past milliseconds", () => {
    expect(parseTimestamp("2026-09-23T23:37:51.36611+05:30")).toBe(
      Date.parse("2026-09-23T18:07:51.366Z")
    )
    expect(parseTimestamp(undefined)).toBeNaN()
  })
})

describe("who may do what, as rag's service decides", () => {
  const owner = { userId: "user-1", role: "member" as const }
  const other = { userId: "user-2", role: "member" as const }
  const admin = { userId: "user-3", role: "admin" as const }

  it("lets only admins make an organization corpus", () => {
    expect(canCreateOrganizationCorpus(owner)).toBe(false)
    expect(canCreateOrganizationCorpus(admin)).toBe(true)
    expect(canCreateOrganizationCorpus({ role: "owner" })).toBe(true)
  })

  it("mirrors mayWrite", () => {
    expect(canWriteCorpus(corpus(), owner)).toBe(true)
    expect(canWriteCorpus(corpus(), other)).toBe(false)
    expect(canWriteCorpus(corpus(), admin)).toBe(true)
    const shared = corpus({ scope: "organization", member_id: undefined })
    expect(canWriteCorpus(shared, owner)).toBe(false)
    expect(canWriteCorpus(shared, admin)).toBe(true)
    expect(canWriteCorpus(corpus({ scope: "team" }), other)).toBe(true)
  })

  it("mirrors mayDelete", () => {
    expect(canDeleteCorpus(corpus(), owner)).toBe(true)
    expect(canDeleteCorpus(corpus(), other)).toBe(false)
    expect(canDeleteCorpus(corpus({ scope: "organization" }), owner)).toBe(
      false
    )
    expect(canDeleteCorpus(corpus({ scope: "organization" }), admin)).toBe(true)
    expect(canDeleteCorpus(corpus({ scope: "team" }), owner)).toBe(true)
    expect(canDeleteCorpus(corpus({ scope: "team" }), other)).toBe(false)
  })
})

describe("ragErrorMessage", () => {
  it("shows rag's own reason for a 400 or 409 as a sentence", () => {
    expect(
      ragErrorMessage(new ApiError("document text is required", 400), "x")
    ).toBe("Document text is required.")
  })

  it("says the rest plainly", () => {
    expect(ragErrorMessage(new ApiError("x", 403), "fallback")).toBe(
      "You don't have permission to do that here."
    )
    expect(
      ragErrorMessage(new ApiError("not found", 404), "fallback", {
        notFound: "It's gone.",
      })
    ).toBe("It's gone.")
    expect(ragErrorMessage(new ApiError("x", 413), "fallback")).toMatch(
      /too large/
    )
    expect(ragErrorMessage(new ApiError("boom", 500), "fallback")).toBe(
      "fallback"
    )
    expect(ragErrorMessage(new TypeError("Failed to fetch"), "fallback")).toBe(
      "fallback"
    )
    expect(ragErrorMessage(new ApiError("Request failed", 400), "fb")).toBe(
      "fb"
    )
  })
})

describe("queries", () => {
  it("keys everything under rag and the organization", () => {
    expect(corporaQueryOptions("org_1").queryKey).toEqual([
      "rag",
      "organization",
      "org_1",
      "corpora",
    ])
    expect(corpusQueryOptions("org_1", "cor_1").queryKey).toEqual([
      "rag",
      "organization",
      "org_1",
      "corpus",
      "cor_1",
    ])
    expect(documentsInfiniteQueryOptions("org_1", "cor_1").queryKey).toEqual([
      ...ragKeys.corpus("org_1", "cor_1"),
      "documents",
    ])
    expect(capabilitiesQueryOptions().queryKey).toEqual(["rag", "capabilities"])
  })

  it("pages documents by cursor, fifty at a time", async () => {
    const firstPage = () =>
      jsonResponse({ documents: [document()], next_cursor: "next" })
    const fetchMock = stubFetch(
      firstPage(),
      firstPage(),
      jsonResponse({ documents: [document({ id: "doc_2" })] })
    )
    const queryClient = new QueryClient()
    const options = documentsInfiniteQueryOptions("org_1", "cor_1")
    const first = await queryClient.fetchInfiniteQuery(options)
    expect(fetchMock.mock.calls[0]?.[0]).toBe(
      "/v1/organizations/org_1/corpora/cor_1/documents?limit=50"
    )
    expect(
      options.getNextPageParam(first.pages[0], first.pages, undefined, [])
    ).toBe("next")
    expect(
      options.getNextPageParam({ documents: [] }, first.pages, "next", [])
    ).toBeUndefined()

    const both = await queryClient.fetchInfiniteQuery({ ...options, pages: 2 })
    expect(
      both.pages.flatMap((page) => page.documents.map((d) => d.id))
    ).toEqual(["doc_1", "doc_2"])
    expect(fetchMock.mock.calls.at(-1)?.[0]).toBe(
      "/v1/organizations/org_1/corpora/cor_1/documents?cursor=next&limit=50"
    )
  })

  it("polls only while something is indexing, and less as it drags on", () => {
    const now = Date.parse("2026-09-20T10:00:00Z")
    const at = (ms: number) => new Date(now - ms).toISOString()
    const pages = (...documents: CorpusDocument[]): DocumentPage[] => [
      { documents },
    ]

    expect(documentsPollInterval(undefined, now)).toBe(false)
    expect(documentsPollInterval(pages(document()), now)).toBe(false)
    expect(
      documentsPollInterval(
        pages(document({ status: "failed" }), document()),
        now
      )
    ).toBe(false)

    const fresh = document({
      status: "pending",
      attempts: 0,
      updated_at: at(0),
    })
    const working = document({ status: "ingesting", updated_at: at(1_000) })
    const slow = document({
      status: "ingesting",
      updated_at: at(FRESH_INDEXING_MS + 1),
    })
    const retrying = document({
      status: "pending",
      attempts: 2,
      updated_at: at(0),
    })

    expect(documentsPollInterval(pages(fresh), now)).toBe(INDEXING_POLL_MS)
    expect(documentsPollInterval(pages(working), now)).toBe(INDEXING_POLL_MS)
    expect(documentsPollInterval(pages(slow), now)).toBe(SLOW_INDEXING_POLL_MS)
    expect(documentsPollInterval(pages(retrying), now)).toBe(RETRY_POLL_MS)
    expect(documentsPollInterval(pages(retrying, slow, fresh), now)).toBe(
      INDEXING_POLL_MS
    )
    expect(INDEXING_POLL_MS).toBeLessThan(SLOW_INDEXING_POLL_MS)
    expect(SLOW_INDEXING_POLL_MS).toBeLessThan(RETRY_POLL_MS)
  })

  it("stops polling in the background", () => {
    const options = documentsInfiniteQueryOptions("org_1", "cor_1")
    expect(options.refetchIntervalInBackground).toBe(false)
    expect(typeof options.refetchInterval).toBe("function")
  })
})

describe("cache updates", () => {
  it("puts a new corpus first and caches it for its detail", async () => {
    const queryClient = new QueryClient()
    queryClient.setQueryData(ragKeys.corpora("org_1"), [corpus()])
    const created = corpus({ id: "cor_2", name: "New" })
    await afterCreateCorpus(queryClient, "org_1", created)
    expect(
      queryClient
        .getQueryData<Corpus[]>(ragKeys.corpora("org_1"))
        ?.map((item) => item.id)
    ).toEqual(["cor_2", "cor_1"])
    expect(queryClient.getQueryData(ragKeys.corpus("org_1", "cor_2"))).toEqual(
      created
    )
  })

  it("takes a deleted corpus out of the list", async () => {
    const queryClient = new QueryClient()
    queryClient.setQueryData(ragKeys.corpora("org_1"), [
      corpus(),
      corpus({ id: "cor_2" }),
    ])
    await afterDeleteCorpus(queryClient, "org_1", "cor_1")
    expect(
      queryClient
        .getQueryData<Corpus[]>(ragKeys.corpora("org_1"))
        ?.map((item) => item.id)
    ).toEqual(["cor_2"])
    expect(
      queryClient.getQueryState(ragKeys.corpora("org_1"))?.isInvalidated
    ).toBe(true)
  })

  it("marks a corpus, its documents and the list stale after a document change", async () => {
    const queryClient = new QueryClient()
    queryClient.setQueryData(ragKeys.corpora("org_1"), [corpus()])
    queryClient.setQueryData(ragKeys.corpus("org_1", "cor_1"), corpus())
    queryClient.setQueryData(ragKeys.documents("org_1", "cor_1"), {
      pages: [],
      pageParams: [],
    })
    queryClient.setQueryData(ragKeys.corpus("org_1", "cor_2"), corpus())
    await afterDocumentsChange(queryClient, "org_1", "cor_1")
    const stale = (key: readonly unknown[]) =>
      queryClient.getQueryState(key)?.isInvalidated
    expect(stale(ragKeys.corpora("org_1"))).toBe(true)
    expect(stale(ragKeys.corpus("org_1", "cor_1"))).toBe(true)
    expect(stale(ragKeys.documents("org_1", "cor_1"))).toBe(true)
    expect(stale(ragKeys.corpus("org_1", "cor_2"))).toBe(false)
  })
})
