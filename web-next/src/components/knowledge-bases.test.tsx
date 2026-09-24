// @vitest-environment jsdom

import { afterEach, describe, expect, it, vi } from "vitest"
import {
  cleanup,
  fireEvent,
  render,
  screen,
  waitFor,
  within,
} from "@testing-library/react"
import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import { useState } from "react"
import type { ComponentProps } from "react"
import type { Corpus, CorpusDocument } from "@/lib/rag/client"
import { KnowledgeBases } from "./knowledge-bases"
import type { SelectedKnowledgeBase } from "./knowledge-bases"

type Handler = (request: {
  url: URL
  method: string
  body: unknown
}) => Response | Promise<Response>

function json(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  })
}

/**
 * Routes by `METHOD /path`. A value that is not a function is answered as
 * JSON; an unknown route is a 599 so a test notices it.
 */
function stubApi(routes: Record<string, unknown>) {
  const calls: Array<{
    method: string
    path: string
    search: URLSearchParams
    body: unknown
  }> = []
  const fetchMock = vi.fn<typeof fetch>(async (input, init) => {
    const url = new URL(
      input instanceof Request ? input.url : String(input),
      "http://localhost"
    )
    const method = init?.method ?? "GET"
    const body =
      typeof init?.body === "string" ? JSON.parse(init.body) : init?.body
    calls.push({ method, path: url.pathname, search: url.searchParams, body })
    const route = routes[`${method} ${url.pathname}`]
    if (route === undefined) {
      return json({ error: `no route for ${method} ${url.pathname}` }, 599)
    }
    return typeof route === "function"
      ? (route as Handler)({ url, method, body })
      : json(route)
  })
  vi.stubGlobal("fetch", fetchMock)
  return {
    fetchMock,
    calls,
    find: (method: string, path: string) =>
      calls.filter((call) => call.method === method && call.path === path),
  }
}

const ago = (ms: number) => new Date(Date.now() - ms).toISOString()
const day = 86_400_000

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
    metadata: { description: "How we build." },
    document_count: 0,
    chunk_count: 0,
    created_by: "user-1",
    created_at: ago(2 * day),
    updated_at: ago(2 * day),
    ...overrides,
  }
}

function document(overrides: Partial<CorpusDocument> = {}): CorpusDocument {
  return {
    id: "doc_1",
    corpus_id: "cor_1",
    organization_id: "org_1",
    source: { kind: "inline" },
    title: "Decision log",
    content_type: "text/plain",
    content_sha256: "",
    byte_size: 2048,
    status: "ready",
    chunk_count: 2,
    attempts: 1,
    created_by: "user-1",
    created_at: ago(5 * 60_000),
    updated_at: ago(5 * 60_000),
    ...overrides,
  }
}

const capabilities = (answering: boolean) => ({
  embedding_providers: ["vertex"],
  backends: ["pgvector"],
  models: [],
  content_types: ["text/plain"],
  answering,
  reranking: false,
})

const corpusPath = "/v1/organizations/org_1/corpora/cor_1"

/** Routes for one open knowledge base; `documents` may change between reads. */
function detailRoutes(
  base: Corpus,
  documents: () => CorpusDocument[],
  { answering = true }: { answering?: boolean } = {}
) {
  return {
    "GET /v1/capabilities": capabilities(answering),
    "GET /v1/organizations/org_1/corpora": { corpora: [base] },
    [`GET ${corpusPath}`]: () =>
      json({ ...base, document_count: documents().length }),
    [`GET ${corpusPath}/documents`]: () => json({ documents: documents() }),
  }
}

function renderKnowledge(
  props: Partial<ComponentProps<typeof KnowledgeBases>> = {}
) {
  const onSelect = vi.fn()
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })
  function Harness() {
    const [selected, setSelected] = useState<SelectedKnowledgeBase | null>(
      props.selected ?? null
    )
    return (
      <KnowledgeBases
        organizationId="org_1"
        organizationName="Acme"
        organizationRole="member"
        userId="user-1"
        {...props}
        selected={selected}
        onSelect={(base) => {
          onSelect(base)
          setSelected(base)
        }}
      />
    )
  }
  render(
    <QueryClientProvider client={client}>
      <Harness />
    </QueryClientProvider>
  )
  return { onSelect }
}

const openHandbook = { selected: { id: "cor_1", name: "Handbook" } }

afterEach(() => {
  cleanup()
  vi.unstubAllGlobals()
})

