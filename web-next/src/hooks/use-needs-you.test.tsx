// @vitest-environment jsdom

import { cleanup, renderHook, waitFor } from "@testing-library/react"
import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import type { ReactNode } from "react"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"
import {
  INBOX_LIMIT,
  needsYouBadge,
  useInbox,
  useNeedsYou,
  useNeedsYouCount,
  waitingRequests,
} from "./use-needs-you"
import {
  ASKER,
  NOW,
  ORG,
  OWNER,
  agentRequest,
  approvedRequest,
  slackRequest,
} from "@/components/request-card-fixtures"
import type { AgentRequest } from "@/lib/agents/agent-requests"

const path = `/v1/organizations/${ORG}`
const json = (body: unknown, status = 200) =>
  new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  })
const later = new Date(NOW + 3600_000).toISOString()

describe("waitingRequests: the one rule for what waits on a member", () => {
  const pending = slackRequest({ expires_at: later })
  it.each<[string, AgentRequest, boolean]>([
    ["pending for its owner", pending, true],
    [
      "pending for its owner, whoever was set to approve it",
      { ...pending, id: "agent", approval: "agent" },
      true,
    ],
    [
      "an answer held for its owner's review",
      { ...pending, id: "review", status: "review" },
      true,
    ],
    [
      "an answer held for review, even past its time",
      {
        ...pending,
        id: "review-late",
        status: "review",
        expires_at: new Date(NOW - 1).toISOString(),
      },
      true,
    ],
    [
      "approved: the agent is working, not the owner",
      approvedRequest("summary", { expires_at: later }),
      false,
    ],
    [
      "pending but past its time",
      { ...pending, id: "late", expires_at: new Date(NOW - 1).toISOString() },
      false,
    ],
    [
      "addressed to someone else",
      {
        ...pending,
        id: "theirs",
        target: { ...pending.target, user_id: ASKER },
        requester: { ...pending.requester, user_id: "user-other" },
      },
      false,
    ],
    ["already answered", { ...pending, status: "completed" }, false],
  ])("%s → %s", (_, request, waits) => {
    expect(waitingRequests([request], OWNER, NOW)).toHaveLength(waits ? 1 : 0)
  })

  it("never waits on the member who asked", () => {
    expect(waitingRequests([pending], ASKER, NOW)).toEqual([])
  })
})

const fetchMock = vi.fn<typeof fetch>()
let confirmations: Response | (() => Response)
let inbox: AgentRequest[]

beforeEach(() => {
  vi.useFakeTimers({ toFake: ["Date"] })
  vi.setSystemTime(NOW)
  vi.stubGlobal("fetch", fetchMock)
  inbox = []
  confirmations = json({ threads: [], total: 0 })
  fetchMock.mockImplementation(async (input) => {
    const url = new URL(String(input), "http://localhost")
    if (url.pathname === `${path}/agent-requests/inbox`)
      return json({ requests: inbox })
    if (url.pathname === `${path}/pending-confirmations`)
      return typeof confirmations === "function"
        ? confirmations()
        : confirmations.clone()
    return json({ error: { code: "not_found", message: "No route" } }, 404)
  })
})

afterEach(() => {
  cleanup()
  vi.useRealTimers()
  fetchMock.mockReset()
  vi.unstubAllGlobals()
})

function wrapper() {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })
  return ({ children }: { children: ReactNode }) => (
    <QueryClientProvider client={client}>{children}</QueryClientProvider>
  )
}

const reads = (route: string) =>
  fetchMock.mock.calls.filter(
    ([input]) =>
      new URL(String(input), "http://localhost").pathname === `${path}/${route}`
  )

describe("useNeedsYou", () => {
  it("counts waiting requests and chats with a tool waiting, once both have answered", async () => {
    inbox = [
      agentRequest({ id: "a", expires_at: later }),
      agentRequest({ id: "b", status: "review", expires_at: later }),
      approvedRequest("summary", { expires_at: later }),
    ]
    confirmations = json({
      threads: [
        {
          id: "chat-1",
          title: "Delete plan",
          updated_at: new Date(NOW).toISOString(),
          pending_confirmations: 2,
        },
      ],
      total: 2,
    })
    const { result } = renderHook(() => useNeedsYou(ORG, OWNER), {
      wrapper: wrapper(),
    })
    expect(result.current.count).toBeUndefined()
    await waitFor(() => expect(result.current.count).toBe(3))
    expect(result.current.requests?.map((request) => request.id)).toEqual([
      "a",
      "b",
    ])
    expect(result.current.chats?.map((chat) => chat.id)).toEqual(["chat-1"])
    expect(needsYouBadge(result.current)).toBe("3")
  })

  it("reads a server without the confirmations route as none waiting, not unknown", async () => {
    inbox = [agentRequest({ expires_at: later })]
    confirmations = json({ error: { code: "not_found", message: "" } }, 404)
    const { result } = renderHook(() => useNeedsYouCount(ORG, OWNER), {
      wrapper: wrapper(),
    })
    await waitFor(() => expect(result.current).toBe(1))
  })

  it("never counts what a refused confirmations read leaves unknown", async () => {
    confirmations = json({ error: { code: "forbidden", message: "" } }, 403)
    const { result } = renderHook(() => useNeedsYou(ORG, OWNER), {
      wrapper: wrapper(),
    })
    await waitFor(() => expect(result.current.confirmations.isError).toBe(true))
    expect(result.current.requests).toEqual([])
    expect(result.current.count).toBeUndefined()
  })

  it("says a full inbox may hold more", async () => {
    inbox = Array.from({ length: INBOX_LIMIT }, (_, i) =>
      agentRequest({ id: `r${i}`, expires_at: later })
    )
    const { result } = renderHook(() => useNeedsYou(ORG, OWNER), {
      wrapper: wrapper(),
    })
    await waitFor(() => expect(result.current.count).toBe(INBOX_LIMIT))
    expect(needsYouBadge(result.current)).toBe(`${INBOX_LIMIT}+`)
  })

  it("shares one inbox read, at one limit, between every page that counts it", async () => {
    const shared = wrapper()
    renderHook(
      () => {
        useNeedsYouCount(ORG, OWNER)
        useNeedsYou(ORG, OWNER)
        useInbox(ORG, OWNER)
      },
      { wrapper: shared }
    )
    await waitFor(() => expect(reads("agent-requests/inbox")).toHaveLength(1))
    await new Promise((resolve) => setTimeout(resolve, 20))
    expect(reads("agent-requests/inbox")).toHaveLength(1)
    expect(reads("pending-confirmations")).toHaveLength(1)
    const [input] = reads("agent-requests/inbox")[0]
    expect(
      new URL(String(input), "http://localhost").searchParams.get("limit")
    ).toBe(String(INBOX_LIMIT))
  })
})
