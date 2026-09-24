import { afterEach, describe, expect, it, vi } from "vitest"
import { ApiError } from "@/lib/api/client"
import { appsErrorMessage } from "@/lib/apps/client"
import {
  effectiveKinds,
  kindsQueryOptions,
  knowledgeQueryKey,
  listKinds,
  listProviders,
  listRuns,
  listSources,
  providersQueryOptions,
  registerSource,
  removeSource,
  setSourceKinds,
  sourceReasonText,
  sourceRunsQueryKey,
  sourceRunsQueryOptions,
  sourcesQueryOptions,
  syncSource,
} from "./client"
import type { Source, SourceKind, SyncRun } from "./client"

function json(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  })
}

function stubFetch(response: () => Response) {
  const fetchMock = vi.fn(
    async (_input: RequestInfo | URL, _init?: RequestInit) => response()
  )
  vi.stubGlobal("fetch", fetchMock)
  return fetchMock
}

afterEach(() => vi.unstubAllGlobals())

function source(overrides: Partial<Source> = {}): Source {
  return {
    id: "src_1",
    provider: "github",
    connection_id: "apc_1",
    target: { repository: "acme/api" },
    scope: "member",
    member_id: "user-1",
    created_by: "user-1",
    status: "active",
    interval_seconds: 3600,
    created_at: "2026-09-20T00:00:00Z",
    updated_at: "2026-09-20T00:00:00Z",
    ...overrides,
  }
}

const kinds: SourceKind[] = [
  { name: "conversation", label: "Conversations", hint: "", default: true },
  { name: "work_material", label: "Documents", hint: "", default: true },
  { name: "newsletter", label: "Newsletters", hint: "", default: false },
]

// Routes are knowledge/internal/httpserver/server.go's; bodies are what its
// handlers decode (DisallowUnknownFields), in the order they are sent.
describe("knowledge API contracts", () => {
  it("reads the providers and the kinds without an organization", async () => {
    const fetchMock = stubFetch(() => json({ providers: ["github", "gmail"] }))
    await expect(listProviders()).resolves.toEqual(["github", "gmail"])
    expect(fetchMock).toHaveBeenCalledWith(
      "/v1/sources/providers",
      expect.objectContaining({ method: "GET", credentials: "include" })
    )

    const kindsFetch = stubFetch(() => json({ kinds }))
    await expect(listKinds()).resolves.toEqual(kinds)
    expect(kindsFetch).toHaveBeenCalledWith(
      "/v1/sources/kinds",
      expect.objectContaining({ method: "GET" })
    )
  })

  it("lists sources with no query by default, and with each filter it is given", async () => {
    const fetchMock = stubFetch(() => json({ sources: [source()] }))
    await expect(listSources("org_1")).resolves.toEqual([source()])
    expect(fetchMock).toHaveBeenLastCalledWith(
      "/v1/organizations/org_1/sources",
      expect.objectContaining({ method: "GET" })
    )

    await listSources("org 1", {
      connectionId: "apc_1",
      provider: "github",
      scope: "organization",
    })
    expect(fetchMock).toHaveBeenLastCalledWith(
      "/v1/organizations/org%201/sources?connection_id=apc_1&provider=github&scope=organization",
      expect.objectContaining({ method: "GET" })
    )
  })

  it("registers a source with exactly the four fields the handler reads", async () => {
    const fetchMock = stubFetch(() => json(source(), 201))
    await expect(
      registerSource("org_1", {
        connectionId: "apc_1",
        provider: "github",
        target: { repository: "acme/api" },
        intervalSeconds: 900,
      })
    ).resolves.toEqual(source())
    expect(fetchMock).toHaveBeenCalledWith(
      "/v1/organizations/org_1/sources",
      expect.objectContaining({
        method: "POST",
        body: JSON.stringify({
          connection_id: "apc_1",
          provider: "github",
          target: { repository: "acme/api" },
          interval_seconds: 900,
        }),
        headers: { "Content-Type": "application/json" },
      })
    )
  })

  it("leaves out what is not given, so the server applies its defaults", async () => {
    const fetchMock = stubFetch(() => json(source({ provider: "files" }), 201))
    await registerSource("org_1", { provider: "files", target: {} })
    expect(fetchMock).toHaveBeenCalledWith(
      "/v1/organizations/org_1/sources",
      expect.objectContaining({
        body: JSON.stringify({ provider: "files", target: {} }),
      })
    )
  })

  it("removes a source with DELETE and reads the 204 as done", async () => {
    const fetchMock = stubFetch(() => new Response(null, { status: 204 }))
    await expect(removeSource("org_1", "src/1")).resolves.toBeUndefined()
    expect(fetchMock).toHaveBeenCalledWith(
      "/v1/organizations/org_1/sources/src%2F1",
      expect.objectContaining({ method: "DELETE", body: undefined })
    )
  })

  it("syncs a source with a bodiless POST and answers the source", async () => {
    const due = source({ sync_at: "2026-09-23T10:00:00Z" })
    const fetchMock = stubFetch(() => json(due))
    await expect(syncSource("org_1", "src_1")).resolves.toEqual(due)
    expect(fetchMock).toHaveBeenCalledWith(
      "/v1/organizations/org_1/sources/src_1/sync",
      expect.objectContaining({ method: "POST", body: undefined })
    )
  })

  it("sets the kinds a source indexes with PUT, and null for the defaults", async () => {
    const fetchMock = stubFetch(() =>
      json(source({ include_kinds: ["newsletter"] }))
    )
    await setSourceKinds("org_1", "src_1", ["newsletter"])
    expect(fetchMock).toHaveBeenLastCalledWith(
      "/v1/organizations/org_1/sources/src_1/kinds",
      expect.objectContaining({
        method: "PUT",
        body: JSON.stringify({ include_kinds: ["newsletter"] }),
      })
    )

    await setSourceKinds("org_1", "src_1", null)
    expect(fetchMock).toHaveBeenLastCalledWith(
      "/v1/organizations/org_1/sources/src_1/kinds",
      expect.objectContaining({
        method: "PUT",
        body: JSON.stringify({ include_kinds: null }),
      })
    )
  })

  it("lists runs with a limit when one is given", async () => {
    const run: SyncRun = {
      id: "run_1",
      organization_id: "org_1",
      source_id: "src_1",
      kind: "incremental",
      status: "completed",
      documents_seen: 4,
      documents_published: 3,
      documents_removed: 0,
      documents_excluded: 1,
      started_at: "2026-09-23T10:00:00Z",
    }
    const fetchMock = stubFetch(() => json({ runs: [run] }))
    await expect(listRuns("org_1", "src_1", 10)).resolves.toEqual([run])
    expect(fetchMock).toHaveBeenLastCalledWith(
      "/v1/organizations/org_1/sources/src_1/runs?limit=10",
      expect.objectContaining({ method: "GET" })
    )

    await listRuns("org_1", "src_1")
    expect(fetchMock).toHaveBeenLastCalledWith(
      "/v1/organizations/org_1/sources/src_1/runs",
      expect.objectContaining({ method: "GET" })
    )
  })

  it("surfaces knowledge's error codes, which apps' humanizer reads", async () => {
    stubFetch(() =>
      json(
        {
          error: {
            code: "needs_reconnect",
            message: "the connection must be reconnected first",
          },
        },
        409
      )
    )
    const error = await syncSource("org_1", "src_1").catch(
      (caught: unknown) => caught
    )
    expect(error).toBeInstanceOf(ApiError)
    expect(error).toMatchObject({ status: 409, code: "needs_reconnect" })
    expect(appsErrorMessage(error, "fallback")).toBe(
      "Reconnect this app to keep using it."
    )

    stubFetch(() =>
      json(
        {
          error: {
            code: "provider_unavailable",
            message:
              "the provider or a service it depends on did not answer; retry",
          },
        },
        503
      )
    )
    const unavailable = await listSources("org_1").catch(
      (caught: unknown) => caught
    )
    expect(appsErrorMessage(unavailable, "Sources could not be loaded.")).toBe(
      "Sources could not be loaded."
    )
  })
})

