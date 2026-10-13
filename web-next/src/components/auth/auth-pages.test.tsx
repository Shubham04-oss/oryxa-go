// @vitest-environment jsdom

import { StrictMode } from "react"
import type { ReactNode } from "react"
import {
  createMemoryHistory,
  createRootRoute,
  createRouter,
  RouterProvider,
} from "@tanstack/react-router"
import {
  cleanup,
  fireEvent,
  render,
  screen,
  within,
} from "@testing-library/react"
import { afterEach, describe, expect, it, vi } from "vitest"
import { ResetPasswordPage, SignUpPage, VerifyEmailPage } from "./auth-pages"

afterEach(() => {
  cleanup()
  vi.unstubAllGlobals()
})

function jsonResponse(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  })
}

// These pages render <Link>, which needs a router context to mount.
function renderWithRouter(component: () => ReactNode) {
  const rootRoute = createRootRoute({ component })
  const router = createRouter({
    routeTree: rootRoute,
    history: createMemoryHistory({ initialEntries: ["/"] }),
  })
  return render(<RouterProvider router={router} />)
}

describe("SignUpPage", () => {
  it("keeps legal links available through validation and confirmation while preserving signup and its return link", async () => {
    const fetchMock = vi.fn(async (url: string, _init: RequestInit) => {
      if (url === "/v1/auth/config") {
        return jsonResponse({ identity_providers: ["Google"] })
      }
      if (url === "/v1/auth/password/signup") {
        return new Response(null, { status: 204 })
      }
      throw new Error(`unexpected fetch: ${url}`)
    })
    vi.stubGlobal("fetch", fetchMock)

    renderWithRouter(() => <SignUpPage redirect="/?org=org-1" />)

    const legal = await screen.findByRole("navigation", { name: "Legal" })
    const privacy = within(legal).getByRole("link", {
      name: "Privacy Policy (opens in a new tab)",
    })
    const terms = within(legal).getByRole("link", {
      name: "Terms of Service (opens in a new tab)",
    })
    expect(privacy.getAttribute("href")).toBe("https://oryxa.in/legal/privacy")
    expect(terms.getAttribute("href")).toBe("https://oryxa.in/legal/terms")
    for (const link of [privacy, terms]) {
      expect(link.getAttribute("target")).toBe("_blank")
      expect(link.getAttribute("rel")).toContain("noopener")
    }
    expect(
      await screen.findByRole("button", { name: "Continue with Google" })
    ).toBeTruthy()
    const signIn = screen.getByRole("link", { name: "Sign in" })
    expect(
      new URL(
        signIn.getAttribute("href")!,
        "http://localhost"
      ).searchParams.get("redirect")
    ).toBe("/?org=org-1")

    fireEvent.click(screen.getByRole("button", { name: "Create account" }))
    expect(await screen.findByText("Enter your name")).toBeTruthy()
    expect(
      fetchMock.mock.calls.filter(([url]) => url === "/v1/auth/password/signup")
    ).toHaveLength(0)
    expect(screen.getByRole("navigation", { name: "Legal" })).toBe(legal)

    fireEvent.change(screen.getByLabelText("Your name"), {
      target: { value: "Ada Lovelace" },
    })
    fireEvent.change(screen.getByLabelText("Work email"), {
      target: { value: "ada@example.com" },
    })
    fireEvent.change(screen.getByLabelText("Password"), {
      target: { value: "Correct-Horse-7!" },
    })
    fireEvent.click(screen.getByRole("button", { name: "Create account" }))

    await screen.findByText(
      "Check ada@example.com for a verification link, then sign in."
    )
    const signups = fetchMock.mock.calls.filter(
      ([url]) => url === "/v1/auth/password/signup"
    )
    expect(signups).toHaveLength(1)
    expect(JSON.parse(String(signups[0][1].body))).toEqual({
      display_name: "Ada Lovelace",
      email: "ada@example.com",
      password: "Correct-Horse-7!",
    })
    expect(screen.getByRole("navigation", { name: "Legal" })).toBe(legal)
  })
})

describe("VerifyEmailPage", () => {
  it("posts exactly once under StrictMode and reaches the verified state", async () => {
    const fetchMock = vi.fn(async (url: string) => {
      if (url === "/v1/auth/password/verify") {
        return new Response(null, { status: 204 })
      }
      throw new Error(`unexpected fetch: ${url}`)
    })
    vi.stubGlobal("fetch", fetchMock)

    renderWithRouter(() => (
      <StrictMode>
        <VerifyEmailPage token="t" />
      </StrictMode>
    ))

    expect(await screen.findByText("Email verified.")).toBeTruthy()
    expect(
      fetchMock.mock.calls.filter(([url]) => url === "/v1/auth/password/verify")
    ).toHaveLength(1)
  })
})

describe("ResetPasswordPage", () => {
  it("shows the invalid-link state when the reset token was already used", async () => {
    const fetchMock = vi.fn(async () =>
      jsonResponse({ error: "resource state conflicts with the request" }, 409)
    )
    vi.stubGlobal("fetch", fetchMock)

    renderWithRouter(() => <ResetPasswordPage token="t" />)

    fireEvent.change(await screen.findByLabelText("New password"), {
      target: { value: "Correct-Horse-7!" },
    })
    fireEvent.click(screen.getByRole("button", { name: "Update password" }))

    expect(await screen.findByText("This link isn’t available.")).toBeTruthy()
  })
})
