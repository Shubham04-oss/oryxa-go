// @vitest-environment jsdom

import { StrictMode } from "react"
import type { ReactNode } from "react"
import {
  createMemoryHistory,
  createRootRoute,
  createRouter,
  RouterProvider,
} from "@tanstack/react-router"
import { cleanup, fireEvent, render, screen } from "@testing-library/react"
import { afterEach, describe, expect, it, vi } from "vitest"
import { ResetPasswordPage, VerifyEmailPage } from "./auth-pages"

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