describe("knowledge query options", () => {
  it("keeps the providers and kinds for the session", () => {
    expect(providersQueryOptions().queryKey).toEqual(["knowledge", "providers"])
    expect(providersQueryOptions().staleTime).toBe(Number.POSITIVE_INFINITY)
    expect(kindsQueryOptions().queryKey).toEqual(["knowledge", "kinds"])
    expect(kindsQueryOptions().staleTime).toBe(Number.POSITIVE_INFINITY)
  })

  it("keys sources under the organization by connection, provider and scope", async () => {
    const fetchMock = stubFetch(() => json({ sources: [] }))
    const options = sourcesQueryOptions("org_1", { connectionId: "apc_1" })
    expect(options.queryKey).toEqual([
      "knowledge",
      "org_1",
      "sources",
      "apc_1",
      "",
      "mine",
    ])
    expect(options.queryKey.slice(0, 2)).toEqual(knowledgeQueryKey("org_1"))
    await options.queryFn?.({} as never)
    expect(fetchMock).toHaveBeenCalledWith(
      "/v1/organizations/org_1/sources?connection_id=apc_1",
      expect.anything()
    )
  })

  it("keys runs under their source, with the limit last", async () => {
    const fetchMock = stubFetch(() => json({ runs: [] }))
    const options = sourceRunsQueryOptions("org_1", "src_1")
    expect(options.queryKey).toEqual([
      ...sourceRunsQueryKey("org_1", "src_1"),
      10,
    ])
    expect(sourceRunsQueryKey("org_1", "src_1")).toEqual([
      "knowledge",
      "org_1",
      "runs",
      "src_1",
    ])
    await options.queryFn?.({} as never)
    expect(fetchMock).toHaveBeenCalledWith(
      "/v1/organizations/org_1/sources/src_1/runs?limit=10",
      expect.anything()
    )
  })
})

describe("reading a source", () => {
  it("indexes the default kinds until the member chooses", () => {
    expect(effectiveKinds(kinds, undefined)).toEqual([
      "conversation",
      "work_material",
    ])
    expect(effectiveKinds(kinds, ["newsletter"])).toEqual(["newsletter"])
  })

  it("says why a source is paused in words, never as its code", () => {
    expect(sourceReasonText("needs_reconnect")).toBe(
      "Paused until the app is reconnected."
    )
    expect(sourceReasonText("scope_required")).toMatch(/missing a permission/)
    expect(sourceReasonText("disconnected")).toBe(
      "Paused: the app was disconnected."
    )
    expect(sourceReasonText("app_deleted")).toBe("Paused: the app was removed.")
    expect(sourceReasonText(undefined)).toBe("Paused.")
    expect(sourceReasonText("some_new_reason")).toBe("Paused: some new reason.")
  })
})
