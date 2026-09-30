// @vitest-environment jsdom

import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import {
  act,
  cleanup,
  fireEvent,
  render,
  screen,
  waitFor,
} from "@testing-library/react"
import { afterEach, describe, expect, it, vi } from "vitest"
import { getAgentRequest } from "@/lib/agents/agent-requests"
import type { AgentRequest } from "@/lib/agents/agent-requests"
import type * as AgentRequestsClient from "@/lib/agents/agent-requests"
import { agentKeys } from "@/lib/agents/queries"
import { ApiError } from "@/lib/api/client"
import { buildTimeline } from "@/lib/threads/timeline"
import { AgentRequestCard, AgentTimeline } from "./agent-timeline"

vi.mock("@/lib/agents/agent-requests", async (original) => ({
  ...(await original<typeof AgentRequestsClient>()),
  getAgentRequest: vi.fn(),
}))
vi.mock("thinking-orbs", () => ({ ThinkingOrb: () => null }))

const read = vi.mocked(getAgentRequest)
const clients: QueryClient[] = []
afterEach(() => {
  cleanup()
  for (const client of clients.splice(0)) client.clear()
  vi.useRealTimers()
  vi.resetAllMocks()
})

function request(status: AgentRequest["status"]): AgentRequest {
  return {
    id: "irq_1",
    organization_id: "org_1",
    requester: { user_id: "asker", agent_id: "leo", agent_name: "Leo" },
    target: { user_id: "owner", agent_id: "nia", agent_name: "Nia" },
    purpose: "Launch",
    task: "When is launch?",
    context: [],
    requested_tools: [],
    requested_permissions: [],
    status,
    revision: 1,
    expires_at: "2026-10-01T00:00:00Z",
    created_at: "2026-09-30T00:00:00Z",
    updated_at: "2026-09-30T00:00:00Z",
  }
}

function setup(
  children = (
    <AgentRequestCard
      organizationId="org_1"
      entry={{
        kind: "request",
        key: "request:irq_1",
        requestId: "irq_1",
        status: "asked",
      }}
    />
  )
) {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false, gcTime: Infinity } },
  })
  clients.push(client)
  render(<QueryClientProvider client={client}>{children}</QueryClientProvider>)
  return client
}

describe("the asker's request card", () => {
  it("renders one live card for duplicate request timeline items", async () => {
    read.mockResolvedValue(request("pending"))
    setup(
      <AgentTimeline
        entries={buildTimeline([
          {
            type: "request",
            id: "create",
            request_id: "irq_1",
            status: "pending",
          },
          { type: "request", id: "note", request_id: "irq_1", status: "asked" },
        ])}
        running={false}
        actionable={new Set()}
        submitted={{}}
        answering={false}
        onAnswer={vi.fn()}
        organizationId="org_1"
      />
    )
    expect(await screen.findByRole("group", { name: "Asked Nia" })).toBeTruthy()
    expect(screen.getAllByRole("group")).toHaveLength(1)
    expect(screen.getByText("When is launch?")).toBeTruthy()
    expect(screen.getByText("Waiting for approval")).toBeTruthy()
    expect(read).toHaveBeenCalledTimes(1)
  })

  it("polls review into the approved answer, then stops", async () => {
    vi.useFakeTimers()
    const held = request("review")
    held.held = {
      result: {
        status: "completed",
        summary: "Private draft",
        content: "private@example.com",
        tools_used: [],
        artifacts: [],
        completed_at: "now",
      },
    }
    const answered = request("completed")
    answered.result = {
      status: "completed",
      summary: "Launches **14 October**",
      content: "Project Falcon",
      tools_used: [],
      artifacts: [],
      completed_at: "now",
    }
    read.mockResolvedValueOnce(held).mockResolvedValue(answered)
    setup()
    await act(async () => {
      await vi.advanceTimersByTimeAsync(1)
    })
    expect(
      screen.getByText("Waiting for the answer to be reviewed")
    ).toBeTruthy()
    expect(screen.queryByText("Private draft")).toBeNull()
    expect(screen.queryByText("private@example.com")).toBeNull()
    await act(async () => {
      await vi.advanceTimersByTimeAsync(10_010)
    })
    expect(screen.getByText("Answered")).toBeTruthy()
    expect(screen.getByText("14 October").tagName).toBe("STRONG")
    expect(screen.getByText("Project Falcon")).toBeTruthy()
    expect(read).toHaveBeenCalledTimes(2)
    await act(async () => {
      await vi.advanceTimersByTimeAsync(30_000)
    })
    expect(read).toHaveBeenCalledTimes(2)
  })

  it("keeps a completed pointer readable when the answer read fails, with retry", async () => {
    const answered = request("completed")
    answered.result = {
      status: "completed",
      summary: "14 October",
      content: "",
      tools_used: [],
      artifacts: [],
      completed_at: "now",
    }
    read.mockRejectedValueOnce(new Error("offline")).mockResolvedValue(answered)
    setup(
      <AgentRequestCard
        organizationId="org_1"
        entry={{
          kind: "request",
          key: "request:irq_1",
          requestId: "irq_1",
          status: "answered",
        }}
      />
    )
    expect(
      await screen.findByRole("button", { name: "Try again" })
    ).toBeTruthy()
    expect(screen.getByText("Answered")).toBeTruthy()
    fireEvent.click(screen.getByRole("button", { name: "Try again" }))
    expect(await screen.findByText("14 October")).toBeTruthy()
    expect(screen.queryByText("Couldn't load the latest request.")).toBeNull()
  })

  it("hides cached content if Go refuses access", async () => {
    const answered = request("completed")
    answered.result = {
      status: "completed",
      summary: "Published answer",
      content: "",
      tools_used: [],
      artifacts: [],
      completed_at: "now",
    }
    read
      .mockResolvedValueOnce(answered)
      .mockRejectedValue(new ApiError("not yours", 403, "forbidden"))
    const client = setup()
    expect(await screen.findByText("Published answer")).toBeTruthy()
    await act(async () => {
      await client.invalidateQueries({
        queryKey: agentKeys.request("org_1", "irq_1"),
      })
    })
    await waitFor(() => expect(screen.queryByRole("group")).toBeNull())
    expect(screen.queryByText("Published answer")).toBeNull()
  })

  it.each(["withheld", "failed", "expired", "rejected"] as const)(
    "shows %s clearly and never renders a held draft",
    async (status) => {
      const value = request(status)
      value.held = {
        result: {
          status: "completed",
          summary: "Private draft",
          content: "Secret",
          tools_used: [],
          artifacts: [],
          completed_at: "now",
        },
      }
      if (status === "withheld") value.result = value.held.result
      if (status === "rejected")
        value.decision = {
          decision: "rejected",
          message: "I cannot share that.",
          allowed_tools: [],
          granted_permissions: [],
          decided_at: "now",
        }
      read.mockResolvedValue(value)
      setup()
      expect(
        await screen.findByRole("group", { name: "Asked Nia" })
      ).toBeTruthy()
      expect(
        screen.getByText(
          {
            withheld: "Not shared",
            failed: "Failed",
            expired: "Expired",
            rejected: "Declined",
          }[status]
        )
      ).toBeTruthy()
      expect(screen.queryByText("Private draft")).toBeNull()
      expect(screen.queryByText("Secret")).toBeNull()
      if (status === "rejected")
        expect(screen.getByText("I cannot share that.")).toBeTruthy()
    }
  )
})
