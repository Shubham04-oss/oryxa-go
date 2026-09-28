// @vitest-environment jsdom

import type { ReactNode } from "react"
import {
  createMemoryHistory,
  createRootRoute,
  createRouter,
  RouterProvider,
} from "@tanstack/react-router"
import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import {
  cleanup,
  fireEvent,
  render,
  screen,
  waitFor,
} from "@testing-library/react"
import { afterEach, describe, expect, it, vi } from "vitest"
import { InvitationAcceptPage } from "./invitation-accept-page"

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

// This page renders <Link> and calls useNavigate(), which need a router
// context to mount, and useQuery/useMutation, which need a query client.
function renderWithRouter(component: () => ReactNode) {
  const rootRoute = createRootRoute({ component })
  const router = createRouter({
    routeTree: rootRoute,
    history: createMemoryHistory({ initialEntries: ["/"] }),
  })
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })
  return render(
    <QueryClientProvider client={client}>
      <RouterProvider router={router} />
    </QueryClientProvider>
  )
}

// jsdom's Location doesn't let vi.spyOn redefine `replace` (it refuses a
// real navigation anyway), so the whole object is deleted and swapped out
// for one whose `replace` we can observe. `Window.location`'s ambient type
// has a setter narrower than its getter, so the reassignment goes through a
// differently-typed view of `window` instead of fighting that type.
function setWindowLocation(value: Location) {
  const win = window as unknown as { location: Location }
  // @ts-expect-error -- window.location is not optional, but jsdom lets it
  // be deleted so a plain stub object can take its place.
  delete win.location
  win.location = value
}

function stubLocation(pathname: string, search: string) {
  const original = window.location
  const replace = vi.fn()
  setWindowLocation({ ...original, pathname, search, replace })
  return {
    replace,
    restore: () => setWindowLocation(original),
  }
}

describe("InvitationAcceptPage", () => {
  it("offers a way out of the wrong-account state: sign out, then return here to sign in again", async () => {
    const location = stubLocation("/invitations/accept", "?token=tok-1")
    const fetchMock = vi.fn(async (url: string, init?: RequestInit) => {
      if (url === "/v1/auth/me") {
        return jsonResponse({
          user: { id: "u1", email: "me@example.com" },
          organizations: [],
        })
      }
      if (url === "/v1/invitations/accept") {
        return jsonResponse(
          { error: "This invitation was sent to someone else." },
          403
        )
      }
      if (url === "/v1/auth/logout" && init?.method === "POST") {
        return new Response(null, { status: 204 })
      }
      throw new Error(`unexpected fetch: ${init?.method || "GET"} ${url}`)
    })
    vi.stubGlobal("fetch", fetchMock)

    renderWithRouter(() => <InvitationAcceptPage token="tok-1" />)

    fireEvent.click(
      await screen.findByRole("button", { name: "Accept invitation" })
    )
    expect(
      await screen.findByText(
        "Sign in with the email address this invitation was sent to."
      )
    ).toBeTruthy()

    fireEvent.click(
      screen.getByRole("button", { name: "Sign out and use another account" })
    )
    await waitFor(() =>
      expect(fetchMock).toHaveBeenCalledWith(
        "/v1/auth/logout",
        expect.objectContaining({ method: "POST" })
      )
    )
    await waitFor(() =>
      expect(location.replace).toHaveBeenCalledWith(
        "/auth/sign-in?redirect=%2Finvitations%2Faccept%3Ftoken%3Dtok-1"
      )
    )

    location.restore()
  })

  it("shows an error and stays put when signing out fails", async () => {
    const location = stubLocation("/invitations/accept", "?token=tok-1")
    const fetchMock = vi.fn(async (url: string) => {
      if (url === "/v1/auth/me") {
        return jsonResponse({
          user: { id: "u1", email: "me@example.com" },
          organizations: [],
        })
      }
      if (url === "/v1/invitations/accept") {
        return jsonResponse({ error: "wrong account" }, 403)
      }
      if (url === "/v1/auth/logout") {
        return new Response(null, { status: 500 })
      }
      throw new Error(`unexpected fetch: ${url}`)
    })
    vi.stubGlobal("fetch", fetchMock)

    renderWithRouter(() => <InvitationAcceptPage token="tok-1" />)

    fireEvent.click(
      await screen.findByRole("button", { name: "Accept invitation" })
    )
    fireEvent.click(
      await screen.findByRole("button", {
        name: "Sign out and use another account",
      })
    )

    expect(
      await screen.findByText("Could not sign out. Try again.")
    ).toBeTruthy()
    expect(location.replace).not.toHaveBeenCalled()

    location.restore()
  })
})
