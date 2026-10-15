// @vitest-environment jsdom

import { afterEach, describe, expect, it, vi } from "vitest"
import { cleanup, fireEvent, render, screen } from "@testing-library/react"
import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import { useState } from "react"
import { SessionWorkTabs } from "./agent-session-work"
import type { SessionWorkTab } from "./agent-session-work"
import { threadKeys } from "@/lib/threads/client"
import type { ThreadDetail } from "@/lib/threads/client"

vi.mock("./live-work", () => ({
  useRoomChildren: () => ({
    children: { isPending: false, isError: false, data: [] },
  }),
  LiveChildren: () => null,
}))
vi.mock("./agent-timeline", () => ({
  AgentRequestCard: ({ entry }: { entry: { requestId: string } }) => (
    <div>Request {entry.requestId}</div>
  ),
}))

afterEach(cleanup)

const detail: ThreadDetail = {
  id: "th_1",
  title: "Plan",
  updated_at: "2026-10-15T09:00:00Z",
  pending_confirmations: [],
  items: [
    { type: "message", id: "ev_1", author: "member", text: "Plan it" },
    { type: "artifact", id: "ev_2", artifact: "plan.docx", version: 1 },
    { type: "artifact", id: "ev_3", artifact: "plan.docx", version: 2 },
    { type: "request", id: "ev_4", request_id: "rq_1", status: "pending" },
  ],
}

function Harness({
  threadId,
  onOpenFile = vi.fn(),
}: {
  threadId: string | null
  onOpenFile?: () => void
}) {
  const [tab, setTab] = useState<SessionWorkTab>("overview")
  return (
    <SessionWorkTabs
      organizationId="org_1"
      threadId={threadId}
      tab={tab}
      onTabChange={setTab}
      overview={<p>Agent overview body</p>}
      onOpenFile={onOpenFile}
      onOpenLive={vi.fn()}
    />
  )
}

function renderTabs(threadId: string | null, onOpenFile = vi.fn()) {
  const client = new QueryClient({
    defaultOptions: { queries: { staleTime: Infinity, retry: false } },
  })
  client.setQueryData(threadKeys.detail("org_1", "th_1"), detail)
  render(
    <QueryClientProvider client={client}>
      <Harness threadId={threadId} onOpenFile={onOpenFile} />
    </QueryClientProvider>
  )
  return onOpenFile
}

describe("the session's work tabs", () => {
  it("opens on the overview, with the conversation's counts on its tabs", () => {
    renderTabs("th_1")
    expect(screen.getByText("Agent overview body")).toBeTruthy()
    expect(screen.getByRole("tab", { name: "Files1" })).toBeTruthy()
    expect(screen.getByRole("tab", { name: "Requests1" })).toBeTruthy()
    expect(screen.getByRole("tab", { name: "Live" })).toBeTruthy()
  })

  it("lists the conversation's files, latest version, and opens one", () => {
    const onOpenFile = renderTabs("th_1")
    fireEvent.click(screen.getByRole("tab", { name: "Files1" }))
    const file = screen.getByRole("button", { name: "plan.docx" })
    expect(file.textContent).toContain("Version 2 of 2")
    fireEvent.click(file)
    expect(onOpenFile).toHaveBeenCalledWith({
      threadId: "th_1",
      name: "plan.docx",
      version: 2,
    })
  })

  it("lists the requests the agent filed in this conversation", () => {
    renderTabs("th_1")
    fireEvent.click(screen.getByRole("tab", { name: "Requests1" }))
    expect(screen.getByText("Request rq_1")).toBeTruthy()
  })

  it("says plainly what a new chat has", () => {
    renderTabs(null)
    fireEvent.click(screen.getByRole("tab", { name: "Live" }))
    expect(
      screen.getByText(
        "Start a conversation to see its sandbox and browser here."
      )
    ).toBeTruthy()
    fireEvent.click(screen.getByRole("tab", { name: "Requests" }))
    expect(
      screen.getByText(
        "Start a conversation to see the requests it files here."
      )
    ).toBeTruthy()
  })
})
