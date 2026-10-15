// @vitest-environment jsdom

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"
import { cleanup, fireEvent, render, screen } from "@testing-library/react"
import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import { CallProvider } from "./call-provider"
import {
  WorkspaceCommandPalette,
  browseAsk,
  matches,
} from "./workspace-command-palette"
import type { CommandPaletteProps } from "./workspace-command-palette"
import { requestListQueryOptions } from "./requests-view-queries"
import { agentsQueryOptions } from "@/lib/agents/queries"
import { filesQueryOptions } from "@/lib/files/client"
import { threadListQueryOptions } from "@/lib/threads/client"
import type { Teamspace } from "@/lib/teamspaces/client"

const ORG = "org_1"
const VIEWER = "user-ada"

const launch = {
  id: "tsp_launch",
  name: "EU launch",
  whisperers: [{ user_id: VIEWER }, { user_id: "user-raya" }],
} as unknown as Teamspace

function seeded(): QueryClient {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false, staleTime: Infinity } },
  })
  client.setQueryData(requestListQueryOptions(ORG, "inbox").queryKey, [
    {
      id: "irq_1",
      task: "What is the plan for the EU launch?",
      purpose: "",
      requester: { user_id: "user-raya" },
      target: { user_id: VIEWER },
      status: "pending",
    },
  ] as never)
  client.setQueryData(threadListQueryOptions(ORG, VIEWER).queryKey, [
    {
      id: "thr_1",
      title: "Plan the EU launch",
      updated_at: new Date().toISOString(),
      origin: "chat",
    },
    { id: "thr_2", title: "", updated_at: new Date().toISOString() },
  ])
  client.setQueryData(filesQueryOptions(ORG).queryKey, [
    { id: "fil_1", filename: "eu-launch-plan.md" },
  ] as never)
  client.setQueryData(agentsQueryOptions(ORG).queryKey, [
    { id: "agt_1", name: "Krude", kind: "primary" },
  ] as never)
  return client
}

function renderPalette(overrides: Partial<CommandPaletteProps> = {}) {
  const props: CommandPaletteProps = {
    open: true,
    onOpenChange: vi.fn(),
    organizationId: ORG,
    viewerId: VIEWER,
    viewerName: "Ada",
    teamspaces: [launch],
    nameOf: (id) => (id === "user-raya" ? "Raya Patel" : undefined),
    onOpenTeamspace: vi.fn(),
    onOpenRequest: vi.fn(),
    onOpenChat: vi.fn(),
    onAsk: vi.fn(),
    ...overrides,
  }
  render(
    <QueryClientProvider client={seeded()}>
      <CallProvider>
        <WorkspaceCommandPalette {...props} />
      </CallProvider>
    </QueryClientProvider>
  )
  return props
}

beforeEach(() => {
  // cmdk measures its list and scrolls the chosen option into view.
  vi.stubGlobal(
    "ResizeObserver",
    class {
      observe() {}
      unobserve() {}
      disconnect() {}
    }
  )
  Element.prototype.scrollIntoView = vi.fn()
  // Nothing here may reach a server: every list is already loaded.
  vi.stubGlobal(
    "fetch",
    vi.fn(() => new Promise(() => undefined))
  )
})
afterEach(() => {
  cleanup()
  vi.unstubAllGlobals()
})

describe("matches", () => {
  it("needs every word, in any case and order", () => {
    expect(matches("Plan the EU launch", "launch eu")).toBe(true)
    expect(matches("Plan the EU launch", "sales")).toBe(false)
    expect(matches("anything", "")).toBe(true)
  })
})

describe("browseAsk", () => {
  it("asks the agent to open the page and show it", () => {
    expect(browseAsk(" notion pricing ")).toBe(
      "Open notion pricing in your browser and show me what's there."
    )
  })
})

describe("WorkspaceCommandPalette", () => {
  it("goes to what matches, filtered from the loaded lists", () => {
    const props = renderPalette()
    fireEvent.change(screen.getByRole("combobox"), { target: { value: "eu" } })

    expect(
      screen.getByRole("option", { name: /EU launch.*Teamspace · 2 people/ })
    ).toBeTruthy()
    expect(
      screen.getByRole("option", {
        name: /What is the plan for the EU launch\?.*Request from Raya/,
      })
    ).toBeTruthy()
    expect(
      screen.getByRole("option", {
        name: /Plan the EU launch.*Chat with Krude/,
      })
    ).toBeTruthy()
    expect(
      screen.getByRole("option", { name: /eu-launch-plan\.md/ })
    ).toBeTruthy()

    fireEvent.click(
      screen.getByRole("option", { name: /Plan the EU launch.*Chat/ })
    )
    expect(props.onOpenChat).toHaveBeenCalledWith("thr_1")
    expect(props.onOpenChange).toHaveBeenCalledWith(false)
  })

  it("offers to start a parley, ask the agent, or have it open a page", () => {
    const props = renderPalette()
    const input = screen.getByRole("combobox")
    fireEvent.change(input, { target: { value: "eu" } })

    expect(
      screen.getByRole("option", { name: /Start a parley in EU launch/ })
    ).toBeTruthy()
    fireEvent.click(
      screen.getByRole("option", { name: /Have Krude open a page/ })
    )
    expect(props.onAsk).toHaveBeenCalledWith(browseAsk("eu"))

    fireEvent.keyDown(input, { key: "Enter", metaKey: true })
    expect(props.onAsk).toHaveBeenLastCalledWith("eu")
  })

  it("lists nothing and does nothing without a workspace", () => {
    renderPalette({
      organizationId: undefined,
      viewerId: undefined,
      teamspaces: [],
    })
    fireEvent.change(screen.getByRole("combobox"), { target: { value: "eu" } })
    expect(
      screen.getByText("Sign in to a workspace to search it.")
    ).toBeTruthy()
    expect(screen.queryByRole("option", { name: /Ask/ })).toBeNull()
  })
})