describe("the knowledge bases list", () => {
  it("shows the member's real knowledge bases, never examples", async () => {
    stubApi({
      "GET /v1/capabilities": capabilities(true),
      "GET /v1/organizations/org_1/corpora": {
        corpora: [
          corpus({ document_count: 3 }),
          corpus({
            id: "cor_2",
            name: "Company wiki",
            scope: "organization",
            member_id: undefined,
            metadata: undefined,
            document_count: 1,
            created_at: ago(30_000),
            updated_at: ago(30_000),
          }),
          corpus({
            id: "cor_3",
            name: "Knowledge from connected apps",
            metadata: { origin: "knowledge" },
            document_count: 12,
          }),
        ],
      },
    })
    renderKnowledge()

    const handbook = await screen.findByRole("button", {
      name: "Open Handbook",
    })
    expect(within(handbook).getByText("How we build.")).toBeTruthy()
    expect(within(handbook).getByText("3 documents")).toBeTruthy()
    expect(within(handbook).getByText("Just you")).toBeTruthy()
    expect(within(handbook).getByText("Created 2 days ago")).toBeTruthy()

    const wiki = screen.getByRole("button", { name: "Open Company wiki" })
    expect(within(wiki).getByText("1 document")).toBeTruthy()
    expect(within(wiki).getByText("Whole workspace")).toBeTruthy()
    expect(within(wiki).getByText("Created just now")).toBeTruthy()

    const apps = screen.getByRole("button", {
      name: "Open Knowledge from connected apps",
    })
    expect(
      within(apps).getByText("Indexed from your connected apps.")
    ).toBeTruthy()
    expect(screen.getByText("3 bases")).toBeTruthy()
    expect(screen.queryByText("Product handbook")).toBeNull()
  })

  it("searches names and descriptions", async () => {
    stubApi({
      "GET /v1/capabilities": capabilities(true),
      "GET /v1/organizations/org_1/corpora": {
        corpora: [
          corpus(),
          corpus({
            id: "cor_2",
            name: "Customer insights",
            metadata: { description: "Interview notes" },
          }),
        ],
      },
    })
    renderKnowledge()
    await screen.findByRole("button", { name: "Open Handbook" })

    const search = screen.getByRole("searchbox", {
      name: "Search knowledge bases",
    })
    fireEvent.change(search, { target: { value: "interview" } })
    expect(
      screen.getByRole("button", { name: "Open Customer insights" })
    ).toBeTruthy()
    expect(screen.queryByRole("button", { name: "Open Handbook" })).toBeNull()
    expect(screen.getByText("1 base")).toBeTruthy()

    fireEvent.change(search, { target: { value: "nothing like this" } })
    expect(screen.getByText("No knowledge bases found")).toBeTruthy()
  })

  it("says so when there are none yet", async () => {
    stubApi({
      "GET /v1/capabilities": capabilities(true),
      "GET /v1/organizations/org_1/corpora": { corpora: [] },
    })
    renderKnowledge()
    expect(await screen.findByText("No knowledge bases yet")).toBeTruthy()
    expect(screen.queryByRole("searchbox")).toBeNull()
  })

  it("shows a failed read with a retry", async () => {
    let reads = 0
    stubApi({
      "GET /v1/capabilities": capabilities(true),
      "GET /v1/organizations/org_1/corpora": () =>
        ++reads === 1
          ? json({ error: "something went wrong" }, 500)
          : json({ corpora: [corpus()] }),
    })
    renderKnowledge()

    const alert = await screen.findByRole("alert")
    expect(alert.textContent).toContain("Couldn’t load your knowledge bases.")
    fireEvent.click(within(alert).getByRole("button", { name: "Try again" }))
    expect(
      await screen.findByRole("button", { name: "Open Handbook" })
    ).toBeTruthy()
  })

  it("labels the page a preview when the knowledge service is missing", async () => {
    const missing = () =>
      new Response("404 page not found", {
        status: 404,
        headers: { "Content-Type": "text/plain" },
      })
    stubApi({
      "GET /v1/capabilities": missing,
      "GET /v1/organizations/org_1/corpora": missing,
    })
    renderKnowledge()

    expect(
      await screen.findByText(/The knowledge service isn’t reachable here/)
    ).toBeTruthy()
    expect(
      screen.queryByRole("button", { name: "New knowledge base" })
    ).toBeNull()
  })

  it("reads nothing without a workspace", () => {
    const { fetchMock } = stubApi({})
    renderKnowledge({ organizationId: undefined })
    expect(screen.getByRole("status").textContent).toContain(
      "Sign in to a workspace"
    )
    expect(fetchMock).not.toHaveBeenCalled()
  })
})

