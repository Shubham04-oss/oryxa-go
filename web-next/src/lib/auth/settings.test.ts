import { afterEach, describe, expect, it, vi } from "vitest"
import {
  authorizeAgentGrant,
  getAgentGrant,
  getAppPreferences,
  organizationProfileSchema,
  revokeAgentGrant,
  setAppUpdates,
  updateOrganization,
} from "./settings"

function json(body: unknown) {
  return new Response(JSON.stringify(body), {
    headers: { "Content-Type": "application/json" },
  })
}

afterEach(() => vi.unstubAllGlobals())

describe("settings API", () => {
  it("validates and normalises the organisation profile", () => {
    expect(
      organizationProfileSchema.parse({
        name: " Product Labs ",
        slug: "Product Labs!",
      })
    ).toEqual({ name: "Product Labs", slug: "product-labs" })
    expect(
      organizationProfileSchema.safeParse({ name: "", slug: "a" }).success
    ).toBe(false)
  })

  it("saves an organisation with the selected id", async () => {
    const fetchMock = vi.fn(async () =>
      json({ id: "org/1", name: "Product", slug: "product" })
    )
    vi.stubGlobal("fetch", fetchMock)
    await updateOrganization("org/1", { name: "Product", slug: "product" })
    expect(fetchMock).toHaveBeenCalledWith(
      "/v1/organizations/org%2F1",
      expect.objectContaining({
        method: "PATCH",
        credentials: "include",
        body: JSON.stringify({ name: "Product", slug: "product" }),
      })
    )
  })

  it("reads and saves app updates for the selected organisation", async () => {
    const fetchMock = vi.fn(async () => json({ app_updates: false }))
    vi.stubGlobal("fetch", fetchMock)
    expect((await getAppPreferences("org-1")).app_updates).toBe(false)
    await setAppUpdates("org-1", false)
    expect(fetchMock).toHaveBeenCalledWith(
      "/v1/organizations/org-1/preferences",
      expect.objectContaining({ method: "PUT", body: '{"app_updates":false}' })
    )
  })

  it("uses the existing agent-grant flow", async () => {
    const fetchMock = vi.fn(async () =>
      json({ granted: false, authorization_url: "https://example.test/grant" })
    )
    vi.stubGlobal("fetch", fetchMock)
    await getAgentGrant()
    await authorizeAgentGrant("https://app.example.test/?org=org-1")
    await revokeAgentGrant()
    expect(fetchMock).toHaveBeenCalledWith(
      "/v1/agent-grant/authorize",
      expect.objectContaining({
        body: '{"return_to":"https://app.example.test/?org=org-1"}',
      })
    )
    expect(fetchMock).toHaveBeenCalledWith(
      "/v1/agent-grant",
      expect.objectContaining({ method: "DELETE" })
    )
  })
})
