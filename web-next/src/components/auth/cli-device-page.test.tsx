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
import { CliDevicePage } from "./cli-device-page"

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

const device = {
  user_code: "HJKM-PQRT",
  client_name: "oryxa-cli on build-box (linux)",
  client_address: "192.0.2.10",
  requested_at: "2026-10-23T10:00:00Z",
  expires_at: "2026-10-23T10:10:00Z",
  grants:
    "Workspace setup for one organization you administer, for at most 15 minutes.",
}

function stubAuth(decide: (path: string, body: unknown) => Response) {
  const fetchMock = vi.fn(async (url: string, init?: RequestInit) => {
    if (url === "/v1/auth/me") {
      return jsonResponse({
        user: { id: "u1", email: "me@example.com" },
        organizations: [],
      })
    }
    const body = init?.body ? JSON.parse(String(init.body)) : undefined
    if (url === "/v1/auth/cli/device/lookup") {
      return String(body?.user_code).toUpperCase() === "HJKM-PQRT"
        ? jsonResponse(device)
        : jsonResponse({ error: "this code is not valid or has expired" }, 404)
    }
    if (url.startsWith("/v1/auth/cli/device/")) return decide(url, body)
    throw new Error(`unexpected fetch: ${init?.method || "GET"} ${url}`)
  })
  vi.stubGlobal("fetch", fetchMock)
  return fetchMock
}

describe("CliDevicePage", () => {
  it("shows which CLI asks and what it gets, then approves", async () => {
    const fetchMock = stubAuth(() => jsonResponse({ status: "approved" }))
    renderWithRouter(() => <CliDevicePage />)
    fireEvent.change(await screen.findByLabelText("Code"), {
      target: { value: "HJKM-PQRT" },
    })
    fireEvent.click(screen.getByRole("button", { name: "Continue" }))

    expect(
      await screen.findByText("oryxa-cli on build-box (linux)")
    ).toBeTruthy()
    expect(screen.getByText("192.0.2.10")).toBeTruthy()
    expect(screen.getByText(/at most 15 minutes/)).toBeTruthy()
    expect(screen.getByText(/as me@example.com/)).toBeTruthy()
    // Nothing is granted by opening the link.
    expect(fetchMock).not.toHaveBeenCalledWith(
      "/v1/auth/cli/device/approve",
      expect.anything()
    )

    fireEvent.click(screen.getByRole("button", { name: "Approve this CLI" }))
    expect(await screen.findByText(/Return to your terminal/)).toBeTruthy()
    expect(fetchMock).toHaveBeenCalledWith(
      "/v1/auth/cli/device/approve",
      expect.objectContaining({
        method: "POST",
        body: JSON.stringify({ user_code: "HJKM-PQRT" }),
      })
    )
  })

  it("denies", async () => {
    const fetchMock = stubAuth(() => jsonResponse({ status: "denied" }))
    renderWithRouter(() => <CliDevicePage />)
    fireEvent.change(await screen.findByLabelText("Code"), {
      target: { value: "HJKM-PQRT" },
    })
    fireEvent.click(screen.getByRole("button", { name: "Continue" }))
    fireEvent.click(await screen.findByRole("button", { name: "Deny" }))
    expect(await screen.findByText(/That CLI gets nothing/)).toBeTruthy()
    expect(fetchMock).toHaveBeenCalledWith(
      "/v1/auth/cli/device/deny",
      expect.objectContaining({ method: "POST" })
    )
  })

  it("asks for the code when the link has none, and explains an expired one", async () => {
    stubAuth(() => jsonResponse({ status: "approved" }))
    renderWithRouter(() => <CliDevicePage />)

    fireEvent.change(await screen.findByLabelText("Code"), {
      target: { value: "CDFG-HJKM" },
    })
    fireEvent.click(screen.getByRole("button", { name: "Continue" }))
    expect(await screen.findByText(/not valid or has expired/)).toBeTruthy()

    fireEvent.click(screen.getByRole("button", { name: "Enter another code" }))
    fireEvent.change(await screen.findByLabelText("Code"), {
      target: { value: "hjkm-pqrt" },
    })
    fireEvent.click(screen.getByRole("button", { name: "Continue" }))
    await waitFor(() =>
      expect(screen.getByText("oryxa-cli on build-box (linux)")).toBeTruthy()
    )
  })
})
