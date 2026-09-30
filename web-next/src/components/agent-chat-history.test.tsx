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

vi.mock("thinking-orbs", () => ({
  ThinkingOrb: ({ state }: { state: string }) => (
    <i aria-hidden="true" data-orb-state={state} />
  ),
}))

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

describe("the sidebar's Recent list", () => {
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
    // Always shown: there is no disclosure to open first.
    expect(screen.queryByRole("button", { name: "History" })).toBeNull()
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
    expect(rows[2].getAttribute("data-current")).toBe("true")
    fireEvent.click(
      within(rows[0]).getByRole("button", { name: /Launch1 min ago/ })
    )
    expect(open).toHaveBeenCalledExactlyOnceWith("new")
  })

  it("says the agent is working in the conversation whose chat runs, in place of its time", () => {
    render(
      <AgentChatHistory
        threads={threads}
        working="new"
        onOpen={vi.fn()}
        onDelete={vi.fn()}
      />
    )
    const row = screen.getByRole("button", { name: /^Launch/ })
    expect(row.textContent).toBe("LaunchWorking")
    expect(row.querySelector("[data-orb-state=working]")).toBeTruthy()
    expect(
      screen.getByRole("button", { name: /^Earlier plan/ }).textContent
    ).toBe("Earlier plan1 h ago")
  })

  it("hides empty abandoned starts but keeps selected and pending untitled chats", () => {
    const props = { threads, onOpen: vi.fn(), onDelete: vi.fn() }
    const view = render(<AgentChatHistory {...props} />)
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

  it("keeps every row closed while a first message is on its way", () => {
    const open = vi.fn()
    render(
      <AgentChatHistory
        threads={threads}
        disabled
        onOpen={open}
        onDelete={vi.fn()}
      />
    )
    for (const button of screen.getAllByRole("button"))
      expect(button.hasAttribute("disabled")).toBe(true)
    fireEvent.click(screen.getByRole("button", { name: /^Launch/ }))
    expect(open).not.toHaveBeenCalled()
  })

  it("reports loading, empty and failed reads distinctly", () => {
    const props = {
      threads: [],
      onOpen: vi.fn(),
      onDelete: vi.fn(),
      onRetry: vi.fn(),
    }
    const view = render(<AgentChatHistory {...props} loading />)
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
