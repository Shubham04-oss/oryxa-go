// @vitest-environment jsdom

// A request named in a direct message opens in Requests: the shell gives what
// shows a DM (src/lib/dms/request-navigation.ts) the function that does it. A
// DM's own view is a probe here, which asks for that function the way the real
// one does, so this tests the wiring and nothing of the DM.
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"
import {
  cleanup,
  fireEvent,
  render,
  screen,
  within,
} from "@testing-library/react"
import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import { WorkspaceShell } from "./workspace-shell"
import {
  ORG,
  VIEWER,
  fakeAgentsBackend,
  primary,
  request,
} from "./agents-fake-backend"
import { useOpenRequest } from "@/lib/dms/request-navigation"
import type { Session } from "@/lib/auth/client"
import type * as DirectMessage from "./direct-message"

vi.mock("./direct-message", async (importOriginal) => ({
  ...(await importOriginal<typeof DirectMessage>()),
  DirectMessageView: function DirectMessageProbe() {
    const open = useOpenRequest()
    return open ? (
      <button type="button" onClick={() => open("irq_dm")}>
        Open the request in this DM
      </button>
    ) : (
      <p>This DM cannot open a request.</p>
    )
  },
}))
vi.mock(
  "agents/react",
  async () => (await import("@/test/room-socket")).agentsReact
)
vi.mock(
  "@cloudflare/ai-chat/react",
  async () => (await import("@/test/agent-chat")).aiChatReact
)

const session: Session = {
  user: { id: VIEWER, email: "member@example.com", display_name: "Member" },
  organizations: [{ id: ORG, name: "Simulated Org", slug: "sim" }],
}

let client: QueryClient

beforeEach(() => {
  const backend = fakeAgentsBackend({ agents: [primary], inbox: [] })
  const json = (body: unknown) =>
    new Response(JSON.stringify(body), {
      status: 200,
      headers: { "Content-Type": "application/json" },
    })
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: string, init?: RequestInit) => {
      const path = new URL(input, "http://localhost").pathname
      if (path === `/v1/organizations/${ORG}/members`) {
        return json({
          members: [
            {
              organization_id: ORG,
              user_id: "user-priya",
              email: "priya@example.com",
              display_name: "Priya Shah",
              role: "member",
              joined_at: "2026-09-01T00:00:00Z",
            },
          ],
        })
      }
      if (path === `/v1/organizations/${ORG}/agent-requests/irq_dm`) {
        return json(
          request({ id: "irq_dm", status: "pending", task: "Asked in a DM." })
        )
      }
      return backend.fetch(input, init)
    })
  )
  vi.stubGlobal("matchMedia", () => ({
    matches: false,
    addEventListener: vi.fn(),
    removeEventListener: vi.fn(),
  }))
  client = new QueryClient({ defaultOptions: { queries: { retry: false } } })
})

afterEach(() => {
  cleanup()
  client.clear()
  vi.unstubAllGlobals()
})

describe("a request named in a direct message", () => {
  it("opens in Requests, first, from the DM in the main canvas", async () => {
    render(
      <QueryClientProvider client={client}>
        <WorkspaceShell
          session={session}
          selectedOrganization={session.organizations[0]}
          initialDirectMessage="user-priya"
        />
      </QueryClientProvider>
    )

    // The DM is on screen, and can open a request.
    fireEvent.click(
      await screen.findByRole("button", {
        name: "Open the request in this DM",
      })
    )

    expect(
      await screen.findByRole("heading", { name: "Requests", level: 1 })
    ).toBeTruthy()
    const opened = await screen.findByRole("region", { name: "Opened request" })
    await within(opened).findByText("Asked in a DM.")
    expect(screen.queryByText("This DM cannot open a request.")).toBeNull()
  })
})