describe("creating a knowledge base", () => {
  it("creates a real member corpus and opens it", async () => {
    const created = corpus({
      id: "cor_9",
      name: "Engineering handbook",
      slug: "engineering-handbook",
      metadata: { description: "Technical decisions and guides." },
    })
    const api = stubApi({
      "GET /v1/capabilities": capabilities(true),
      "GET /v1/organizations/org_1/corpora": { corpora: [] },
      "POST /v1/organizations/org_1/corpora": () => json(created, 201),
      "GET /v1/organizations/org_1/corpora/cor_9": created,
      "GET /v1/organizations/org_1/corpora/cor_9/documents": {
        documents: [],
      },
    })
    const { onSelect } = renderKnowledge()
    await screen.findByText("No knowledge bases yet")

    fireEvent.click(screen.getByRole("button", { name: "New knowledge base" }))
    expect(
      screen.queryByRole("radiogroup", { name: "Who can use it" })
    ).toBeNull()
    expect(
      screen.getByText(
        "Only you can see it, along with your workspace’s admins."
      )
    ).toBeTruthy()
    fireEvent.click(screen.getByRole("button", { name: "Create base" }))
    expect(screen.getByText("Use at least 2 characters.")).toBeTruthy()
    expect(api.find("POST", "/v1/organizations/org_1/corpora")).toHaveLength(0)

    fireEvent.change(screen.getByRole("textbox", { name: "Name" }), {
      target: { value: "Engineering handbook" },
    })
    fireEvent.change(screen.getByRole("textbox", { name: "Description" }), {
      target: { value: "Technical decisions and guides." },
    })
    fireEvent.click(screen.getByRole("button", { name: "Create base" }))

    await waitFor(() =>
      expect(onSelect).toHaveBeenCalledWith({
        id: "cor_9",
        name: "Engineering handbook",
      })
    )
    expect(
      api.find("POST", "/v1/organizations/org_1/corpora")[0]?.body
    ).toEqual({
      name: "Engineering handbook",
      slug: "engineering-handbook",
      scope: "member",
      metadata: { description: "Technical decisions and guides." },
    })
    expect(await screen.findByText("No documents yet")).toBeTruthy()
    expect(screen.getByText("Technical decisions and guides.")).toBeTruthy()
  })

  it("lets an admin share one with the whole workspace", async () => {
    const created = corpus({ id: "cor_9", scope: "organization" })
    const api = stubApi({
      "GET /v1/capabilities": capabilities(true),
      "GET /v1/organizations/org_1/corpora": { corpora: [] },
      "POST /v1/organizations/org_1/corpora": () => json(created, 201),
      "GET /v1/organizations/org_1/corpora/cor_9": created,
      "GET /v1/organizations/org_1/corpora/cor_9/documents": {
        documents: [],
      },
    })
    renderKnowledge({ organizationRole: "admin", userId: "user-3" })
    await screen.findByText("No knowledge bases yet")

    fireEvent.click(screen.getByRole("button", { name: "New knowledge base" }))
    fireEvent.change(screen.getByRole("textbox", { name: "Name" }), {
      target: { value: "Company wiki" },
    })
    fireEvent.click(screen.getByRole("radio", { name: "Everyone in Acme" }))
    expect(
      screen.getByText(
        "Everyone in the workspace can search it. Only admins can change what’s in it."
      )
    ).toBeTruthy()
    fireEvent.click(screen.getByRole("button", { name: "Create base" }))

    await waitFor(() =>
      expect(
        api.find("POST", "/v1/organizations/org_1/corpora")[0]?.body
      ).toEqual({
        name: "Company wiki",
        slug: "company-wiki",
        scope: "organization",
      })
    )
  })

  it("explains a refusal", async () => {
    stubApi({
      "GET /v1/capabilities": capabilities(true),
      "GET /v1/organizations/org_1/corpora": { corpora: [] },
      "POST /v1/organizations/org_1/corpora": () =>
        json({ error: "you may not do that here" }, 403),
    })
    renderKnowledge()
    await screen.findByText("No knowledge bases yet")

    fireEvent.click(screen.getByRole("button", { name: "New knowledge base" }))
    fireEvent.change(screen.getByRole("textbox", { name: "Name" }), {
      target: { value: "Wiki" },
    })
    fireEvent.click(screen.getByRole("button", { name: "Create base" }))
    expect((await screen.findByRole("alert")).textContent).toBe(
      "You don't have permission to do that here."
    )
  })
})

