// @vitest-environment jsdom

import { afterEach, describe, expect, it, vi } from "vitest"
import {
  cleanup,
  fireEvent,
  render,
  screen,
  within,
} from "@testing-library/react"
import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import { ChatsList, chatListGroups } from "./chats-page"
import { AgentChatMenu } from "./agent-chat-menu"
import { threadListQueryOptions } from "@/lib/threads/client"
import type { Thread } from "@/lib/threads/client"
import type { PrimaryChatTarget } from "@/lib/threads/chat-choice"

afterEach(cleanup)

const now = new Date("2026-10-15T12:00:00Z")
const thread = (id: string, title: string, updated_at: string): Thread => ({
  id,
  title,
  updated_at,
})
const threads = [
  thread("th_1", "Plan the EU launch", "2026-10-15T11:54:00Z"),
  thread("th_2", "Summarise the vendor contracts", "2026-10-15T09:00:00Z"),
  thread("th_3", "Compare the hosting quotes", "2026-10-14T15:00:00Z"),
  thread("th_4", "", "2026-10-15T08:00:00Z"),
  {
    ...thread("th_5", "Updates from your apps", "2026-10-15T10:00:00Z"),
    origin: "wakeup",
  },
]

describe("chatListGroups", () => {
  it("groups the member's chats into Today and Earlier, newest first", () => {
    const groups = chatListGroups(threads, {
      query: "",
      waiting: new Set(),
      now,
    })
    expect(groups.map((group) => group.label)).toEqual(["Today", "Earlier"])
    expect(groups[0].threads.map((item) => item.id)).toEqual(["th_1", "th_2"])
    expect(groups[1].threads.map((item) => item.id)).toEqual(["th_3"])
  })

  it("keeps an untitled chat only while it is open or waits on the member", () => {
    const open = chatListGroups(threads, {
      query: "",
      selected: "th_4",
      waiting: new Set(),
      now,
    })
    expect(open[0].threads.map((item) => item.id)).toContain("th_4")
  })

  it("filters by title", () => {
    const groups = chatListGroups(threads, {
      query: "hosting",
      waiting: new Set(),
      now,
    })
    expect(groups).toEqual([{ label: "Earlier", threads: [threads[2]] }])
  })
})

function renderList({
  busy = false,
  onOpen = vi.fn<(target: PrimaryChatTarget) => void>(),
  waitingThread,
}: {
  busy?: boolean
  onOpen?: (target: PrimaryChatTarget) => void
  waitingThread?: string
} = {}) {
  const client = new QueryClient({
    defaultOptions: { queries: { staleTime: Infinity, retry: false } },
  })
  client.setQueryData(threadListQueryOptions("org_1", "u_1").queryKey, threads)
  render(
    <QueryClientProvider client={client}>
      <ChatsList
        organizationId="org_1"
        viewerId="u_1"
        agentName="Krude"
        selected="th_1"
        waitingThread={waitingThread}
        busy={busy}
        onOpen={onOpen}
      />
    </QueryClientProvider>
  )
  return onOpen
}

describe("ChatsList", () => {
  it("raises the open chat and opens another through the chat's queue", () => {
    const onOpen = renderList()
    const list = screen.getByRole("region", { name: "Chats" })
    const open = within(list).getByRole("button", {
      name: /Plan the EU launch/,
    })
    expect(open.getAttribute("aria-current")).toBe("true")
    fireEvent.click(
      within(list).getByRole("button", { name: /Compare the hosting quotes/ })
    )
    expect(onOpen).toHaveBeenCalledWith({ kind: "thread", threadId: "th_3" })
    fireEvent.click(within(list).getByRole("button", { name: "New chat" }))
    expect(onOpen).toHaveBeenCalledWith({ kind: "new" })
  })

  it("marks the open chat that waits on the member", () => {
    renderList({ waitingThread: "th_1" })
    expect(screen.getByText("Needs approval")).toBeTruthy()
  })

  it("holds the chat while the agent works", () => {
    renderList({ busy: true })
    expect(
      screen
        .getByRole("button", { name: /Compare the hosting quotes/ })
        .hasAttribute("disabled")
    ).toBe(true)
    expect(
      screen.getByText(
        "Your agent is working. You can switch chats once it finishes."
      )
    ).toBeTruthy()
  })

  it("searches by title", () => {
    renderList()
    fireEvent.change(screen.getByRole("searchbox", { name: "Search chats" }), {
      target: { value: "nothing like this" },
    })
    expect(screen.getByText("No chat has that in its title.")).toBeTruthy()
  })
})

describe("AgentChatMenu", () => {
  it("deletes the chat after a confirm, Cancel then Delete", async () => {
    const onDelete = vi.fn(() => Promise.resolve())
    render(<AgentChatMenu title="Plan" disabled={false} onDelete={onDelete} />)
    fireEvent.click(screen.getByRole("button", { name: "Chat options" }))
    fireEvent.click(
      await screen.findByRole("menuitem", { name: "Delete chat" })
    )
    const dialog = await screen.findByRole("dialog")
    const buttons = within(dialog).getAllByRole("button")
    expect(buttons.map((button) => button.textContent)).toEqual([
      "Cancel",
      "Delete",
    ])
    fireEvent.click(within(dialog).getByRole("button", { name: "Delete" }))
    expect(onDelete).toHaveBeenCalledOnce()
  })
})
