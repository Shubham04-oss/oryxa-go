// @vitest-environment jsdom

import { afterEach, describe, expect, it, vi } from "vitest"
import { cleanup, render, screen } from "@testing-library/react"
import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import type { Corpus, RagViewer } from "@/lib/rag/client"
import { KnowledgeBaseDetail } from "./knowledge-base-detail"

function json(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  })
}

/** Routes by `METHOD /path`; an unknown route is a 599 so a test notices it. */
function stubApi(routes: Record<string, unknown>) {
  const calls: Array<{ method: string; path: string }> = []
  const fetchMock = vi.fn<typeof fetch>(async (input, init) => {
    const url = new URL(
      input instanceof Request ? input.url : String(input),
      "http://localhost"
    )
    const method = init?.method ?? "GET"
    calls.push({ method, path: url.pathname })
    const route = routes[`${method} ${url.pathname}`]
    if (route === undefined) {
      return json({ error: `no route for ${method} ${url.pathname}` }, 599)
    }
    return typeof route === "function"
      ? (route as () => Response)()
      : json(route)
  })
  vi.stubGlobal("fetch", fetchMock)
  return {
    find: (method: string, path: string) =>
      calls.filter((call) => call.method === method && call.path === path),
  }
}

function corpus(overrides: Partial<Corpus> = {}): Corpus {
  return {
    id: "cor_1",
    organization_id: "org_1",
    slug: "handbook",
    name: "Handbook",
    scope: "organization",
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
    created_at: "2026-01-01T00:00:00Z",
    updated_at: "2026-01-01T00:00:00Z",
    ...overrides,
  }
}

const corpusPath = "/v1/organizations/org_1/corpora/cor_1"

function renderDetail(viewer: RagViewer) {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })
  return render(
    <QueryClientProvider client={client}>
      <KnowledgeBaseDetail
        organizationId="org_1"
        corpusId="cor_1"
        name="Handbook"
        viewer={viewer}
        onClose={() => {}}
      />
    </QueryClientProvider>
  )
}

afterEach(() => {
  cleanup()
  vi.unstubAllGlobals()
})

describe("a knowledge base's Policies section", () => {
  it("appears for an organization admin, who can see its policies", async () => {
    const api = stubApi({
      [`GET ${corpusPath}`]: corpus(),
      [`GET ${corpusPath}/documents`]: { documents: [] },
      [`GET ${corpusPath}/policies`]: {
        policies: [
          {
            id: "pol_1",
            name: "Hide confidential",
            text: "forbid (principal, action, resource);",
            created_by: "user-1",
            created_at: "2026-01-05T12:00:00Z",
            updated_at: "2026-01-05T12:00:00Z",
          },
        ],
      },
    })
    renderDetail({ userId: "user-1", role: "admin" })

    expect(await screen.findByText("Policies")).toBeTruthy()
    expect(await screen.findByText("Hide confidential")).toBeTruthy()
    expect(
      screen.getByText(
        "Policies can only restrict who sees what here; they never grant access."
      )
    ).toBeTruthy()
    expect(api.find("GET", `${corpusPath}/policies`)).toHaveLength(1)
  })

  it("does not appear for a plain member, who never triggers its request", async () => {
    const api = stubApi({
      [`GET ${corpusPath}`]: corpus(),
      [`GET ${corpusPath}/documents`]: { documents: [] },
    })
    renderDetail({ userId: "user-2", role: "member" })

    await screen.findByText("No documents yet")
    expect(screen.queryByText("Policies")).toBeNull()
    expect(
      screen.queryByText(
        "Policies can only restrict who sees what here; they never grant access."
      )
    ).toBeNull()
    expect(api.find("GET", `${corpusPath}/policies`)).toHaveLength(0)
  })
})
