// @vitest-environment jsdom

import { afterEach, describe, expect, it, vi } from "vitest"
import {
  cleanup,
  fireEvent,
  render,
  screen,
  waitFor,
  within,
} from "@testing-library/react"
import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import { ChatsList, chatFiles, chatListGroups } from "./chats-page"
import { AgentChatMenu } from "./agent-chat-menu"
import { ApiError } from "@/lib/api/client"
import { threadListQueryOptions } from "@/lib/threads/client"
import type { Thread } from "@/lib/threads/client"
import {
  clearPrimaryChatRunning,
  setPrimaryChatRunning,
} from "@/lib/threads/chat-choice"
import type { PrimaryChatTarget } from "@/lib/threads/chat-choice"

afterEach(() => {
  cleanup()
  clearPrimaryChatRunning()
})

const now = new Date("2026-10-15T12:00:00Z")
const thread = (id: string, title: string, updated_at: string): Thread => ({
  id,
  title,
  updated_at,
})
const threads = [
  {
    ...thread("th_1", "Plan the EU launch", "2026-10-15T11:54:00Z"),
    artifact_count: 3,
    latest_artifact: { name: "launch-plan.pdf", kind: "pdf" },
  },
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

describe("chatFiles", () => {
  it("counts the files Go kept, with the latest one's name", () => {
    expect(
      chatFiles({
        artifact_count: 3,
        latest_artifact: { name: "q3.xlsx", kind: "xlsx" },
      })
    ).toEqual({ label: "3 files", latest: "q3.xlsx" })
    expect(chatFiles({ artifact_count: 1, latest_artifact: null })).toEqual({
      label: "1 file",
      latest: undefined,
    })
  })

  it("says nothing when Go has no count, or counts none", () => {
    expect(chatFiles({})).toBeNull()
    expect(chatFiles({ artifact_count: 0, latest_artifact: null })).toBeNull()
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
      name: /^Plan the EU launch/,
    })
    expect(open.getAttribute("aria-current")).toBe("true")
    fireEvent.click(
      within(list).getByRole("button", { name: /^Compare the hosting quotes/ })
    )
    expect(onOpen).toHaveBeenCalledWith({ kind: "thread", threadId: "th_3" })
    fireEvent.click(within(list).getByRole("button", { name: "New chat" }))
    expect(onOpen).toHaveBeenCalledWith({ kind: "new" })
  })

  it("shows a row's file count and its latest file's kind", () => {
    renderList()
    const row = screen.getByRole("button", { name: /^Plan the EU launch/ })
    expect(row.textContent).toContain("3 files")
    expect(row.textContent).toContain("PDF")
    expect(
      screen
        .getByRole("button", { name: /^Compare the hosting quotes/ })
        .textContent.includes("file")
    ).toBe(false)
  })

  it("marks the open chat that waits on the member", () => {
    renderList({ waitingThread: "th_1" })
    expect(screen.getByText("Needs approval")).toBeTruthy()
  })

  it("lets the member switch chats while one works, and marks the working one (C9, F10)", () => {
    setPrimaryChatRunning(
      { organizationId: "org_1", userId: "u_1" },
      "th_1",
      true
    )
    const onOpen = renderList({ busy: true })
    const other = screen.getByRole("button", {
      name: /^Compare the hosting quotes/,
    })
    expect(other.hasAttribute("disabled")).toBe(false)
    fireEvent.click(other)
    expect(onOpen).toHaveBeenCalledWith({ kind: "thread", threadId: "th_3" })
    expect(
      screen.getByRole("button", { name: "New chat" }).hasAttribute("disabled")
    ).toBe(false)
    // The running chat carries the dot; the others don't.
    expect(screen.getAllByRole("img", { name: "Working" })).toHaveLength(1)
    expect(screen.queryByText(/switch chats once it finishes/)).toBeNull()
  })

  it("renames and deletes a chat from its row, and leaves the open one for New when it goes (F11)", async () => {
    const calls: string[] = []
    vi.stubGlobal(
      "fetch",
      vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
        calls.push(
          `${init?.method ?? "GET"} ${new URL(String(input), "http://localhost").pathname}`
        )
        return new Response(null, { status: 204 })
      })
    )
    const onOpen = renderList()
    fireEvent.click(
      screen.getByRole("button", { name: "Options for Plan the EU launch" })
    )
    expect(await screen.findByRole("menuitem", { name: "Rename" })).toBeTruthy()
    fireEvent.click(screen.getByRole("menuitem", { name: "Delete chat" }))
    const dialog = await screen.findByRole("dialog")
    fireEvent.click(within(dialog).getByRole("button", { name: "Delete" }))
    await waitFor(() =>
      expect(calls).toContain("DELETE /agents/thread-chat-agent/org_1~th_1")
    )
    await waitFor(() =>
      expect(
        screen.queryByRole("button", { name: /^Plan the EU launch/ })
      ).toBeNull()
    )
    expect(onOpen).toHaveBeenCalledWith({ kind: "new" })
    vi.unstubAllGlobals()
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

describe("AgentChatMenu rename", () => {
  async function openRename(
    onRename: (title: string) => Promise<void>,
    title = "Plan"
  ) {
    render(
      <AgentChatMenu
        title={title}
        agentName="Krude"
        disabled={false}
        onRename={onRename}
      />
    )
    fireEvent.click(screen.getByRole("button", { name: "Chat options" }))
    fireEvent.click(await screen.findByRole("menuitem", { name: "Rename" }))
    return screen.findByRole("dialog")
  }

  it("offers only what it is handed", async () => {
    render(<AgentChatMenu title="Plan" disabled={false} onDelete={vi.fn()} />)
    fireEvent.click(screen.getByRole("button", { name: "Chat options" }))
    await screen.findByRole("menuitem", { name: "Delete chat" })
    expect(screen.queryByRole("menuitem", { name: "Rename" })).toBeNull()
  })

  it("renames the chat, Cancel then Save", async () => {
    const onRename = vi.fn(() => Promise.resolve())
    const dialog = await openRename(onRename)
    const field = within(dialog).getByRole("textbox", { name: "Chat name" })
    expect((field as HTMLInputElement).value).toBe("Plan")
    expect(
      within(dialog)
        .getAllByRole("button")
        .map((button) => button.textContent)
    ).toEqual(["Cancel", "Save"])
    fireEvent.change(field, { target: { value: "  EU launch  " } })
    fireEvent.click(within(dialog).getByRole("button", { name: "Save" }))
    await vi.waitFor(() =>
      expect(onRename).toHaveBeenCalledWith("  EU launch  ")
    )
    await vi.waitFor(() => expect(screen.queryByRole("dialog")).toBeNull())
  })

  it("refuses an empty or too long name before sending", async () => {
    const onRename = vi.fn(() => Promise.resolve())
    const dialog = await openRename(onRename)
    const field = within(dialog).getByRole("textbox", { name: "Chat name" })
    fireEvent.change(field, { target: { value: "a".repeat(81) } })
    expect(within(dialog).getByRole("alert").textContent).toBe(
      "Keep the name to 80 characters."
    )
    const save = within(dialog).getByRole("button", { name: "Save" })
    expect(save.hasAttribute("disabled")).toBe(true)
    fireEvent.change(field, { target: { value: "   " } })
    expect(save.hasAttribute("disabled")).toBe(true)
    expect(onRename).not.toHaveBeenCalled()
  })

  it("says the agent is answering when Go refuses with 409", async () => {
    const onRename = vi.fn(() =>
      Promise.reject(new ApiError("busy", 409, "run_in_progress"))
    )
    const dialog = await openRename(onRename)
    fireEvent.change(
      within(dialog).getByRole("textbox", { name: "Chat name" }),
      { target: { value: "EU launch" } }
    )
    fireEvent.click(within(dialog).getByRole("button", { name: "Save" }))
    expect((await within(dialog).findByRole("alert")).textContent).toBe(
      "Krude is answering — try again in a moment"
    )
    expect(screen.getByRole("dialog")).toBeTruthy()
  })
})
