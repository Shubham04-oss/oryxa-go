// @vitest-environment jsdom

import { afterEach, describe, expect, it, vi } from "vitest"
import {
  cleanup,
  fireEvent,
  render,
  screen,
  waitFor,
} from "@testing-library/react"
import { WorkspaceTools } from "./workspace-tools"
import { previewToolkits } from "./workspace-tools-data"

function apiResponse(body: unknown, status = 200): Response {
  return {
    ok: status >= 200 && status < 300,
    status,
    json: async () => body,
  } as Response
}

afterEach(() => {
  cleanup()
  vi.unstubAllGlobals()
})

describe("WorkspaceTools", () => {
  it("labels offline examples and lets the logo strip expand without implying a live connection", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async () => apiResponse({ error: { message: "offline" } }, 502))
    )
    render(<WorkspaceTools />)

    expect(await screen.findByText(/Layout preview/)).toBeTruthy()
    expect(
      screen.getByRole("heading", { name: "Example connections" })
    ).toBeTruthy()
    expect(screen.getByRole("heading", { name: "Gmail" })).toBeTruthy()
    expect(screen.queryByRole("button", { name: "Connect Gmail" })).toBeNull()
    expect(
      screen.queryByText("Keep email within reach for the work that matters.")
    ).toBeNull()
    fireEvent.click(screen.getByRole("button", { name: "Gmail details" }))
    expect(
      screen
        .getByRole("button", { name: "Gmail details" })
        .closest(".workspace-tool-card")
        ?.classList.contains("is-expanded")
    ).toBe(true)
    expect(
      screen.getByText("Keep email within reach for the work that matters.")
    ).toBeTruthy()
    fireEvent.click(screen.getByRole("button", { name: "Gmail details" }))
    expect(
      screen.queryByText("Keep email within reach for the work that matters.")
    ).toBeNull()

    fireEvent.click(
      screen.getByRole("button", { name: "Expand GitHub connection" })
    )
    expect(
      screen
        .getByRole("button", { name: "Expand GitHub connection" })
        .classList.contains("workspace-tools-connected-item")
    ).toBe(true)
    expect(
      screen
        .getByRole("button", { name: "Expand GitHub connection" })
        .classList.contains("is-selected")
    ).toBe(true)
    expect(
      screen.getByText("Example connection · no account linked")
    ).toBeTruthy()
    expect(screen.getByRole("tab", { name: "Events" })).toBeTruthy()
    expect(screen.getByText("New item")).toBeTruthy()
    fireEvent.click(screen.getByRole("tab", { name: "Indexing" }))
    expect(screen.getByText("Example source")).toBeTruthy()
    fireEvent.click(screen.getByRole("tab", { name: "Runs" }))
    expect(screen.getByText("Example run")).toBeTruthy()

    fireEvent.change(screen.getByRole("searchbox", { name: "Search tools" }), {
      target: { value: "zoom" },
    })
    expect(screen.getByRole("heading", { name: "Zoom" })).toBeTruthy()
    expect(screen.queryByRole("heading", { name: "Gmail" })).toBeNull()
  })

  it("uses the Apps and Auth APIs for personal connections and catalog search", async () => {
    let subscribed = false
    let indexed = false
    const fetchMock = vi.fn(
      async (input: RequestInfo | URL, options?: RequestInit) => {
        const url = String(input)
        const method = options?.method || "GET"
        if (url === "/v1/apps/catalog?limit=1")
          return apiResponse({
            toolkits: [previewToolkits[0]],
            next_cursor: null,
          })
        if (url === "/v1/auth/me")
          return apiResponse({ organizations: [{ id: "org-1", name: "Acme" }] })
        if (url === "/v1/organizations/org-1/apps/connections")
          return apiResponse({
            connections: [
              {
                id: "connection-1",
                toolkit: "github",
                status: "active",
                label: "github-acme",
              },
            ],
          })
        if (url === "/v1/apps/catalog/github/triggers?limit=50")
          return apiResponse({
            triggers: [
              {
                trigger: "GITHUB_PUSH_EVENT",
                name: "Push",
                description: "A repository receives a push.",
                kind: "webhook",
                config_schema: {
                  properties: { repo: { type: "string", title: "Repository" } },
                  required: ["repo"],
                },
              },
            ],
          })
        if (
          url ===
          "/v1/organizations/org-1/apps/triggers?connection_id=connection-1"
        )
          return apiResponse({
            triggers: subscribed
              ? [
                  {
                    id: "trigger-1",
                    trigger: "GITHUB_PUSH_EVENT",
                    status: "active",
                    delivery_count: 0,
                  },
                ]
              : [],
          })
        if (
          url === "/v1/organizations/org-1/apps/triggers" &&
          method === "POST"
        ) {
          subscribed = true
          return apiResponse({ id: "trigger-1", status: "active" }, 201)
        }
        if (url === "/v1/sources/providers")
          return apiResponse({ providers: ["github"] })
        if (
          url === "/v1/organizations/org-1/sources?connection_id=connection-1"
        )
          return apiResponse({
            sources: indexed
              ? [
                  {
                    id: "source-1",
                    provider: "github",
                    target: { repository: "oryxa/app" },
                    status: "active",
                    interval_seconds: 3600,
                  },
                ]
              : [],
          })
        if (
          url ===
          "/v1/organizations/org-1/apps/connections/connection-1/targets"
        )
          return apiResponse({
            targets: [
              {
                id: "repo-1",
                name: "oryxa/app",
                target: { repository: "oryxa/app" },
              },
            ],
          })
        if (url === "/v1/organizations/org-1/sources" && method === "POST") {
          indexed = true
          return apiResponse({ id: "source-1" }, 201)
        }
        if (url === "/v1/organizations/org-1/sources/source-1/runs?limit=10")
          return apiResponse({
            runs: [
              {
                id: "run-1",
                status: "completed",
                started_at: "2026-09-23T10:00:00Z",
                documents_seen: 12,
                documents_published: 8,
                documents_removed: 1,
              },
            ],
          })
        if (url.startsWith("/v1/apps/catalog?search="))
          return apiResponse({
            toolkits: [
              {
                toolkit: "slack",
                name: "Slack",
                description: "Team messages",
                categories: [{ id: "messaging", name: "Messaging" }],
                connectable: true,
                tools_count: 42,
                triggers_count: 3,
              },
            ],
            next_cursor: null,
          })
        const slug = url.split("/").at(-1)
        const toolkit = previewToolkits.find((item) => item.toolkit === slug)
        return toolkit
          ? apiResponse({ ...toolkit, connectable: true })
          : apiResponse({ error: { message: "missing" } }, 404)
      }
    )
    vi.stubGlobal("fetch", fetchMock)
    render(<WorkspaceTools organizationId="org-1" />)

    expect(
      await screen.findByRole("heading", { name: "Connected tools" })
    ).toBeTruthy()
    expect(
      await screen.findByRole("button", { name: "Expand GitHub connection" })
    ).toBeTruthy()
    expect(screen.queryByText(/Layout preview/)).toBeNull()
    await waitFor(() =>
      expect(screen.getByRole("button", { name: "Gmail details" })).toBeTruthy()
    )
    expect(screen.queryByRole("button", { name: "Connect Gmail" })).toBeNull()
    fireEvent.click(screen.getByRole("button", { name: "Gmail details" }))
    expect(
      screen
        .getByRole("button", { name: "Connect Gmail" })
        .hasAttribute("disabled")
    ).toBe(false)
    fireEvent.click(
      screen.getByRole("button", { name: "Expand GitHub connection" })
    )
    expect(screen.getByText("github-acme")).toBeTruthy()

    fireEvent.click(await screen.findByRole("button", { name: "Add event" }))
    fireEvent.click(await screen.findByRole("button", { name: /^Push/ }))
    fireEvent.change(screen.getByRole("textbox", { name: "Repository *" }), {
      target: { value: "oryxa/app" },
    })
    fireEvent.click(screen.getByRole("button", { name: "Watch event" }))
    await waitFor(() => expect(subscribed).toBe(true))
    expect(fetchMock).toHaveBeenCalledWith(
      "/v1/organizations/org-1/apps/triggers",
      expect.objectContaining({
        method: "POST",
        body: JSON.stringify({
          connection_id: "connection-1",
          trigger: "GITHUB_PUSH_EVENT",
          config: { repo: "oryxa/app" },
        }),
      })
    )
    // Wait for the "add event" form (its own heading also reads "Push") to
    // close before looking for the subscribed trigger's row, so this finds
    // the active row's dot rather than the closing form's heading.
    await waitFor(() =>
      expect(screen.queryByRole("button", { name: "Watch event" })).toBeNull()
    )
    const activeDot = (await screen.findByText("Push"))
      .closest(".workspace-tool-row")
      ?.querySelector(".workspace-tool-dot")
    expect(activeDot?.classList.contains("workspace-tool-dot")).toBe(true)
    expect(activeDot?.classList.contains("is-live")).toBe(true)

    fireEvent.click(screen.getByRole("tab", { name: "Indexing" }))
    fireEvent.click(await screen.findByRole("button", { name: "Add source" }))
    fireEvent.change(
      await screen.findByRole("combobox", { name: "Repository *" }),
      {
        target: { value: "repo-1" },
      }
    )
    fireEvent.click(screen.getByRole("button", { name: "Register source" }))
    await waitFor(() => expect(indexed).toBe(true))
    expect(fetchMock).toHaveBeenCalledWith(
      "/v1/organizations/org-1/sources",
      expect.objectContaining({
        method: "POST",
        body: JSON.stringify({
          connection_id: "connection-1",
          provider: "github",
          target: { repository: "oryxa/app" },
          interval_seconds: 3600,
        }),
      })
    )
    fireEvent.click(screen.getByRole("tab", { name: "Runs" }))
    expect(
      await screen.findByText("8 published · 1 removed · 12 seen")
    ).toBeTruthy()

    fireEvent.change(screen.getByRole("searchbox", { name: "Search tools" }), {
      target: { value: "slack" },
    })
    expect(
      await screen.findByRole("heading", { name: "Messaging" })
    ).toBeTruthy()
    fireEvent.click(screen.getByRole("button", { name: "Slack details" }))
    expect(screen.getByRole("button", { name: "Connect Slack" })).toBeTruthy()
    expect(fetchMock).toHaveBeenCalledWith(
      "/v1/organizations/org-1/apps/connections",
      expect.objectContaining({ credentials: "include" })
    )
  })

  it("uses the organizationId prop, never the first organization from /v1/auth/me", async () => {
    const requestedUrls: string[] = []
    const fetchMock = vi.fn(async (input: RequestInfo | URL) => {
      const url = String(input)
      requestedUrls.push(url)
      if (url === "/v1/apps/catalog?limit=1")
        return apiResponse({ toolkits: [], next_cursor: null })
      if (url === "/v1/auth/me")
        return apiResponse({
          organizations: [
            { id: "org_a", name: "Org A" },
            { id: "org_b", name: "Org B" },
          ],
        })
      if (url === "/v1/organizations/org_b/apps/connections")
        return apiResponse({ connections: [] })
      const slug = url.split("/").at(-1)
      const toolkit = previewToolkits.find((item) => item.toolkit === slug)
      return toolkit
        ? apiResponse({ ...toolkit, connectable: true })
        : apiResponse({ error: { message: "missing" } }, 404)
    })
    vi.stubGlobal("fetch", fetchMock)
    render(<WorkspaceTools organizationId="org_b" />)

    await waitFor(() =>
      expect(requestedUrls).toContain(
        "/v1/organizations/org_b/apps/connections"
      )
    )
    expect(requestedUrls).not.toContain(
      "/v1/organizations/org_a/apps/connections"
    )
  })
})
