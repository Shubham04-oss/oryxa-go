// @vitest-environment jsdom

import { afterEach, describe, expect, it, vi } from "vitest"
import {
  act,
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
import { KnowledgeBases, LibraryHeading } from "./knowledge-bases"
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

/** A response the test lets go when it chooses: return `pending` from a route, then `answer` it. */
function held() {
  let answer!: (response: Response) => void
  const pending = new Promise<Response>((resolve) => (answer = resolve))
  return { answer, pending }
}

/** Lets what a settled request set going (state updates, the next request) run to its end. */
const settle = () =>
  act(async () => {
    await new Promise((resolve) => setTimeout(resolve, 20))
  })

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

const members = {
  members: [
    {
      organization_id: "org_1",
      user_id: "user-1",
      email: "ada@example.com",
      display_name: "Ada Lovelace",
      role: "member",
      joined_at: ago(30 * day),
    },
  ],
}

const corpusPath = "/v1/organizations/org_1/corpora/cor_1"

/** Routes for one open collection; `documents` may change between reads. */
function detailRoutes(
  base: Corpus,
  documents: () => CorpusDocument[],
  { answering = true }: { answering?: boolean } = {}
) {
  return {
    "GET /v1/capabilities": capabilities(answering),
    "GET /v1/organizations/org_1/members": members,
    "GET /v1/organizations/org_1/corpora": () =>
      json({ corpora: [{ ...base, document_count: documents().length }] }),
    [`GET ${corpusPath}`]: () =>
      json({ ...base, document_count: documents().length }),
    [`GET ${corpusPath}/documents`]: () => json({ documents: documents() }),
  }
}

/** Routes for the Library with no collections at all. */
const emptyRoutes = {
  "GET /v1/capabilities": capabilities(true),
  "GET /v1/organizations/org_1/members": members,
  "GET /v1/organizations/org_1/corpora": { corpora: [] },
}

function renderKnowledge(
  props: Partial<ComponentProps<typeof KnowledgeBases>> = {}
) {
  const onSelect = vi.fn()
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })
  let selectBase!: (base: SelectedKnowledgeBase | null) => void
  function Harness() {
    const [selected, setSelected] = useState<SelectedKnowledgeBase | null>(
      props.selected ?? null
    )
    selectBase = setSelected
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
  return {
    onSelect,
    select: (base: SelectedKnowledgeBase | null) => act(() => selectBase(base)),
  }
}

const openHandbook = { selected: { id: "cor_1", name: "Handbook" } }

/** The rail's entry for a collection, or for All files. */
const railItem = (name: string) =>
  within(screen.getByRole("navigation", { name: "Collections" })).getByRole(
    "button",
    { name: new RegExp(`^${name} \\d`) }
  )
const findRailItem = async (name: string) => {
  await screen.findByRole("navigation", { name: "Collections" })
  return waitFor(() => railItem(name))
}

/** Opens the New collection dialog from the rail's "+". */
async function openNewCollection() {
  const rail = await screen.findByRole("navigation", { name: "Collections" })
  fireEvent.click(within(rail).getByRole("button", { name: "New collection" }))
  return screen.findByRole("dialog")
}

/** Opens a More menu and picks one of its items. */
async function chooseMenuItem(menu: string, item: string) {
  fireEvent.click(screen.getByRole("button", { name: menu }))
  fireEvent.click(await screen.findByRole("menuitem", { name: item }))
}

/** The rows of a file table, without its header. */
function fileRows(name: string) {
  const table = screen.getByRole("table", { name })
  return within(table).getAllByRole("row").slice(1)
}

afterEach(() => {
  cleanup()
  vi.unstubAllGlobals()
})