describe("a knowledge base's documents", () => {
  it("shows every document's real status and where it came from", async () => {
    const documents = [
      document(),
      document({
        id: "doc_2",
        title: "",
        source: { kind: "external", ref: "https://example.com/pricing" },
        status: "pending",
        attempts: 0,
      }),
      document({
        id: "doc_3",
        title: "launch-notes.md",
        source: { kind: "file", ref: "file_1" },
        content_type: "text/markdown",
        status: "ingesting",
      }),
      document({
        id: "doc_4",
        title: "Huge export",
        status: "failed",
        failure_reason: "embed: quota exceeded",
        attempts: 5,
      }),
      document({
        id: "doc_5",
        title: "Q3 plan",
        source: { kind: "external", ref: "https://docs.google.com/d/1" },
        metadata: { provider: "googledrive" },
        status: "pending",
        attempts: 2,
      }),
    ]
    stubApi(detailRoutes(corpus(), () => documents))
    renderKnowledge(openHandbook)

    const list = await screen.findByRole("list")
    const rows = within(list).getAllByRole("listitem")
    expect(rows).toHaveLength(5)

    expect(within(rows[0]).getByText("Ready")).toBeTruthy()
    expect(
      within(rows[0]).getByText(/^Note · 2 KB · Added 5 minutes ago$/)
    ).toBeTruthy()

    const link = within(rows[1]).getByRole("link", { name: "example.com" })
    expect(link.getAttribute("href")).toBe("https://example.com/pricing")
    expect(link.getAttribute("target")).toBe("_blank")
    expect(within(rows[1]).getByText("Pending")).toBeTruthy()

    expect(within(rows[2]).getByText("Indexing")).toBeTruthy()
    expect(within(rows[2]).getByText(/^File · Markdown/)).toBeTruthy()

    expect(within(rows[3]).getByText("Failed")).toBeTruthy()
    expect(within(rows[3]).getByText("embed: quota exceeded")).toBeTruthy()

    expect(within(rows[4]).getByText(/^Google Drive ·/)).toBeTruthy()
    expect(
      within(rows[4]).getByText(
        "2 attempts didn’t finish. It will be tried again."
      )
    ).toBeTruthy()
    expect(screen.getByText("5 · 3 indexing")).toBeTruthy()
  })

  it("shows a quiet Admins only badge on a restricted document", async () => {
    stubApi(
      detailRoutes(corpus(), () => [
        document({ access: "admins" }),
        document({ id: "doc_2", title: "Roadmap", access: "members" }),
      ])
    )
    renderKnowledge(openHandbook)

    const restricted = (await screen.findByText("Decision log")).closest("li")!
    expect(within(restricted).getByText("Admins only")).toBeTruthy()
    const open = screen.getByText("Roadmap").closest("li")!
    expect(within(open).queryByText("Admins only")).toBeNull()
  })

  it("loads more with the cursor the last page returned", async () => {
    const api = stubApi({
      ...detailRoutes(corpus({ document_count: 2 }), () => []),
      [`GET ${corpusPath}`]: corpus({ document_count: 2 }),
      [`GET ${corpusPath}/documents`]: ({ url }: { url: URL }) =>
        url.searchParams.get("cursor") === "page-2"
          ? json({ documents: [document({ id: "doc_2", title: "Older" })] })
          : json({ documents: [document()], next_cursor: "page-2" }),
    })
    renderKnowledge(openHandbook)

    await screen.findByText("Decision log")
    expect(
      api.find("GET", `${corpusPath}/documents`)[0]?.search.get("limit")
    ).toBe("50")
    fireEvent.click(screen.getByRole("button", { name: "Load more" }))
    expect(await screen.findByText("Older")).toBeTruthy()
    expect(
      api.find("GET", `${corpusPath}/documents`).at(-1)?.search.get("cursor")
    ).toBe("page-2")
    expect(screen.queryByRole("button", { name: "Load more" })).toBeNull()
  })

  it("says when the knowledge base is gone", async () => {
    stubApi({
      "GET /v1/capabilities": capabilities(true),
      "GET /v1/organizations/org_1/corpora": { corpora: [] },
      [`GET ${corpusPath}`]: () => json({ error: "not found" }, 404),
      [`GET ${corpusPath}/documents`]: () => json({ error: "not found" }, 404),
    })
    const { onSelect } = renderKnowledge(openHandbook)

    expect(
      await screen.findByText("This knowledge base is no longer here")
    ).toBeTruthy()
    fireEvent.click(screen.getByRole("button", { name: "All knowledge bases" }))
    expect(onSelect).toHaveBeenCalledWith(null)
  })
})

