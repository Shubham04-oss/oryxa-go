// @vitest-environment jsdom

import {
  cleanup,
  fireEvent,
  render,
  screen,
  waitFor,
  within,
} from "@testing-library/react"
import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"
import { RoomLive } from "./live-work"
import { childLabel, threadRoom } from "@/lib/live/children"
import type { LiveChild } from "@/lib/live/children"

const room = { kind: "teamspace", id: "tsp_1" } as const
const base = "/live/org_1/rooms/teamspace/tsp_1"

class QuietSocket extends EventTarget {
  send() {}
  close() {}
}

function json(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  })
}

const now = new Date().toISOString()
const sandbox: LiveChild = {
  kind: "sandbox",
  name: "shared",
  status: "running",
  createdAt: now,
  lastUsedAt: now,
}
const browser: LiveChild = {
  kind: "browser",
  name: "br_1",
  status: "running",
  member: "usr_ada",
  createdAt: now,
  lastUsedAt: now,
}

const fetchMock = vi.fn<typeof fetch>()
let handoff = { active: false } as { active: boolean; reason?: string }
let extraEvents: object[] = []

function calls(path: string, method = "GET") {
  return fetchMock.mock.calls.filter(
    ([url, init]) =>
      String(url).includes(path) && (init?.method ?? "GET") === method
  )
}

function bodyOf(path: string, method = "POST") {
  const [, init] = calls(path, method).at(-1) ?? []
  return JSON.parse(String(init?.body)) as unknown
}

beforeEach(() => {
  handoff = { active: false }
  extraEvents = []
  vi.stubGlobal("WebSocket", QuietSocket)
  vi.stubGlobal("fetch", fetchMock)
  fetchMock.mockImplementation(async (url, init) => {
    const target = String(url)
    const method = init?.method ?? "GET"
    if (target.endsWith(`${base}/children`))
      return json({ children: [sandbox, browser] })
    if (target.includes(`${base}/log`)) {
      return json({
        events: [
          {
            seq: 1,
            at: now,
            org: "org_1",
            room,
            actor: { type: "agent", id: "agt_0", onBehalfOf: "usr_ada" },
            run: "run_1",
            type: "sandbox.exec",
            summary: "Ran python report.py",
            data: {},
            durationMs: 1200,
            private: false,
          },
          ...extraEvents,
        ],
        next: 1,
        more: false,
      })
    }
    if (target.includes("/sandbox/shared/files/read")) {
      return json({
        files: [
          {
            path: "src/report.py",
            encoding: "utf8",
            content: "print('hi')",
            size: 11,
          },
        ],
      })
    }
    if (target.includes("/sandbox/shared/files")) {
      const path = new URL(target, "http://x").searchParams.get("path") ?? ""
      return json(
        path === "src"
          ? {
              path: "src",
              entries: [
                {
                  name: "report.py",
                  path: "src/report.py",
                  type: "file",
                  size: 11,
                },
              ],
            }
          : {
              path: "",
              entries: [{ name: "src", path: "src", type: "directory" }],
            }
      )
    }
    if (target.includes("/sandbox/shared/preview")) {
      return json({
        url: "https://edge-staging.oryxa.in/p/token/",
        expiresAt: now,
      })
    }
    if (target.includes("/browser/br_1/live")) {
      const mode = (JSON.parse(String(init?.body)) as { mode: string }).mode
      return json({ url: `https://live.example/${mode}`, expiresAt: now })
    }
    if (target.includes("/browser/br_1/handoff")) return json(handoff)
    if (target.includes("/browser/br_1/resume") && method === "POST") {
      handoff = { active: false }
      return json({ resumed: true })
    }
    return json({ error: { code: "not_found", message: "no" } }, 404)
  })
})

afterEach(() => {
  cleanup()
  fetchMock.mockReset()
  vi.unstubAllGlobals()
})

function renderLive() {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })
  render(
    <QueryClientProvider client={client}>
      <RoomLive organizationId="org_1" room={room} viewerId="usr_ada" />
    </QueryClientProvider>
  )
}

describe("the live client", () => {
  it("names the room an agent's conversation runs in, only for an id the edge takes", () => {
    expect(threadRoom("thr_123")).toEqual({ kind: "thread", id: "thr_123" })
    expect(threadRoom("a thread/with spaces")).toBeUndefined()
    expect(threadRoom(null)).toBeUndefined()
  })

  it("names sandboxes and browsers for a member", () => {
    expect(childLabel(sandbox)).toBe("Shared sandbox")
    expect(childLabel({ ...sandbox, name: "private-usr_ada" })).toBe(
      "Your private sandbox"
    )
    expect(childLabel({ ...sandbox, name: "run_1" })).toBe("A run's sandbox")
    expect(childLabel(browser, "usr_ada")).toBe("Your browser")
    expect(childLabel(browser, "usr_raya")).toBe("Browser")
  })
})

