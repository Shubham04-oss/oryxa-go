import { afterEach, describe, expect, it, vi } from "vitest"
import { ApiError } from "@/lib/api/client"
import {
  createCorpusPolicy,
  deleteCorpusPolicy,
  listCorpusPolicies,
  policyErrorMessage,
} from "./client"
import type { Policy } from "./client"

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

afterEach(() => vi.unstubAllGlobals())

function policy(overrides: Partial<Policy> = {}): Policy {
  return {
    id: "pol_1",
    name: "Hide confidential",
    text: "forbid (principal, action, resource) when { resource.metadata has confidential };",
    created_by: "user-1",
    created_at: "2026-01-05T12:00:00Z",
    updated_at: "2026-01-05T12:00:00Z",
    ...overrides,
  }
}

describe("corpus policies routes", () => {
  it("lists a corpus's policies with a plain GET", async () => {
    const fetchMock = stubFetch(jsonResponse({ policies: [policy()] }))
    expect(await listCorpusPolicies("org_1", "cor_1")).toEqual([policy()])
    expect(fetchMock).toHaveBeenCalledWith(
      "/v1/organizations/org_1/corpora/cor_1/policies",
      expect.objectContaining({ credentials: "include", method: "GET" })
    )
  })

  it("encodes ids that need it, in every route", async () => {
    const fetchMock = stubFetch(
      jsonResponse({ policies: [] }),
      jsonResponse({ policy: policy() }, 201),
      new Response(null, { status: 204 })
    )
    await listCorpusPolicies("org/one", "cor one")
    await createCorpusPolicy("org/one", "cor one", {
      name: "x",
      text: "forbid (principal, action, resource);",
    })
    await deleteCorpusPolicy("org/one", "cor one", "pol one")

    expect(fetchMock).toHaveBeenNthCalledWith(
      1,
      "/v1/organizations/org%2Fone/corpora/cor%20one/policies",
      expect.objectContaining({ method: "GET" })
    )
    expect(fetchMock).toHaveBeenNthCalledWith(
      2,
      "/v1/organizations/org%2Fone/corpora/cor%20one/policies",
      expect.objectContaining({ method: "POST" })
    )
    expect(fetchMock).toHaveBeenNthCalledWith(
      3,
      "/v1/organizations/org%2Fone/corpora/cor%20one/policies/pol%20one",
      expect.objectContaining({ method: "DELETE" })
    )
  })

  it("creates a policy from its name and text alone", async () => {
    const created = policy({ id: "pol_2", name: "New policy" })
    const fetchMock = stubFetch(jsonResponse({ policy: created }, 201))
    expect(
      await createCorpusPolicy("org_1", "cor_1", {
        name: "New policy",
        text: created.text,
      })
    ).toEqual(created)
    expect(fetchMock).toHaveBeenCalledWith(
      "/v1/organizations/org_1/corpora/cor_1/policies",
      expect.objectContaining({
        method: "POST",
        body: JSON.stringify({ name: "New policy", text: created.text }),
      })
    )
  })

  it("deletes a policy by id", async () => {
    const fetchMock = stubFetch(new Response(null, { status: 204 }))
    expect(await deleteCorpusPolicy("org_1", "cor_1", "pol_1")).toBeUndefined()
    expect(fetchMock).toHaveBeenCalledWith(
      "/v1/organizations/org_1/corpora/cor_1/policies/pol_1",
      expect.objectContaining({ method: "DELETE" })
    )
  })
})

describe("policyErrorMessage", () => {
  it("shows the validator or Cedar parse-error message from a 400, as a sentence", () => {
    expect(
      policyErrorMessage(
        new ApiError("only forbid statements are allowed", 400),
        "fallback"
      )
    ).toBe("Only forbid statements are allowed.")
    expect(
      policyErrorMessage(
        new ApiError("unexpected token at line 2", 400),
        "fallback"
      )
    ).toBe("Unexpected token at line 2.")
  })

  it("says only admins can manage policies on a 403", () => {
    expect(
      policyErrorMessage(new ApiError("you may not do that here", 403), "x")
    ).toBe("Only organization admins can manage policies.")
  })

  it("names the duplicate on a 409, regardless of the server's own wording", () => {
    expect(
      policyErrorMessage(new ApiError("policy name already used", 409), "x")
    ).toBe("A policy with this name already exists.")
  })

  it("falls back to the caller's message for a 404, a network failure, and a 500", () => {
    expect(policyErrorMessage(new ApiError("not found", 404), "fallback")).toBe(
      "fallback"
    )
    expect(
      policyErrorMessage(new TypeError("Failed to fetch"), "fallback")
    ).toBe("fallback")
    expect(policyErrorMessage(new ApiError("boom", 500), "fallback")).toBe(
      "fallback"
    )
    expect(policyErrorMessage(new ApiError("Request failed", 400), "fb")).toBe(
      "fb"
    )
  })

  it("explains an ended session on a 401", () => {
    expect(policyErrorMessage(new ApiError("nope", 401), "fallback")).toBe(
      "Your session has ended. Sign in again to continue."
    )
  })
})