describe("the Library", () => {
  it("lists the member's real collections in the rail, never examples", async () => {
    stubApi({
      ...emptyRoutes,
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
          }),
        ],
      },
      [`GET ${corpusPath}/documents`]: { documents: [] },
      "GET /v1/organizations/org_1/corpora/cor_2/documents": { documents: [] },
    })
    renderKnowledge()

    expect((await findRailItem("All files")).textContent).toContain("4")
    expect(railItem("All files").getAttribute("aria-current")).toBe("page")
    expect(railItem("Handbook").textContent).toContain("3")
    expect(railItem("Company wiki").textContent).toContain("1")
    expect(screen.queryByText("Product docs")).toBeNull()
  })

  it("shows every collection's files in one table, newest first, with who added them", async () => {
    stubApi({
      ...emptyRoutes,
      "GET /v1/organizations/org_1/corpora": {
        corpora: [
          corpus({ document_count: 1 }),
          corpus({ id: "cor_2", name: "Company wiki", document_count: 1 }),
        ],
      },
      [`GET ${corpusPath}/documents`]: {
        documents: [document({ updated_at: ago(3 * 3_600_000) })],
      },
      "GET /v1/organizations/org_1/corpora/cor_2/documents": {
        documents: [
          document({
            id: "doc_9",
            corpus_id: "cor_2",
            title: "pricing.md",
            content_type: "text/markdown",
            created_by: "user-gone",
            updated_at: ago(60_000),
          }),
        ],
      },
    })
    renderKnowledge()

    await screen.findByText("pricing.md")
    const rows = fileRows("All files")
    expect(rows).toHaveLength(2)
    expect(within(rows[0]).getByText("pricing.md")).toBeTruthy()
    expect(within(rows[0]).getByText(/Company wiki ·/)).toBeTruthy()
    expect(within(rows[0]).getByText("MD")).toBeTruthy()
    // An owner no longer in the workspace is not guessed at.
    expect(within(rows[0]).getByText("—")).toBeTruthy()
    expect(within(rows[1]).getByText("Decision log")).toBeTruthy()
    expect(within(rows[1]).getByText(/Handbook · Note · 2 KB/)).toBeTruthy()
    expect(within(rows[1]).getByText("TXT")).toBeTruthy()
    await waitFor(() =>
      expect(within(rows[1]).getByText("Ada Lovelace")).toBeTruthy()
    )
    expect(within(rows[1]).getByText("3 hours ago")).toBeTruthy()
    expect(
      screen.queryByText(/Showing the latest from each collection/)
    ).toBeNull()
  })

  it("says when All files shows only the latest of a collection", async () => {
    stubApi({
      ...emptyRoutes,
      "GET /v1/organizations/org_1/corpora": {
        corpora: [corpus({ document_count: 120 })],
      },
      [`GET ${corpusPath}/documents`]: {
        documents: [document()],
        next_cursor: "page-2",
      },
    })
    renderKnowledge()

    expect(
      await screen.findByText(/Showing the latest from each collection/)
    ).toBeTruthy()
  })

  it("filters the loaded files by title", async () => {
    stubApi({
      ...emptyRoutes,
      "GET /v1/organizations/org_1/corpora": {
        corpora: [corpus({ document_count: 2 })],
      },
      [`GET ${corpusPath}/documents`]: {
        documents: [
          document(),
          document({ id: "doc_2", title: "Interview notes" }),
        ],
      },
    })
    renderKnowledge()
    await screen.findByText("Interview notes")

    const search = screen.getByRole("searchbox", { name: "Search files" })
    fireEvent.change(search, { target: { value: "interview" } })
    expect(fileRows("All files")).toHaveLength(1)
    expect(screen.queryByText("Decision log")).toBeNull()

    fireEvent.change(search, { target: { value: "nothing like this" } })
    expect(screen.getByText("No files match “nothing like this”.")).toBeTruthy()
  })

  it("opens a collection from the rail", async () => {
    stubApi(detailRoutes(corpus(), () => [document()]))
    const { onSelect } = renderKnowledge()

    fireEvent.click(await findRailItem("Handbook"))
    expect(onSelect).toHaveBeenCalledWith({ id: "cor_1", name: "Handbook" })
    expect(
      await screen.findByRole("heading", { name: "Handbook" })
    ).toBeTruthy()
    expect(railItem("Handbook").getAttribute("aria-current")).toBe("page")
    await screen.findByText("Decision log")
    expect(fileRows("Files in Handbook")).toHaveLength(1)
  })

  it("says so when there are none yet", async () => {
    stubApi(emptyRoutes)
    renderKnowledge()
    expect(await screen.findByText("Your Library is empty")).toBeTruthy()
    expect(screen.queryByRole("button", { name: "Add files" })).toBeNull()
  })

  it("shows a failed read with a retry", async () => {
    let reads = 0
    stubApi({
      ...emptyRoutes,
      "GET /v1/organizations/org_1/corpora": () =>
        ++reads === 1
          ? json({ error: "something went wrong" }, 500)
          : json({ corpora: [corpus()] }),
      [`GET ${corpusPath}/documents`]: { documents: [] },
    })
    renderKnowledge()

    const alert = await screen.findByRole("alert")
    expect(alert.textContent).toContain("Couldn’t load your Library.")
    fireEvent.click(within(alert).getByRole("button", { name: "Try again" }))
    expect(await findRailItem("Handbook")).toBeTruthy()
  })

  it("says which collection's files could not be read", async () => {
    stubApi({
      ...emptyRoutes,
      "GET /v1/organizations/org_1/corpora": {
        corpora: [corpus({ document_count: 1 })],
      },
      [`GET ${corpusPath}/documents`]: () => json({ error: "down" }, 500),
    })
    renderKnowledge()

    expect((await screen.findByRole("alert")).textContent).toContain(
      "Couldn’t load the files in Handbook."
    )
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
    expect(screen.queryByRole("button", { name: "New collection" })).toBeNull()
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

describe("the Library's heading", () => {
  function renderHeading() {
    const client = new QueryClient({
      defaultOptions: { queries: { retry: false } },
    })
    render(
      <QueryClientProvider client={client}>
        <LibraryHeading organizationId="org_1" />
      </QueryClientProvider>
    )
  }

  it("counts the files and the sources they come from", async () => {
    stubApi({
      "GET /v1/organizations/org_1/corpora": {
        corpora: [
          corpus({ document_count: 3 }),
          corpus({ id: "cor_2", document_count: 125 }),
        ],
      },
      "GET /v1/organizations/org_1/sources": {
        sources: [{ id: "src_1" }, { id: "src_2" }],
      },
    })
    renderHeading()
    expect(screen.getByRole("heading", { name: "Library" })).toBeTruthy()
    expect(await screen.findByText("128 files from 2 sources")).toBeTruthy()
  })

  it("counts only the files when sources can't be read", async () => {
    stubApi({
      "GET /v1/organizations/org_1/corpora": {
        corpora: [corpus({ document_count: 1 })],
      },
      "GET /v1/organizations/org_1/sources": () => json({}, 404),
    })
    renderHeading()
    expect(await screen.findByText("1 file")).toBeTruthy()
  })
})

describe("creating a collection", () => {
  it("creates a real member corpus and goes straight to adding a note", async () => {
    const created = corpus({
      id: "cor_9",
      name: "Engineering handbook",
      slug: "engineering-handbook",
      metadata: { description: "Technical decisions and guides." },
    })
    const documents: CorpusDocument[] = []
    const list: Corpus[] = []
    const api = stubApi({
      ...emptyRoutes,
      "GET /v1/organizations/org_1/corpora": () => json({ corpora: list }),
      "POST /v1/organizations/org_1/corpora": () => {
        list.push(created)
        return json(created, 201)
      },
      "GET /v1/organizations/org_1/corpora/cor_9": created,
      "GET /v1/organizations/org_1/corpora/cor_9/documents": () =>
        json({ documents }),
      "POST /v1/organizations/org_1/corpora/cor_9/documents": () => {
        const added = document({ corpus_id: "cor_9", status: "pending" })
        documents.push(added)
        return json(added, 202)
      },
    })
    const { onSelect } = renderKnowledge()
    await screen.findByText("Your Library is empty")

    const dialog = await openNewCollection()
    expect(
      within(dialog).queryByRole("radiogroup", { name: "Who can use it" })
    ).toBeNull()
    expect(
      within(dialog).getByText(
        "Only you can see it, along with your workspace’s admins."
      )
    ).toBeTruthy()
    fireEvent.click(
      within(dialog).getByRole("button", { name: "Create collection" })
    )
    expect(within(dialog).getByText("Use at least 2 characters.")).toBeTruthy()
    expect(api.find("POST", "/v1/organizations/org_1/corpora")).toHaveLength(0)

    fireEvent.change(
      within(dialog).getByRole("textbox", { name: "Short name" }),
      {
        target: { value: "Engineering handbook" },
      }
    )
    fireEvent.change(
      within(dialog).getByRole("textbox", { name: "Description" }),
      { target: { value: "Technical decisions and guides." } }
    )
    fireEvent.click(
      within(dialog).getByRole("button", { name: "Create collection" })
    )

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
    const adding = await screen.findByRole("dialog", {
      name: "Add to Engineering handbook",
    })
    expect(
      within(adding).getByRole<HTMLInputElement>("radio", { name: "Note" })
        .checked
    ).toBe(true)
    expect(await screen.findByText("No files yet")).toBeTruthy()
    expect(screen.getByText("Technical decisions and guides.")).toBeTruthy()
    fireEvent.change(within(adding).getByRole("textbox", { name: "Text" }), {
      target: { value: "We chose pgvector for local runs." },
    })
    fireEvent.click(within(adding).getByRole("button", { name: "Add note" }))
    await waitFor(() =>
      expect(
        api.find("POST", "/v1/organizations/org_1/corpora/cor_9/documents")[0]
          ?.body
      ).toEqual({
        source: { kind: "inline" },
        text: "We chose pgvector for local runs.",
      })
    )
    expect(await screen.findByText("Decision log")).toBeTruthy()
    await waitFor(() =>
      expect(screen.queryByRole("textbox", { name: "Text" })).toBeNull()
    )
  })

  it("explains a short name and its existing length limit accessibly", async () => {
    stubApi(emptyRoutes)
    renderKnowledge()
    await screen.findByText("Your Library is empty")
    const dialog = await openNewCollection()
    const name = within(dialog).getByRole("textbox", { name: "Short name" })
    expect(name.getAttribute("placeholder")).toBe("e.g. Engineering handbook")
    expect(name.getAttribute("maxlength")).toBe("60")
    const hint = within(dialog).getByText(
      "Short label, 2–60 characters. Add facts and notes next."
    )
    expect(name.getAttribute("aria-describedby")).toBe(hint.id)
    fireEvent.click(
      within(dialog).getByRole("button", { name: "Create collection" })
    )
    expect(name.getAttribute("aria-describedby")).toContain(hint.id)
    expect(name.getAttribute("aria-describedby")).toContain(
      "knowledge-base-name-error"
    )
  })

  it.each(["AB", "A".repeat(60)])(
    "accepts a name within the existing constraints: %s",
    async (name) => {
      const made = corpus({ name })
      const api = stubApi({
        ...detailRoutes(made, () => []),
        "POST /v1/organizations/org_1/corpora": () => json(made, 201),
      })
      renderKnowledge()
      await findRailItem(name)
      const dialog = await openNewCollection()
      fireEvent.change(
        within(dialog).getByRole("textbox", { name: "Short name" }),
        { target: { value: ` ${name} ` } }
      )
      fireEvent.click(
        within(dialog).getByRole("button", { name: "Create collection" })
      )
      await screen.findByRole("button", { name: "Add note" })
      expect(
        api.find("POST", "/v1/organizations/org_1/corpora")[0]?.body
      ).toMatchObject({ name })
    }
  )

  it.each(["A", "A".repeat(61)])(
    "refuses a name outside the existing constraints before creating: %s",
    async (name) => {
      const api = stubApi(emptyRoutes)
      renderKnowledge()
      await screen.findByText("Your Library is empty")
      const dialog = await openNewCollection()
      fireEvent.change(
        within(dialog).getByRole("textbox", { name: "Short name" }),
        { target: { value: name } }
      )
      fireEvent.click(
        within(dialog).getByRole("button", { name: "Create collection" })
      )
      expect(
        within(dialog).getByText(
          name.length < 2
            ? "Use at least 2 characters."
            : "Use at most 60 characters."
        )
      ).toBeTruthy()
      expect(api.find("POST", "/v1/organizations/org_1/corpora")).toHaveLength(
        0
      )
    }
  )

  it("keeps an existing empty collection closed and does not repeat the create handoff", async () => {
    const made = corpus()
    stubApi({
      ...detailRoutes(made, () => []),
      "POST /v1/organizations/org_1/corpora": () => json(made, 201),
    })
    const view = renderKnowledge()
    fireEvent.click(await findRailItem("Handbook"))
    await screen.findByText("No files yet")
    expect(screen.queryByRole("textbox", { name: "Text" })).toBeNull()
    view.select(null)
    const dialog = await openNewCollection()
    fireEvent.change(
      within(dialog).getByRole("textbox", { name: "Short name" }),
      {
        target: { value: "Handbook" },
      }
    )
    fireEvent.click(
      within(dialog).getByRole("button", { name: "Create collection" })
    )
    const adding = await screen.findByRole("dialog", {
      name: "Add to Handbook",
    })
    fireEvent.click(within(adding).getByRole("button", { name: "Cancel" }))
    await waitFor(() =>
      expect(screen.queryByRole("textbox", { name: "Text" })).toBeNull()
    )
    view.select(null)
    view.select({ id: made.id, name: made.name })
    await screen.findByText("No files yet")
    expect(screen.queryByRole("textbox", { name: "Text" })).toBeNull()
    view.select(null)
    fireEvent.click(await findRailItem("Handbook"))
    await screen.findByText("No files yet")
    expect(screen.queryByRole("textbox", { name: "Text" })).toBeNull()
  })

  it("lets an admin share one with the whole workspace", async () => {
    const created = corpus({ id: "cor_9", scope: "organization" })
    let posted = false
    const api = stubApi({
      ...emptyRoutes,
      "GET /v1/organizations/org_1/corpora": () =>
        json({ corpora: posted ? [created] : [] }),
      "POST /v1/organizations/org_1/corpora": () => {
        posted = true
        return json(created, 201)
      },
      "GET /v1/organizations/org_1/corpora/cor_9": created,
      "GET /v1/organizations/org_1/corpora/cor_9/documents": {
        documents: [],
      },
    })
    renderKnowledge({ organizationRole: "admin", userId: "user-3" })
    await screen.findByText("Your Library is empty")

    const dialog = await openNewCollection()
    fireEvent.change(
      within(dialog).getByRole("textbox", { name: "Short name" }),
      {
        target: { value: "Company wiki" },
      }
    )
    fireEvent.click(
      within(dialog).getByRole("radio", { name: "Everyone in Acme" })
    )
    expect(
      within(dialog).getByText(
        "Everyone in the workspace can search it. Only admins can change what’s in it."
      )
    ).toBeTruthy()
    fireEvent.click(
      within(dialog).getByRole("button", { name: "Create collection" })
    )

    await waitFor(() =>
      expect(
        api.find("POST", "/v1/organizations/org_1/corpora")[0]?.body
      ).toEqual({
        name: "Company wiki",
        slug: "company-wiki",
        scope: "organization",
      })
    )
    expect(await screen.findByRole("button", { name: "Add note" })).toBeTruthy()
    expect(screen.getByRole("radio", { name: "Admins only" })).toBeTruthy()
  })

  it("explains a refusal", async () => {
    stubApi({
      ...emptyRoutes,
      "POST /v1/organizations/org_1/corpora": () =>
        json({ error: "you may not do that here" }, 403),
    })
    renderKnowledge()
    await screen.findByText("Your Library is empty")

    const dialog = await openNewCollection()
    fireEvent.change(
      within(dialog).getByRole("textbox", { name: "Short name" }),
      {
        target: { value: "Wiki" },
      }
    )
    fireEvent.click(
      within(dialog).getByRole("button", { name: "Create collection" })
    )
    expect((await within(dialog).findByRole("alert")).textContent).toBe(
      "You don't have permission to do that here."
    )
  })

  describe("when Cancel is pressed after the request went out", () => {
    const newCollectionDialog = () =>
      screen.queryByRole("dialog", { name: "New collection" })

    /** Sends "First try" and cancels, its request still out. */
    async function sendThenCancel(
      request: ReturnType<typeof held>,
      list: Corpus[]
    ) {
      const api = stubApi({
        ...emptyRoutes,
        "GET /v1/organizations/org_1/corpora": () => json({ corpora: list }),
        "POST /v1/organizations/org_1/corpora": () => request.pending,
        "GET /v1/organizations/org_1/corpora/cor_9/documents": {
          documents: [],
        },
      })
      const view = renderKnowledge()
      await screen.findByText("Your Library is empty")

      const dialog = await openNewCollection()
      fireEvent.change(
        within(dialog).getByRole("textbox", { name: "Short name" }),
        { target: { value: "First try" } }
      )
      fireEvent.click(
        within(dialog).getByRole("button", { name: "Create collection" })
      )
      await waitFor(() =>
        expect(
          api.find("POST", "/v1/organizations/org_1/corpora")
        ).toHaveLength(1)
      )
      expect(
        within(dialog).getByRole("button", { name: "Creating…" })
      ).toBeTruthy()
      fireEvent.click(within(dialog).getByRole("button", { name: "Cancel" }))
      await waitFor(() => expect(newCollectionDialog()).toBeNull())
      return view
    }

    it("does not open the collection it made anyway, but the rail learns of it", async () => {
      const made = corpus({ id: "cor_9", name: "First try", slug: "first-try" })
      const list: Corpus[] = []
      const request = held()
      const { onSelect } = await sendThenCancel(request, list)

      // A request that is out cannot be recalled: the collection exists by now.
      list.push(made)
      request.answer(json(made, 201))

      expect(await findRailItem("First try")).toBeTruthy()
      expect(onSelect).not.toHaveBeenCalled()
      expect(newCollectionDialog()).toBeNull()
    })

    it("leaves a dialog opened since exactly as the member left it", async () => {
      const made = corpus({ id: "cor_9", name: "First try", slug: "first-try" })
      const list: Corpus[] = []
      const request = held()
      const { onSelect } = await sendThenCancel(request, list)

      const dialog = await openNewCollection()
      fireEvent.change(
        within(dialog).getByRole("textbox", { name: "Short name" }),
        { target: { value: "Second try" } }
      )
      list.push(made)
      request.answer(json(made, 201))
      // The open dialog hides the page from roles, so look for the words.
      await screen.findByText("First try")

      const name = within(dialog).getByRole("textbox", { name: "Short name" })
      expect((name as HTMLInputElement).value).toBe("Second try")
      expect(newCollectionDialog()).toBeTruthy()
      expect(onSelect).not.toHaveBeenCalled()
    })

    it("does not show its failure in a dialog opened since", async () => {
      const request = held()
      await sendThenCancel(request, [])

      const dialog = await openNewCollection()
      request.answer(json({ error: "you may not do that here" }, 403))
      await settle()

      expect(within(dialog).queryByRole("alert")).toBeNull()
    })
  })

  it("closes the dialog on Cancel when nothing was sent", async () => {
    const api = stubApi(emptyRoutes)
    renderKnowledge()
    await screen.findByText("Your Library is empty")

    const dialog = await openNewCollection()
    fireEvent.change(
      within(dialog).getByRole("textbox", { name: "Short name" }),
      {
        target: { value: "Never sent" },
      }
    )
    fireEvent.click(within(dialog).getByRole("button", { name: "Cancel" }))

    await waitFor(() =>
      expect(
        screen.queryByRole("dialog", { name: "New collection" })
      ).toBeNull()
    )
    expect(api.find("POST", "/v1/organizations/org_1/corpora")).toHaveLength(0)
  })
})

describe("a collection's files", () => {
  it("shows every file's real status and where it came from", async () => {
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

    await screen.findByText("Q3 plan")
    const rows = fileRows("Files in Handbook")
    expect(rows).toHaveLength(5)

    expect(within(rows[0]).queryByText("Indexing…")).toBeNull()
    expect(within(rows[0]).getByText("Note · 2 KB")).toBeTruthy()
    expect(within(rows[0]).getByText("5 minutes ago")).toBeTruthy()

    expect(
      within(rows[1]).getByRole("button", { name: "Open example.com" })
    ).toBeTruthy()
    expect(within(rows[1]).getByText("Indexing…")).toBeTruthy()

    expect(within(rows[2]).getByText("Indexing…")).toBeTruthy()
    expect(within(rows[2]).getByText(/^File · Markdown/)).toBeTruthy()

    expect(within(rows[3]).getByText("Failed")).toBeTruthy()
    expect(within(rows[3]).getByText("embed: quota exceeded")).toBeTruthy()

    expect(within(rows[4]).getByText(/^Google Drive ·/)).toBeTruthy()
    expect(
      within(rows[4]).getByText(
        "2 attempts didn’t finish. It will be tried again."
      )
    ).toBeTruthy()
    expect(
      screen.getByText(/^Just you · 5 files · 3 indexing · Created 2 days ago$/)
    ).toBeTruthy()
  })

  it("shows a quiet Admins only tag on a restricted file", async () => {
    stubApi(
      detailRoutes(corpus(), () => [
        document({ access: "admins" }),
        document({ id: "doc_2", title: "Roadmap", access: "members" }),
      ])
    )
    renderKnowledge(openHandbook)

    const restricted = (await screen.findByText("Decision log")).closest("tr")!
    expect(within(restricted).getByText("Admins only")).toBeTruthy()
    const open = screen.getByText("Roadmap").closest("tr")!
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

  it("says when the collection is gone", async () => {
    stubApi({
      ...emptyRoutes,
      [`GET ${corpusPath}`]: () => json({ error: "not found" }, 404),
      [`GET ${corpusPath}/documents`]: () => json({ error: "not found" }, 404),
    })
    const { onSelect } = renderKnowledge(openHandbook)

    expect(
      await screen.findByText("This collection is no longer here")
    ).toBeTruthy()
    fireEvent.click(screen.getByRole("button", { name: "All files" }))
    expect(onSelect).toHaveBeenCalledWith(null)
  })
})

describe("Add files", () => {
  it("asks which collection first in All files, among those the member may add to", async () => {
    stubApi({
      ...emptyRoutes,
      "GET /v1/organizations/org_1/corpora": {
        corpora: [
          corpus(),
          corpus({ id: "cor_2", name: "Notes", member_id: "user-1" }),
          corpus({
            id: "cor_3",
            name: "Company wiki",
            scope: "organization",
            member_id: undefined,
          }),
        ],
      },
      [`GET ${corpusPath}/documents`]: { documents: [] },
      "GET /v1/organizations/org_1/corpora/cor_2/documents": { documents: [] },
      "GET /v1/organizations/org_1/corpora/cor_3/documents": { documents: [] },
    })
    renderKnowledge()
    await findRailItem("Handbook")

    fireEvent.click(screen.getByRole("button", { name: "Add files" }))
    const dialog = await screen.findByRole("dialog", { name: "Add files" })
    const choices = within(dialog).getByRole("list", { name: "Collections" })
    expect(within(choices).getAllByRole("button")).toHaveLength(2)
    expect(within(choices).queryByText("Company wiki")).toBeNull()
    fireEvent.click(within(choices).getByRole("button", { name: /^Notes/ }))
    expect(
      await screen.findByRole("dialog", { name: "Add to Notes" })
    ).toBeTruthy()
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
    await screen.findByText("No files yet")

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
    await screen.findByText("No files yet")

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
    await screen.findByText("No files yet")

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
    const row = (await screen.findByText("Decision log")).closest("tr")!
    expect(within(row).getByText("Indexing…")).toBeTruthy()
    expect(screen.queryByRole("button", { name: "Add note" })).toBeNull()
  })

  it("adds a link with the text pasted from it", async () => {
    const api = stubApi({
      ...addingRoutes([]),
      [`POST ${corpusPath}/documents`]: () =>
        json(document({ id: "doc_new", status: "pending" }), 202),
    })
    renderKnowledge(openHandbook)
    await screen.findByText("No files yet")

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
    await screen.findByText("No files yet")

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
    await screen.findByText("No files yet")

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
    expect(screen.getByText(/The Library takes text/)).toBeTruthy()
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
    await screen.findByText("No files yet")

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
    await screen.findByText("No files yet")

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
    await screen.findByText("No files yet")

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
    await screen.findByText("No files yet")

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

  describe("when Cancel is pressed after the request went out", () => {
    /** Sends a note and cancels, its request still out; `documents` is what the list will read. */
    async function sendNoteThenCancel(
      request: ReturnType<typeof held>,
      documents: CorpusDocument[]
    ) {
      const api = stubApi({
        ...addingRoutes(documents),
        [`POST ${corpusPath}/documents`]: () => request.pending,
      })
      renderKnowledge(openHandbook)
      await screen.findByText(
        documents.length ? "Decision log" : "No files yet"
      )

      fireEvent.click(screen.getAllByRole("button", { name: "Add content" })[0])
      fireEvent.change(screen.getByRole("textbox", { name: "Text" }), {
        target: { value: "We chose pgvector." },
      })
      fireEvent.click(screen.getByRole("button", { name: "Add note" }))
      await waitFor(() =>
        expect(api.find("POST", `${corpusPath}/documents`)).toHaveLength(1)
      )
      expect(screen.getByRole("button", { name: "Adding…" })).toBeTruthy()
      fireEvent.click(screen.getByRole("button", { name: "Cancel" }))
      expect(screen.queryByRole("textbox", { name: "Text" })).toBeNull()
      return api
    }

    const landed = () =>
      document({
        id: "doc_new",
        title: "Decision log",
        status: "pending",
        attempts: 0,
        created_at: ago(0),
        updated_at: ago(0),
      })

    it("leaves a form opened since exactly as the member left it, and still lists the document", async () => {
      const documents: CorpusDocument[] = []
      const request = held()
      await sendNoteThenCancel(request, documents)

      fireEvent.click(screen.getAllByRole("button", { name: "Add content" })[0])
      fireEvent.change(screen.getByRole("textbox", { name: "Text" }), {
        target: { value: "Something else." },
      })

      // A request that is out cannot be recalled: the document exists by now.
      documents.unshift(landed())
      request.answer(json(landed(), 202))
      expect(await screen.findByText("Decision log")).toBeTruthy()

      const text = screen.getByRole("textbox", { name: "Text" })
      expect((text as HTMLTextAreaElement).value).toBe("Something else.")
      expect(screen.getByRole("button", { name: "Add note" })).toBeTruthy()
    })

    it("does not tell the member the document was already there", async () => {
      const existing = document({ id: "doc_new", title: "Decision log" })
      const documents = [existing]
      const request = held()
      await sendNoteThenCancel(request, documents)

      request.answer(json(existing, 202))
      await settle()

      expect(screen.getByText("Decision log")).toBeTruthy()
      expect(screen.queryByText(/already/)).toBeNull()
    })
  })

  it("tells a member what they added was already there, and closes the form", async () => {
    const existing = document({ id: "doc_1", title: "Decision log" })
    const api = stubApi({
      ...addingRoutes([existing]),
      [`POST ${corpusPath}/documents`]: () => json(existing, 202),
    })
    renderKnowledge(openHandbook)
    await screen.findByText("Decision log")

    fireEvent.click(screen.getAllByRole("button", { name: "Add content" })[0])
    fireEvent.change(screen.getByRole("textbox", { name: "Text" }), {
      target: { value: "We chose pgvector for local runs." },
    })
    const reads = api.find("GET", `${corpusPath}/documents`).length
    fireEvent.click(screen.getByRole("button", { name: "Add note" }))

    expect(
      await screen.findByText("That note is already in this collection.")
    ).toBeTruthy()
    expect(screen.queryByRole("button", { name: "Add note" })).toBeNull()
    // The lists are read again, as for any document added.
    await waitFor(() =>
      expect(api.find("GET", `${corpusPath}/documents`).length).toBeGreaterThan(
        reads
      )
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

    await chooseMenuItem("More for Decision log", "Remove")
    const confirm = await screen.findByRole("alertdialog")
    expect(within(confirm).getByText("Remove “Decision log”?")).toBeTruthy()
    fireEvent.click(within(confirm).getByRole("button", { name: "Cancel" }))
    await waitFor(() => expect(screen.queryByRole("alertdialog")).toBeNull())
    expect(api.find("DELETE", `${corpusPath}/documents/doc_1`)).toHaveLength(0)

    await chooseMenuItem("More for Decision log", "Remove")
    fireEvent.click(
      within(await screen.findByRole("alertdialog")).getByRole("button", {
        name: "Remove",
      })
    )
    await waitFor(() => expect(screen.queryByText("Decision log")).toBeNull())
    expect(api.find("DELETE", `${corpusPath}/documents/doc_1`)).toHaveLength(1)
    expect(screen.getByText("Roadmap")).toBeTruthy()
  })

  it("deletes the collection only after confirming", async () => {
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

    await chooseMenuItem("More for Handbook", "Delete collection")
    const confirm = await screen.findByRole("alertdialog")
    expect(
      within(confirm).getByText(
        "Its 1 file and everything indexed from them go with it. This can’t be undone."
      )
    ).toBeTruthy()
    fireEvent.click(within(confirm).getByRole("button", { name: "Cancel" }))
    await waitFor(() => expect(screen.queryByRole("alertdialog")).toBeNull())
    expect(api.find("DELETE", corpusPath)).toHaveLength(0)

    await chooseMenuItem("More for Handbook", "Delete collection")
    fireEvent.click(
      within(await screen.findByRole("alertdialog")).getByRole("button", {
        name: "Delete collection",
      })
    )
    await waitFor(() => expect(onSelect).toHaveBeenCalledWith(null))
    expect(await screen.findByText("Your Library is empty")).toBeTruthy()
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
    expect(screen.queryByRole("button", { name: "Add files" })).toBeNull()
    expect(
      screen.queryByRole("button", { name: "More for Handbook" })
    ).toBeNull()
    expect(
      screen.queryByRole("button", { name: "More for Decision log" })
    ).toBeNull()
    expect(screen.getByText(/^Whole workspace · 1 file/)).toBeTruthy()
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
    expect(screen.queryByRole("button", { name: "Add files" })).toBeNull()
    expect(
      screen.queryByRole("button", { name: "More for Handbook" })
    ).toBeNull()
    expect(
      screen.queryByRole("button", { name: "More for Decision log" })
    ).toBeNull()
    fireEvent.click(screen.getByRole("button", { name: "Manage in Tools" }))
    expect(onOpenTools).toHaveBeenCalled()
  })
})

describe("asking a collection", () => {
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
    await screen.findByText("No files yet")
    expect(screen.queryByRole("textbox", { name: "Ask Handbook" })).toBeNull()
  })
})

describe("the Library's sources", () => {
  const appsCorpus = corpus({
    id: "cor_apps",
    name: "Knowledge from connected apps",
    metadata: { origin: "knowledge" },
    document_count: 3,
  })
  const appsPath = "/v1/organizations/org_1/corpora/cor_apps/documents"
  const source = (overrides: Record<string, unknown> = {}) => ({
    id: "src_1",
    provider: "github",
    target: { repository: "acme/api" },
    scope: "member",
    created_by: "user-1",
    status: "active",
    interval_seconds: 3600,
    created_at: ago(day),
    updated_at: ago(day),
    ...overrides,
  })
  const appDocuments = [
    document({
      id: "doc_gh",
      corpus_id: "cor_apps",
      title: "PR 312: Runs pane",
      source: { kind: "external", ref: "knowledge:kdoc_1" },
      metadata: { provider: "github" },
    }),
    document({
      id: "doc_notion",
      corpus_id: "cor_apps",
      title: "EU launch plan",
      source: { kind: "external", ref: "knowledge:kdoc_2" },
      metadata: { provider: "notion" },
    }),
  ]

  function sourceRoutes(sources: unknown[]) {
    return {
      ...emptyRoutes,
      "GET /v1/organizations/org_1/corpora": {
        corpora: [corpus(), appsCorpus],
      },
      [`GET ${corpusPath}/documents`]: { documents: [] },
      [`GET ${appsPath}`]: { documents: appDocuments },
      "GET /v1/organizations/org_1/sources": { sources },
    }
  }

  it("lists each connected app once, with a quiet word when it isn't running", async () => {
    stubApi(
      sourceRoutes([
        source(),
        source({ id: "src_2", target: { repository: "acme/web" } }),
        source({
          id: "src_3",
          provider: "notion",
          status: "paused",
          reason: "needs_reconnect",
        }),
      ])
    )
    renderKnowledge()

    const list = await screen.findByRole("list", { name: "Sources" })
    const items = within(list).getAllByRole("button")
    expect(items.map((item) => item.textContent)).toEqual([
      "GitHub",
      "Notion Reconnect",
    ])
    // Per-source counts have no route, so none is shown or guessed.
    expect(within(list).queryByText(/\d/)).toBeNull()
  })

  it("filters the table to one app's files", async () => {
    const onOpenTools = vi.fn()
    stubApi(
      sourceRoutes([source(), source({ id: "src_3", provider: "notion" })])
    )
    renderKnowledge({ onOpenTools })

    const list = await screen.findByRole("list", { name: "Sources" })
    fireEvent.click(within(list).getByRole("button", { name: "GitHub" }))
    expect(await screen.findByRole("heading", { name: "GitHub" })).toBeTruthy()
    await screen.findByText("PR 312: Runs pane")
    expect(fileRows("Files from GitHub")).toHaveLength(1)
    expect(screen.queryByText("EU launch plan")).toBeNull()
    expect(
      within(list)
        .getByRole("button", { name: "GitHub" })
        .getAttribute("aria-current")
    ).toBe("page")
    expect(railItem("All files").getAttribute("aria-current")).toBeNull()

    fireEvent.click(screen.getByRole("button", { name: "Manage in Tools" }))
    expect(onOpenTools).toHaveBeenCalled()

    fireEvent.click(railItem("All files"))
    expect(screen.queryByRole("heading", { name: "GitHub" })).toBeNull()
  })

  it("says plainly when nothing from an app is indexed", async () => {
    stubApi({
      ...sourceRoutes([source({ provider: "gmail" })]),
      "GET /v1/organizations/org_1/corpora": { corpora: [corpus()] },
    })
    renderKnowledge()

    const list = await screen.findByRole("list", { name: "Sources" })
    fireEvent.click(within(list).getByRole("button", { name: "Gmail" }))
    expect(
      await screen.findByText("Nothing from Gmail is indexed yet")
    ).toBeTruthy()
  })

  it("leaves the group out when sources can't be read", async () => {
    stubApi({
      ...sourceRoutes([]),
      "GET /v1/organizations/org_1/sources": () => json({}, 404),
    })
    renderKnowledge()
    await findRailItem("Handbook")
    expect(screen.queryByRole("list", { name: "Sources" })).toBeNull()
  })
})

describe("opening a Library file", () => {
  const text = (body: string, type = "text/markdown") =>
    new Response(body, { headers: { "Content-Type": type } })

  it("opens an uploaded file in the shared frame, with where it sits and its facts", async () => {
    const onOpenFileChange = vi.fn()
    stubApi({
      ...detailRoutes(corpus(), () => [
        document({
          id: "doc_3",
          title: "launch-notes.md",
          source: { kind: "file", ref: "file_1" },
          content_type: "text/markdown",
        }),
      ]),
      "GET /v1/organizations/org_1/files/file_1/content": () =>
        text("# Launch\n\nShip on the 14th."),
    })
    const { onSelect } = renderKnowledge({ ...openHandbook, onOpenFileChange })

    fireEvent.click(
      await screen.findByRole("button", { name: "Open launch-notes.md" })
    )
    const crumbs = await screen.findByRole("navigation", {
      name: "Where this file is",
    })
    expect(crumbs.textContent).toBe("LibraryHandbook")
    expect(screen.getByText("launch-notes.md")).toBeTruthy()
    await waitFor(() =>
      expect(
        screen.getByText("Ada Lovelace · File · Markdown · 2 KB")
      ).toBeTruthy()
    )
    expect(
      screen.getByRole("button", { name: "Download launch-notes.md" })
    ).toBeTruthy()
    expect(await screen.findByText("Ship on the 14th.")).toBeTruthy()
    expect(onOpenFileChange).toHaveBeenLastCalledWith({
      kind: "document",
      id: "doc_3",
      name: "launch-notes.md",
    })

    fireEvent.click(screen.getByRole("button", { name: "Back to Handbook" }))
    expect(
      await screen.findByRole("table", { name: "Files in Handbook" })
    ).toBeTruthy()
    expect(onOpenFileChange).toHaveBeenLastCalledWith(null)

    fireEvent.click(
      screen.getByRole("button", { name: "Open launch-notes.md" })
    )
    fireEvent.click(
      within(
        await screen.findByRole("navigation", { name: "Where this file is" })
      ).getByRole("button", { name: "Library" })
    )
    expect(onSelect).toHaveBeenLastCalledWith(null)
  })

  it("reads a connected app's document from knowledge, with its original", async () => {
    stubApi({
      ...detailRoutes(corpus({ metadata: { origin: "knowledge" } }), () => [
        document({
          id: "doc_app",
          title: "EU launch plan",
          source: { kind: "external", ref: "knowledge:kdoc_1" },
          content_type: "text/markdown",
          metadata: { provider: "notion", knowledge_document_id: "kdoc_1" },
        }),
      ]),
      "GET /v1/organizations/org_1/documents/kdoc_1": {
        id: "kdoc_1",
        title: "EU launch plan",
        url: "https://notion.so/acme/eu-launch",
        mime: "text/markdown",
        body: "Live in five countries.",
        byte_size: 24,
      },
    })
    renderKnowledge(openHandbook)

    fireEvent.click(
      await screen.findByRole("button", { name: "Open EU launch plan" })
    )
    expect(await screen.findByText("Live in five countries.")).toBeTruthy()
    const original = await screen.findByRole("link", { name: "Open original" })
    expect(original.getAttribute("href")).toBe(
      "https://notion.so/acme/eu-launch"
    )
    expect(
      screen.queryByRole("button", { name: "Download EU launch plan" })
    ).toBeNull()
  })

  it("says plainly that a note has no preview yet", async () => {
    stubApi(detailRoutes(corpus(), () => [document()]))
    renderKnowledge(openHandbook)

    fireEvent.click(
      await screen.findByRole("button", { name: "Open Decision log" })
    )
    expect(
      await screen.findByText("Preview isn’t available for notes yet.")
    ).toBeTruthy()
  })
})