describe("RoomLive", () => {
  it("shows the room's sandboxes and browsers, and what agents did", async () => {
    renderLive()
    expect(
      await screen.findByRole("button", { name: "Open Shared sandbox" })
    ).toBeTruthy()
    expect(
      screen.getByRole("button", { name: "Open Your browser" })
    ).toBeTruthy()
    const runLog = await screen.findByRole("list", { name: "Run log" })
    expect(within(runLog).getByText("Ran python report.py")).toBeTruthy()
    expect(within(runLog).getByText("Your agent")).toBeTruthy()
  })

  it("browses a sandbox's files and reads one", async () => {
    renderLive()
    fireEvent.click(
      await screen.findByRole("button", { name: "Open Shared sandbox" })
    )
    const pane = screen.getByRole("region", { name: "Shared sandbox" })
    fireEvent.click(await within(pane).findByRole("button", { name: "src" }))
    fireEvent.click(
      await within(pane).findByRole("button", { name: /report\.py/ })
    )
    expect(await within(pane).findByText("print('hi')")).toBeTruthy()
    expect(bodyOf("/sandbox/shared/files/read")).toEqual({
      paths: ["src/report.py"],
    })
  })

  it("previews a port the sandbox serves", async () => {
    renderLive()
    fireEvent.click(
      await screen.findByRole("button", { name: "Open Shared sandbox" })
    )
    fireEvent.click(screen.getByRole("tab", { name: "Preview" }))
    fireEvent.change(screen.getByRole("textbox"), { target: { value: "8080" } })
    fireEvent.click(screen.getByRole("button", { name: "Open preview" }))
    const frame = await screen.findByTitle("Preview of port 8080")
    expect(frame.getAttribute("src")).toBe(
      "https://edge-staging.oryxa.in/p/token/"
    )
    expect(bodyOf("/sandbox/shared/preview")).toEqual({ port: 8080 })
  })

  it("takes a browser over when the agent asks, and hands it back", async () => {
    handoff = { active: true, reason: "Sign in to the reseller portal" }
    extraEvents = [
      {
        seq: 2,
        at: now,
        org: "org_1",
        room,
        actor: { type: "agent", id: "agt_0", onBehalfOf: "usr_ada" },
        run: "run_1",
        type: "browser.handoff",
        summary: "Asked for a sign-in",
        data: { child: "br_1" },
        durationMs: 0,
        private: false,
      },
    ]
    renderLive()
    expect(
      await screen.findByRole("button", {
        name: "Open Your browser, needs a hand",
      })
    ).toBeTruthy()
    fireEvent.click(
      screen.getByRole("button", { name: "Open Your browser, needs a hand" })
    )

    expect(
      await screen.findByText("Sign in to the reseller portal")
    ).toBeTruthy()
    expect(
      (await screen.findByTitle("The agent's browser, live")).getAttribute(
        "src"
      )
    ).toBe("https://live.example/tab")
    fireEvent.click(screen.getByRole("button", { name: "Take over" }))
    expect(
      (
        await screen.findByTitle("The browser, under your control")
      ).getAttribute("src")
    ).toBe("https://live.example/full")

    fireEvent.change(
      screen.getByRole("textbox", { name: "A note for the agent" }),
      {
        target: { value: "Signed in as ops@" },
      }
    )
    fireEvent.click(screen.getByRole("button", { name: "Hand back" }))
    await waitFor(() =>
      expect(bodyOf("/browser/br_1/resume")).toEqual({
        success: true,
        note: "Signed in as ops@",
      })
    )
    await waitFor(() =>
      expect(screen.queryByRole("button", { name: "Take over" })).toBeNull()
    )
  })

  it("asks no browser anything until its pane opens, and then only once", async () => {
    renderLive()
    await screen.findByRole("button", { name: "Open Your browser" })
    await new Promise((resolve) => setTimeout(resolve, 50))
    expect(calls("/browser/br_1/handoff")).toHaveLength(0)
    expect(calls("/browser/br_1/live", "POST")).toHaveLength(0)

    fireEvent.click(screen.getByRole("button", { name: "Open Your browser" }))
    await screen.findByTitle("The agent's browser, live")
    await new Promise((resolve) => setTimeout(resolve, 50))
    expect(calls("/browser/br_1/handoff")).toHaveLength(1)
    expect(calls("/browser/br_1/live", "POST")).toHaveLength(1)
  })

  it("never wakes a stopped browser", async () => {
    fetchMock.mockImplementation(async (url) => {
      const target = String(url)
      if (target.endsWith(`${base}/children`)) {
        return json({ children: [{ ...browser, status: "stopped" }] })
      }
      if (target.includes(`${base}/log`))
        return json({ events: [], next: 0, more: false })
      return json({ url: "https://live.example/tab", expiresAt: now })
    })
    renderLive()
    fireEvent.click(
      await screen.findByRole("button", { name: "Open Your browser" })
    )
    expect(await screen.findByText(/This browser has stopped/)).toBeTruthy()
    expect(calls("/browser/br_1/live", "POST")).toHaveLength(0)
    expect(calls("/browser/br_1/handoff")).toHaveLength(0)
  })

  it("gives a shell only in the member's own private sandbox", async () => {
    fetchMock.mockImplementation(async (url) => {
      const target = String(url)
      if (target.endsWith(`${base}/children`)) {
        return json({
          children: [sandbox, { ...sandbox, name: "private-usr_ada" }],
        })
      }
      if (target.includes(`${base}/log`))
        return json({ events: [], next: 0, more: false })
      return json({ path: "", entries: [] })
    })
    renderLive()
    fireEvent.click(
      await screen.findByRole("button", { name: "Open Shared sandbox" })
    )
    expect(screen.queryByRole("tab", { name: "Terminal" })).toBeNull()
    fireEvent.click(screen.getByRole("button", { name: "Back" }))
    fireEvent.click(
      await screen.findByRole("button", { name: "Open Your private sandbox" })
    )
    expect(screen.getByRole("tab", { name: "Terminal" })).toBeTruthy()
  })

  it("frames a preview without its own origin", async () => {
    renderLive()
    fireEvent.click(
      await screen.findByRole("button", { name: "Open Shared sandbox" })
    )
    fireEvent.click(screen.getByRole("tab", { name: "Preview" }))
    fireEvent.click(screen.getByRole("button", { name: "Open preview" }))
    const frame = await screen.findByTitle("Preview of port 3000")
    expect(frame.getAttribute("sandbox")).toBe(
      "allow-scripts allow-forms allow-popups"
    )
  })
})
