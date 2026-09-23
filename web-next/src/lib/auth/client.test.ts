import { afterEach, describe, expect, it, vi } from "vitest"
import type { ApiError } from "@/lib/api/client"
import { createOrganization, getSession, signIn, signUp } from "./client"
import {
  acceptInvitationToken,
  getReceivedInvitations,
  sendInvitation,
} from "./invitations"
import { safeRedirect, signUpSchema } from "./schemas"

function jsonResponse(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  })
}

afterEach(() => vi.unstubAllGlobals())

describe("auth API", () => {
  it("maps sign-up fields to the backend contract and sends cookies", async () => {
    const fetchMock = vi.fn(async () =>
      jsonResponse({ status: "verification_requested" }, 202)
    )
    vi.stubGlobal("fetch", fetchMock)

    await signUp({
      displayName: "Avery Morgan",
      email: "avery@example.com",
      password: "Correct-Horse-7!",
    })

    expect(fetchMock).toHaveBeenCalledWith(
      "/v1/auth/password/signup",
      expect.objectContaining({
        method: "POST",
        credentials: "include",
        body: JSON.stringify({
          display_name: "Avery Morgan",
          email: "avery@example.com",
          password: "Correct-Horse-7!",
        }),
      })
    )
  })

  it("treats a wrong password as the answer without attempting refresh", async () => {
    const fetchMock = vi.fn(async () =>
      jsonResponse({ error: "not authorized" }, 401)
    )
    vi.stubGlobal("fetch", fetchMock)

    await expect(
      signIn({ email: "a@example.com", password: "wrong" })
    ).rejects.toMatchObject({
      status: 401,
      message: "not authorized",
    } satisfies Partial<ApiError>)
    expect(fetchMock).toHaveBeenCalledTimes(1)
  })

  it("refreshes an expired session once and retries the protected request", async () => {
    let reads = 0
    const fetchMock = vi.fn(async (url: string) => {
      if (url === "/v1/auth/refresh") return new Response(null, { status: 204 })
      reads += 1
      return reads === 1
        ? jsonResponse({ error: "expired" }, 401)
        : jsonResponse({
            user: { id: "user-1", email: "a@example.com" },
            organizations: [],
          })
    })
    vi.stubGlobal("fetch", fetchMock)

    expect((await getSession()).user.id).toBe("user-1")
    expect(fetchMock).toHaveBeenCalledTimes(3)
  })

  it("creates a workspace for a newly signed-in user", async () => {
    const fetchMock = vi.fn(async () =>
      jsonResponse({ id: "org-1", name: "Product", slug: "product" }, 201)
    )
    vi.stubGlobal("fetch", fetchMock)

    expect((await createOrganization("Product")).id).toBe("org-1")
    expect(fetchMock).toHaveBeenCalledWith(
      "/v1/organizations",
      expect.objectContaining({
        method: "POST",
        body: JSON.stringify({ name: "Product", slug: "" }),
      })
    )
  })

  it("uses the invitation API contract", async () => {
    const fetchMock = vi.fn(async (url: string) => {
      if (url === "/v1/invitations") {
        return jsonResponse({ invitations: [{ id: "inv-1" }] })
      }
      return jsonResponse({ id: "inv-1" }, 201)
    })
    vi.stubGlobal("fetch", fetchMock)

    expect((await getReceivedInvitations())[0].id).toBe("inv-1")
    await sendInvitation("org-1", "a@example.com", "member")
    await acceptInvitationToken("one-time-token")

    expect(fetchMock).toHaveBeenCalledWith(
      "/v1/organizations/org-1/invitations",
      expect.objectContaining({
        method: "POST",
        body: JSON.stringify({ email: "a@example.com", role: "member" }),
      })
    )
    expect(fetchMock).toHaveBeenCalledWith(
      "/v1/invitations/accept",
      expect.objectContaining({
        method: "POST",
        body: JSON.stringify({ token: "one-time-token" }),
      })
    )
  })
})

describe("auth validation", () => {
  it("rejects external redirect targets and accepts workspace paths", () => {
    expect(safeRedirect("/?view=tools")).toBe("/?view=tools")
    expect(safeRedirect("//example.com/steal")).toBeUndefined()
    expect(safeRedirect("https://example.com")).toBeUndefined()
  })

  it("requires the backend password policy before sign-up", () => {
    expect(
      signUpSchema.safeParse({
        displayName: "A",
        email: "no",
        password: "short",
      }).success
    ).toBe(false)
    expect(
      signUpSchema.safeParse({
        displayName: "Avery Morgan",
        email: "avery@example.com",
        password: "Correct-Horse-7!",
      }).success
    ).toBe(true)
  })
})
