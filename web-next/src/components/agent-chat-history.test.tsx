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
import { AgentChatHistory } from "./agent-chat-history"

const threads = [
  {
    id: "old",
    title: "Earlier plan",
    updated_at: new Date(Date.now() - 3_600_000).toISOString(),
  },
  {
    id: "new",
    title: "Launch",
    updated_at: new Date(Date.now() - 60_000).toISOString(),
  },
  {
    id: "blank",
    title: "  ",
    updated_at: new Date(Date.now() - 120_000).toISOString(),
  },
]
afterEach(cleanup)

describe("conversation history", () => {
  it("opens for each explicit Home request without picking a chat or overriding a later collapse", () => {
    const props = {
      threads,
      onOpen: vi.fn(),
      onDelete: vi.fn(),
      onOpened: vi.fn(),
    }
    const request = {}
    const view = render(<AgentChatHistory {...props} openRequest={request} />)
    const toggle = screen.getByRole("button", { name: "History" })
    expect(toggle.getAttribute("aria-expanded")).toBe("true")
    expect(props.onOpened).toHaveBeenCalledOnce()
    expect(props.onOpen).not.toHaveBeenCalled()
    fireEvent.click(toggle)
    view.rerender(<AgentChatHistory {...props} openRequest={request} />)
    expect(toggle.getAttribute("aria-expanded")).toBe("false")
    view.rerender(<AgentChatHistory {...props} openRequest={{}} />)
    expect(toggle.getAttribute("aria-expanded")).toBe("true")
    expect(props.onOpened).toHaveBeenCalledTimes(2)
  })
  it("shows newest first, titles, relative time, current selection and approval markers", () => {
    const open = vi.fn()
    render(
      <AgentChatHistory
        threads={threads}
        selected="old"
        waiting={new Set(["blank"])}
        onOpen={open}
        onDelete={vi.fn()}
      />
    )
    const toggle = screen.getByRole("button", { name: "History" })
    expect(toggle.getAttribute("aria-expanded")).toBe("false")
    fireEvent.click(toggle)
    const rows = screen.getAllByRole("listitem")
    expect(rows.map((row) => row.querySelector("button")?.textContent)).toEqual(
      [
        "Launch1 min ago",
        "Untitled chatNeeds approval · 2 min ago",
        "Earlier plan1 h ago",
      ]
    )
    const selected = within(rows[2]).getByRole("button", {
      name: /Earlier plan1 h ago/,
    })
    expect(selected.getAttribute("aria-current")).toBe("true")
    fireEvent.click(
      within(rows[0]).getByRole("button", { name: /Launch1 min ago/ })
    )
    expect(open).toHaveBeenCalledExactlyOnceWith("new")
  })
  it("hides empty abandoned starts but keeps selected and pending untitled chats", () => {
    const props = { threads, onOpen: vi.fn(), onDelete: vi.fn() }
    const view = render(<AgentChatHistory {...props} />)
    fireEvent.click(screen.getByRole("button", { name: "History" }))
    expect(screen.queryByText("Untitled chat")).toBeNull()
    view.rerender(<AgentChatHistory {...props} selected="blank" />)
    expect(screen.getByText("Untitled chat")).toBeTruthy()
    view.rerender(<AgentChatHistory {...props} waiting={new Set(["blank"])} />)
    expect(screen.getByText("Untitled chat")).toBeTruthy()
    expect(screen.getByText("Needs approval ·")).toBeTruthy()
  })
  it("requires an inline delete confirmation and keeps a refusal actionable", async () => {
    const remove = vi.fn(async () => {
      throw new Error("Couldn't clear the chat. Try again.")
    })
    render(
      <AgentChatHistory threads={threads} onOpen={vi.fn()} onDelete={remove} />
    )
    fireEvent.click(screen.getByRole("button", { name: "History" }))
    fireEvent.click(screen.getByRole("button", { name: "Delete Launch" }))
    expect(remove).not.toHaveBeenCalled()
    fireEvent.click(screen.getByRole("button", { name: "Cancel" }))
    expect(remove).not.toHaveBeenCalled()
    fireEvent.click(screen.getByRole("button", { name: "Delete Launch" }))
    fireEvent.click(
      screen.getByRole("button", { name: "Confirm delete Launch" })
    )
    await waitFor(() => expect(remove).toHaveBeenCalledExactlyOnceWith("new"))
    expect(await screen.findByRole("alert")).toHaveProperty(
      "textContent",
      "Couldn't clear the chat. Try again."
    )
    expect(screen.getByText("Launch")).toBeTruthy()
    expect(
      screen
        .getByRole("button", { name: "Confirm delete Launch" })
        .hasAttribute("disabled")
    ).toBe(false)
  })
  it("reports loading, empty and failed reads distinctly", () => {
    const props = {
      threads: [],
      onOpen: vi.fn(),
      onDelete: vi.fn(),
      onRetry: vi.fn(),
    }
    const view = render(<AgentChatHistory {...props} loading />)
    fireEvent.click(screen.getByRole("button", { name: "History" }))
    expect(screen.getByRole("status").textContent).toBe(
      "Loading conversations…"
    )
    view.rerender(<AgentChatHistory {...props} />)
    expect(screen.getByText("No past conversations yet.")).toBeTruthy()
    view.rerender(<AgentChatHistory {...props} error="Can't load history." />)
    fireEvent.click(screen.getByRole("button", { name: "Try again" }))
    expect(props.onRetry).toHaveBeenCalledOnce()
  })
})