describe("adding content", () => {
  function addingRoutes(documents: CorpusDocument[]) {
    return detailRoutes(corpus(), () => documents)
  }

  it("offers admins-only access to an admin in an organization base", async () => {
    stubApi(
      detailRoutes(
        corpus({ scope: "organization", member_id: undefined }),
        () => []
      )
    )
    renderKnowledge({ ...openHandbook, organizationRole: "admin" })
    await screen.findByText("No documents yet")

    fireEvent.click(screen.getAllByRole("button", { name: "Add content" })[0])
    const group = screen.getByRole("group", { name: "Who can find it" })
    expect(
      within(group).getByRole("radio", { name: "Everyone with access" })
    ).toBeTruthy()
    expect(
      within(group).getByRole("radio", { name: "Admins only" })
    ).toBeTruthy()
  })

  it("hides admins-only access on a 'Just you' base, even for an admin", async () => {
    stubApi(detailRoutes(corpus(), () => []))
    renderKnowledge({ ...openHandbook, organizationRole: "admin" })
    await screen.findByText("No documents yet")

    fireEvent.click(screen.getAllByRole("button", { name: "Add content" })[0])
    expect(screen.queryByRole("group", { name: "Who can find it" })).toBeNull()
  })

  // A plain member never reaches this form on an organization base at all —
  // `canWriteCorpus` refuses "Add content" there first — so the "hidden for
  // a member" half of the rule is covered at the unit level instead:
  // `canSetAdminsOnlyAccess` in `lib/rag/client.test.ts`.

  it("adds a note as inline text and shows it indexing", async () => {
    const documents: CorpusDocument[] = []
    const api = stubApi({
      ...addingRoutes(documents),
      [`POST ${corpusPath}/documents`]: ({ body }: { body: unknown }) => {
        const added = document({
          id: "doc_new",
          title: (body as { title?: string }).title,
          status: "pending",
          attempts: 0,
          created_at: ago(0),
          updated_at: ago(0),
        })
        documents.unshift(added)
        return json(added, 202)
      },
    })
    renderKnowledge(openHandbook)
    await screen.findByText("No documents yet")

    fireEvent.click(screen.getAllByRole("button", { name: "Add content" })[0])
    fireEvent.click(screen.getByRole("button", { name: "Add note" }))
    expect(screen.getByText("Write or paste the note’s text.")).toBeTruthy()

    fireEvent.change(screen.getByRole("textbox", { name: /^Title/ }), {
      target: { value: "Decision log" },
    })
    fireEvent.change(screen.getByRole("textbox", { name: "Text" }), {
      target: { value: "We chose pgvector for local runs." },
    })
    fireEvent.click(screen.getByRole("button", { name: "Add note" }))

    await waitFor(() =>
      expect(api.find("POST", `${corpusPath}/documents`)[0]?.body).toEqual({
        source: { kind: "inline" },
        title: "Decision log",
        text: "We chose pgvector for local runs.",
      })
    )
    const row = (await screen.findByText("Decision log")).closest("li")!
    expect(within(row).getByText("Pending")).toBeTruthy()
    expect(screen.queryByRole("button", { name: "Add note" })).toBeNull()
  })

  it("adds a link with the text pasted from it", async () => {
    const api = stubApi({
      ...addingRoutes([]),
      [`POST ${corpusPath}/documents`]: () =>
        json(document({ id: "doc_new", status: "pending" }), 202),
    })
    renderKnowledge(openHandbook)
    await screen.findByText("No documents yet")

    fireEvent.click(screen.getAllByRole("button", { name: "Add content" })[0])
    fireEvent.click(screen.getByRole("radio", { name: "Link" }))
    fireEvent.change(screen.getByRole("textbox", { name: "Address" }), {
      target: { value: "example.com/pricing" },
    })
    fireEvent.change(
      screen.getByRole("textbox", { name: "Text from the page" }),
      { target: { value: "Plans start at $10." } }
    )
    fireEvent.click(screen.getByRole("button", { name: "Add link" }))
    expect(
      screen.getByText("Use a full web address, starting with https://.")
    ).toBeTruthy()

    fireEvent.change(screen.getByRole("textbox", { name: "Address" }), {
      target: { value: "https://example.com/pricing" },
    })
    fireEvent.click(screen.getByRole("button", { name: "Add link" }))
    await waitFor(() =>
      expect(api.find("POST", `${corpusPath}/documents`)[0]?.body).toEqual({
        source: { kind: "external", ref: "https://example.com/pricing" },
        text: "Plans start at $10.",
      })
    )
  })

  it("uploads a file to files, then adds it by reference", async () => {
    const api = stubApi({
      ...addingRoutes([]),
      "POST /v1/organizations/org_1/files": () =>
        json(
          {
            id: "file_7",
            organization_id: "org_1",
            scope: "member",
            filename: "notes.md",
            content_type: "application/octet-stream",
            purpose: "upload",
            size_bytes: 9,
            sha256: "",
            created_by: "user-1",
            created_at: ago(0),
            expires_at: ago(-30 * day),
          },
          201
        ),
      [`POST ${corpusPath}/documents`]: () =>
        json(
          document({ id: "doc_new", source: { kind: "file", ref: "file_7" } }),
          202
        ),
    })
    renderKnowledge(openHandbook)
    await screen.findByText("No documents yet")

    fireEvent.click(screen.getAllByRole("button", { name: "Add content" })[0])
    fireEvent.click(screen.getByRole("radio", { name: "File" }))
    const picker = screen.getByLabelText("File", {
      selector: 'input[type="file"]',
    })
    fireEvent.change(picker, {
      target: { files: [new File(["# Notes"], "notes.md", { type: "" })] },
    })
    fireEvent.click(screen.getByRole("button", { name: "Upload file" }))

    await waitFor(() =>
      expect(api.find("POST", `${corpusPath}/documents`)[0]?.body).toEqual({
        source: { kind: "file", ref: "file_7" },
        content_type: "text/markdown",
      })
    )
    const upload = api.find("POST", "/v1/organizations/org_1/files")[0]
    expect((upload.body as FormData).get("scope")).toBe("member")
  })

  it("refuses a file rag can't index before uploading it", async () => {
    const api = stubApi(addingRoutes([]))
    renderKnowledge(openHandbook)
    await screen.findByText("No documents yet")

    fireEvent.click(screen.getAllByRole("button", { name: "Add content" })[0])
    fireEvent.click(screen.getByRole("radio", { name: "File" }))
    fireEvent.change(
      screen.getByLabelText("File", { selector: 'input[type="file"]' }),
      {
        target: {
          files: [new File(["%PDF"], "brief.pdf", { type: "application/pdf" })],
        },
      }
    )
    fireEvent.click(screen.getByRole("button", { name: "Upload file" }))
    expect(screen.getByText(/Knowledge bases take text/)).toBeTruthy()
    expect(api.find("POST", "/v1/organizations/org_1/files")).toHaveLength(0)
  })

  function adminOrgRoutes(documents: CorpusDocument[]) {
    return detailRoutes(
      corpus({ scope: "organization", member_id: undefined }),
      () => documents
    )
  }

  it("sends access: admins for a note when Admins only is chosen", async () => {
    const api = stubApi({
      ...adminOrgRoutes([]),
      [`POST ${corpusPath}/documents`]: () =>
        json(document({ id: "doc_new", access: "admins" }), 202),
    })
    renderKnowledge({ ...openHandbook, organizationRole: "admin" })
    await screen.findByText("No documents yet")

    fireEvent.click(screen.getAllByRole("button", { name: "Add content" })[0])
    fireEvent.change(screen.getByRole("textbox", { name: "Text" }), {
      target: { value: "Only leadership should see this." },
    })
    fireEvent.click(screen.getByRole("radio", { name: "Admins only" }))
    fireEvent.click(screen.getByRole("button", { name: "Add note" }))

    await waitFor(() =>
      expect(api.find("POST", `${corpusPath}/documents`)[0]?.body).toEqual({
        source: { kind: "inline" },
        text: "Only leadership should see this.",
        access: "admins",
      })
    )
  })

  it("uploads an admins-only file with access, then adds it with access", async () => {
    const api = stubApi({
      ...adminOrgRoutes([]),
      "POST /v1/organizations/org_1/files": () =>
        json(
          {
            id: "file_8",
            organization_id: "org_1",
            scope: "organization",
            filename: "policy.md",
            content_type: "application/octet-stream",
            purpose: "upload",
            size_bytes: 20,
            sha256: "",
            created_by: "user-1",
            created_at: ago(0),
            expires_at: ago(-30 * day),
          },
          201
        ),
      [`POST ${corpusPath}/documents`]: () =>
        json(
          document({
            id: "doc_new",
            access: "admins",
            source: { kind: "file", ref: "file_8" },
          }),
          202
        ),
    })
    renderKnowledge({ ...openHandbook, organizationRole: "admin" })
    await screen.findByText("No documents yet")

    fireEvent.click(screen.getAllByRole("button", { name: "Add content" })[0])
    fireEvent.click(screen.getByRole("radio", { name: "File" }))
    fireEvent.click(screen.getByRole("radio", { name: "Admins only" }))
    fireEvent.change(
      screen.getByLabelText("File", { selector: 'input[type="file"]' }),
      { target: { files: [new File(["# Policy"], "policy.md", { type: "" })] } }
    )
    fireEvent.click(screen.getByRole("button", { name: "Upload file" }))

    await waitFor(() =>
      expect(api.find("POST", `${corpusPath}/documents`)[0]?.body).toEqual({
        source: { kind: "file", ref: "file_8" },
        content_type: "text/markdown",
        access: "admins",
      })
    )
    const upload = api.find("POST", "/v1/organizations/org_1/files")[0]
    expect((upload.body as FormData).get("access")).toBe("admins")
    expect((upload.body as FormData).get("scope")).toBe("organization")
  })

  it("explains a 403 refusing admins-only access in plain words", async () => {
    stubApi({
      ...adminOrgRoutes([]),
      [`POST ${corpusPath}/documents`]: () =>
        json({ error: "you may not do that here" }, 403),
    })
    renderKnowledge({ ...openHandbook, organizationRole: "admin" })
    await screen.findByText("No documents yet")

    fireEvent.click(screen.getAllByRole("button", { name: "Add content" })[0])
    fireEvent.change(screen.getByRole("textbox", { name: "Text" }), {
      target: { value: "Board minutes." },
    })
    fireEvent.click(screen.getByRole("radio", { name: "Admins only" }))
    fireEvent.click(screen.getByRole("button", { name: "Add note" }))

    expect((await screen.findByRole("alert")).textContent).toBe(
      "You don't have permission to do that here."
    )
  })

  it("explains a 400 file/access mismatch in plain words, and cleans up the upload", async () => {
    const api = stubApi({
      ...adminOrgRoutes([]),
      "POST /v1/organizations/org_1/files": () =>
        json(
          {
            id: "file_8",
            organization_id: "org_1",
            scope: "organization",
            filename: "policy.md",
            content_type: "application/octet-stream",
            purpose: "upload",
            size_bytes: 20,
            sha256: "",
            created_by: "user-1",
            created_at: ago(0),
            expires_at: ago(-30 * day),
          },
          201
        ),
      [`POST ${corpusPath}/documents`]: () =>
        json({ error: "set admin access on the file in Files first" }, 400),
      "DELETE /v1/organizations/org_1/files/file_8": () =>
        new Response(null, { status: 204 }),
    })
    renderKnowledge({ ...openHandbook, organizationRole: "admin" })
    await screen.findByText("No documents yet")

    fireEvent.click(screen.getAllByRole("button", { name: "Add content" })[0])
    fireEvent.click(screen.getByRole("radio", { name: "File" }))
    fireEvent.click(screen.getByRole("radio", { name: "Admins only" }))
    fireEvent.change(
      screen.getByLabelText("File", { selector: 'input[type="file"]' }),
      { target: { files: [new File(["# Policy"], "policy.md", { type: "" })] } }
    )
    fireEvent.click(screen.getByRole("button", { name: "Upload file" }))

    expect((await screen.findByRole("alert")).textContent).toBe(
      "Set admin access on the file in Files first."
    )
    await waitFor(() =>
      expect(
        api.find("DELETE", "/v1/organizations/org_1/files/file_8")
      ).toHaveLength(1)
    )
  })
})

