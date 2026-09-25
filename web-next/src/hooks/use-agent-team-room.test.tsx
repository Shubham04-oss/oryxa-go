// @vitest-environment jsdom

import { afterEach, describe, expect, it, vi } from "vitest"
import { act, cleanup, renderHook, waitFor } from "@testing-library/react"
import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import type { ReactNode } from "react"
import { useAgentTeamRoom } from "./use-agent-team-room"
import {
  ORG,
  agent,
  fakeAgentsBackend,
  primary,
  team,
} from "@/components/agents-fake-backend"
import type { AgentTeamMessage } from "@/lib/agents/agent-teams"

const scout = agent({ id: "agt_scout", name: "Scout" })
const crew = team({ id: "atm_crew", name: "Pipeline crew" }, [
  { agent: scout, role: "Research" },
])
const history: AgentTeamMessage[] = [
  {
    id: "msg_a",
    team_id: crew.id,
    seq: 1,
    kind: "member",
    author_name: "You",
    body: "Where are we?",
    created_at: "2026-09-20T09:00:00Z",
  },
  {
    id: "msg_b",
    team_id: crew.id,
    seq: 2,
    kind: "agent",
    author_name: "Leo",
    author_agent_id: primary.id,
    body: "Two deals are open.",
    in_reply_to_seq: 1,
    created_at: "2026-09-20T09:00:05Z",
  },
]

function setup(options: { messages?: AgentTeamMessage[] } = {}) {
  const backend = fakeAgentsBackend({
    agents: [primary, scout],
    teams: [crew],
    messages: { [crew.id]: options.messages ?? history },
  })
  vi.stubGlobal("fetch", vi.fn(backend.fetch))
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })
  const wrapper = ({ children }: { children: ReactNode }) => (
    <QueryClientProvider client={client}>{children}</QueryClientProvider>
  )
  return { backend, client, wrapper }
}

afterEach(() => {
  cleanup()
  vi.useRealTimers()
  vi.unstubAllGlobals()
})

describe("useAgentTeamRoom", () => {
  it("reads the team's conversation, oldest first", async () => {
    const { backend, wrapper } = setup()
    const { result } = renderHook(() => useAgentTeamRoom(ORG, crew.id), {
      wrapper,
    })

    expect(result.current.loading).toBe(true)
    await waitFor(() => expect(result.current.loading).toBe(false))
    expect(result.current.messages.map((message) => message.body)).toEqual([
      "Where are we?",
      "Two deals are open.",
    ])
    expect(result.current.readError).toBeUndefined()
    expect(backend.calls[0]).toMatchObject({
      method: "GET",
      path: `/v1/organizations/${ORG}/agent-teams/${crew.id}/messages`,
    })
  })

  it("shows a line as sending until the team answers, then merges the replies", async () => {
    const { backend, wrapper } = setup()
    const { result } = renderHook(() => useAgentTeamRoom(ORG, crew.id), {
      wrapper,
    })
    await waitFor(() => expect(result.current.loading).toBe(false))
    const release = backend.holdPosts()

    let sent: Promise<boolean> | undefined
    act(() => {
      sent = result.current.send("Draft the plan")
    })
    await waitFor(() => expect(result.current.sending).toBe("Draft the plan"))
    // Nothing is added to the conversation before a read shows it.
    expect(result.current.messages).toHaveLength(2)
    expect(result.current.sendingStored).toBe(false)
    expect(backend.writes()).toEqual([
      {
        method: "POST",
        path: `/v1/organizations/${ORG}/agent-teams/${crew.id}/messages`,
        body: { body: "Draft the plan" },
      },
    ])

    // The room stores the member's line before the agents answer, so the
    // next read (a poll) shows it while the post is still open.
    act(() => result.current.reload())
    await waitFor(() => expect(result.current.sendingStored).toBe(true))
    expect(result.current.sending).toBe("Draft the plan")

    release()
    await act(async () => {
      expect(await sent).toBe(true)
    })
    await waitFor(() => expect(result.current.sending).toBeUndefined())
    expect(result.current.messages.map((message) => message.body)).toEqual([
      "Where are we?",
      "Two deals are open.",
      "Draft the plan",
      "On it: **Draft the plan**",
    ])
    expect(result.current.sendError).toBeUndefined()
  })

  it("keeps showing a post in flight to a room opened again", async () => {
    const { backend, wrapper } = setup()
    const first = renderHook(() => useAgentTeamRoom(ORG, crew.id), { wrapper })
    await waitFor(() => expect(first.result.current.loading).toBe(false))
    const release = backend.holdPosts()
    act(() => {
      void first.result.current.send("Keep going")
    })
    await waitFor(() => expect(first.result.current.sending).toBe("Keep going"))
    first.unmount()

    const second = renderHook(() => useAgentTeamRoom(ORG, crew.id), {
      wrapper,
    })
    expect(second.result.current.sending).toBe("Keep going")
    release()
    await waitFor(() => expect(second.result.current.sending).toBeUndefined())
    await waitFor(() =>
      expect(second.result.current.messages.at(-1)?.body).toBe(
        "On it: **Keep going**"
      )
    )
  })

  it("hands back a refusal as copy, and forgets it on request", async () => {
    const { backend, wrapper } = setup()
    backend.failNext("POST", /\/messages$/, {
      status: 409,
      code: "team_disabled",
      message: "agent team is disabled",
    })
    const { result } = renderHook(() => useAgentTeamRoom(ORG, crew.id), {
      wrapper,
    })
    await waitFor(() => expect(result.current.loading).toBe(false))

    let sent = true
    await act(async () => {
      sent = await result.current.send("Anyone there?")
    })
    expect(sent).toBe(false)
    await waitFor(() =>
      expect(result.current.sendError).toBe(
        "This team is paused. Resume it to send messages."
      )
    )
    expect(result.current.sending).toBeUndefined()
    act(() => result.current.clearSendError())
    await waitFor(() => expect(result.current.sendError).toBeUndefined())
  })

  it("says when the conversation cannot be read", async () => {
    const { backend, wrapper } = setup()
    backend.unavailable("agent-teams")
    const { result } = renderHook(() => useAgentTeamRoom(ORG, crew.id), {
      wrapper,
    })

    await waitFor(() =>
      expect(result.current.readError).toBe(
        "Team conversations aren't set up on this server."
      )
    )
    expect(result.current.messages).toEqual([])
  })

  it("re-reads the conversation every 5 seconds while it is open", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true })
    const { backend, wrapper } = setup()
    const { result, unmount } = renderHook(
      () => useAgentTeamRoom(ORG, crew.id),
      { wrapper }
    )
    await waitFor(() => expect(result.current.loading).toBe(false))
    const reads = () =>
      backend.calls.filter(
        (call) => call.method === "GET" && call.path.endsWith("/messages")
      ).length
    expect(reads()).toBe(1)

    await act(() => vi.advanceTimersByTimeAsync(5_000))
    expect(reads()).toBe(2)
    unmount()
    await act(() => vi.advanceTimersByTimeAsync(10_000))
    expect(reads()).toBe(2)
  })
})