describe("removing and deleting", () => {
  it("removes a document only after confirming", async () => {
    const documents = [document(), document({ id: "doc_2", title: "Roadmap" })]
    const api = stubApi({
      ...detailRoutes(corpus(), () => documents),
      [`DELETE ${corpusPath}/documents/doc_1`]: () => {
        documents.shift()
        return new Response(null, { status: 204 })
      },
    })
    renderKnowledge(openHandbook)
    await screen.findByText("Decision log")

    fireEvent.click(screen.getByRole("button", { name: "Remove Decision log" }))
    expect(screen.getByText(/Remove “Decision log”\?/)).toBeTruthy()
    fireEvent.click(screen.getByRole("button", { name: "Cancel" }))
    expect(api.find("DELETE", `${corpusPath}/documents/doc_1`)).toHaveLength(0)

    fireEvent.click(screen.getByRole("button", { name: "Remove Decision log" }))
    fireEvent.click(screen.getByRole("button", { name: "Remove" }))
    await waitFor(() => expect(screen.queryByText("Decision log")).toBeNull())
    expect(api.find("DELETE", `${corpusPath}/documents/doc_1`)).toHaveLength(1)
    expect(screen.getByText("Roadmap")).toBeTruthy()
  })

  it("deletes the knowledge base only after confirming", async () => {
    let deleted = false
    const api = stubApi({
      ...detailRoutes(corpus(), () => [document()]),
      "GET /v1/organizations/org_1/corpora": () =>
        json({ corpora: deleted ? [] : [corpus()] }),
      [`DELETE ${corpusPath}`]: () => {
        deleted = true
        return new Response(null, { status: 204 })
      },
    })
    const { onSelect } = renderKnowledge(openHandbook)
    await screen.findByText("Decision log")

    fireEvent.click(
      screen.getByRole("button", { name: "Delete knowledge base" })
    )
    expect(
      screen.getByText(
        /Delete “Handbook”\? Its 1 document and everything indexed from them go with it\./
      )
    ).toBeTruthy()
    fireEvent.click(screen.getByRole("button", { name: "Cancel" }))
    expect(api.find("DELETE", corpusPath)).toHaveLength(0)

    fireEvent.click(
      screen.getByRole("button", { name: "Delete knowledge base" })
    )
    fireEvent.click(
      within(screen.getByText(/Delete “Handbook”\?/).parentElement!).getByRole(
        "button",
        { name: "Delete knowledge base" }
      )
    )
    await waitFor(() => expect(onSelect).toHaveBeenCalledWith(null))
    expect(await screen.findByText("No knowledge bases yet")).toBeTruthy()
  })

  it("offers no changes a member can't make", async () => {
    stubApi(
      detailRoutes(
        corpus({ scope: "organization", member_id: undefined }),
        () => [document()]
      )
    )
    renderKnowledge(openHandbook)
    await screen.findByText("Decision log")

    expect(screen.queryByRole("button", { name: "Add content" })).toBeNull()
    expect(
      screen.queryByRole("button", { name: "Delete knowledge base" })
    ).toBeNull()
    expect(
      screen.queryByRole("button", { name: "Remove Decision log" })
    ).toBeNull()
    expect(screen.getByText(/^Whole workspace ·/)).toBeTruthy()
  })

  it("leaves connected-app content to Tools", async () => {
    stubApi(
      detailRoutes(corpus({ metadata: { origin: "knowledge" } }), () => [
        document({
          source: { kind: "external", ref: "knowledge:kdoc_1" },
          metadata: { provider: "gmail" },
        }),
      ])
    )
    const onOpenTools = vi.fn()
    renderKnowledge({ ...openHandbook, onOpenTools })
    await screen.findByText("Decision log")

    expect(screen.getByText(/^Gmail ·/)).toBeTruthy()
    expect(screen.queryByRole("button", { name: "Add content" })).toBeNull()
    expect(
      screen.queryByRole("button", { name: "Delete knowledge base" })
    ).toBeNull()
    expect(
      screen.queryByRole("button", { name: "Remove Decision log" })
    ).toBeNull()
    fireEvent.click(screen.getByRole("button", { name: "Manage in Tools" }))
    expect(onOpenTools).toHaveBeenCalled()
  })
})

describe("asking a knowledge base", () => {
  it("answers with citations to its documents", async () => {
    const api = stubApi({
      ...detailRoutes(corpus(), () => [document()]),
      [`POST ${corpusPath}/answer`]: {
        answer: {
          text: "Falcon launches on 14 October [2].",
          citations: [
            {
              index: 2,
              chunk_id: "chk_2",
              document_id: "doc_2",
              title: "Launch notes",
              source: { kind: "external", ref: "https://example.com/launch" },
              start_byte: 0,
              end_byte: 40,
              score: 0.8,
            },
          ],
          model: "gemini-3.5-flash-lite",
          usage: {},
          grounded: true,
        },
        matches: [],
      },
    })
    renderKnowledge(openHandbook)
    await screen.findByText("Decision log")

    const field = screen.getByRole("textbox", { name: "Ask Handbook" })
    fireEvent.change(field, { target: { value: " When is the launch? " } })
    fireEvent.click(screen.getByRole("button", { name: "Ask" }))

    expect(
      await screen.findByText("Falcon launches on 14 October [2].")
    ).toBeTruthy()
    expect(api.find("POST", `${corpusPath}/answer`)[0]?.body).toEqual({
      query: "When is the launch?",
    })
    const sources = screen.getByRole("list", { name: "Sources" })
    expect(within(sources).getByText("2")).toBeTruthy()
    expect(
      within(sources)
        .getByRole("link", { name: "Launch notes" })
        .getAttribute("href")
    ).toBe("https://example.com/launch")
  })

  it("shows a citation's provenance as muted chips, and nothing without metadata", async () => {
    stubApi({
      ...detailRoutes(corpus(), () => [document()]),
      [`POST ${corpusPath}/answer`]: {
        answer: {
          text: "Ship on the 14th [1], per the note [2].",
          citations: [
            {
              index: 1,
              chunk_id: "chk_1",
              document_id: "doc_9",
              title: "Launch thread",
              source: { kind: "external", ref: "https://mail.google.com/x" },
              metadata: {
                provider: "gmail",
                document_kind: "message",
                category: "work_material",
              },
              start_byte: 0,
              end_byte: 10,
              score: 0.9,
            },
            {
              index: 2,
              chunk_id: "chk_2",
              document_id: "doc_1",
              title: "Plain note",
              source: { kind: "inline" },
              start_byte: 0,
              end_byte: 10,
              score: 0.8,
            },
          ],
          model: "gemini-3.5-flash-lite",
          usage: {},
          grounded: true,
        },
        matches: [],
      },
    })
    renderKnowledge(openHandbook)
    await screen.findByText("Decision log")

    fireEvent.change(screen.getByRole("textbox", { name: "Ask Handbook" }), {
      target: { value: "When do we ship?" },
    })
    fireEvent.click(screen.getByRole("button", { name: "Ask" }))
    await screen.findByText("Ship on the 14th [1], per the note [2].")

    const sources = screen.getByRole("list", { name: "Sources" })
    const withMeta = within(sources).getByText("Launch thread").closest("li")!
    expect(within(withMeta).getByText("Gmail")).toBeTruthy()
    expect(within(withMeta).getByText("Message")).toBeTruthy()
    expect(within(withMeta).getByText("Work material")).toBeTruthy()

    const withoutMeta = within(sources).getByText("Plain note").closest("li")!
    expect(within(withoutMeta).queryByText("Gmail")).toBeNull()
    expect(within(withoutMeta).queryByText("Message")).toBeNull()
  })

  it("says plainly when nothing answers the question", async () => {
    stubApi({
      ...detailRoutes(corpus(), () => [
        document(),
        document({
          id: "doc_2",
          title: "Roadmap",
          status: "pending",
          attempts: 0,
        }),
      ]),
      [`POST ${corpusPath}/answer`]: {
        answer: {
          text: "I could not find anything in this corpus that answers that.",
          citations: [],
          model: "",
          usage: {},
          grounded: false,
        },
        matches: [],
      },
    })
    renderKnowledge(openHandbook)
    await screen.findByText("Roadmap")

    fireEvent.change(screen.getByRole("textbox", { name: "Ask Handbook" }), {
      target: { value: "Who won the match?" },
    })
    fireEvent.click(screen.getByRole("button", { name: "Ask" }))
    expect(
      await screen.findByText(
        "Nothing in Handbook answers that yet. Some documents are still being indexed."
      )
    ).toBeTruthy()
  })

  it("searches instead when the deployment has no answering model", async () => {
    const api = stubApi({
      ...detailRoutes(corpus(), () => [document()], { answering: false }),
      [`POST ${corpusPath}/search`]: {
        matches: [
          {
            chunk: {
              id: "chk_1",
              document_id: "doc_1",
              corpus_id: "cor_1",
              organization_id: "org_1",
              ordinal: 0,
              text: "We chose pgvector for local runs.",
              token_count: 8,
              heading_path: ["Storage"],
              start_byte: 0,
              end_byte: 33,
              sha256: "",
            },
            document: {
              id: "doc_1",
              title: "Decision log",
              source: { kind: "inline" },
            },
            score: 0.9,
          },
        ],
        usage: {},
        reranked: false,
      },
    })
    renderKnowledge(openHandbook)
    await screen.findByText("Decision log")

    fireEvent.change(screen.getByRole("textbox", { name: "Search Handbook" }), {
      target: { value: "vector store" },
    })
    fireEvent.click(screen.getByRole("button", { name: "Search" }))

    const passages = await screen.findByRole("list", {
      name: "Matching passages",
    })
    expect(
      within(passages).getByText("We chose pgvector for local runs.")
    ).toBeTruthy()
    expect(within(passages).getByText("Decision log · Storage")).toBeTruthy()
    expect(api.find("POST", `${corpusPath}/search`)[0]?.body).toEqual({
      query: "vector store",
      top_k: 5,
    })
    expect(api.find("POST", `${corpusPath}/answer`)).toHaveLength(0)
  })

  it("is not offered before there is anything to ask", async () => {
    stubApi(detailRoutes(corpus(), () => []))
    renderKnowledge(openHandbook)
    await screen.findByText("No documents yet")
    expect(screen.queryByRole("textbox", { name: "Ask Handbook" })).toBeNull()
  })
})
