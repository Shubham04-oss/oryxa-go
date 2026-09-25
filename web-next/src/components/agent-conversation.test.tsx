// @vitest-environment jsdom

import { afterEach, describe, expect, it } from "vitest"
import { cleanup, fireEvent, render, screen } from "@testing-library/react"
import { AgentConversation } from "./agent-conversation"

afterEach(cleanup)

/** jsdom lays nothing out: give the log a height and a content height. */
function layOut(log: HTMLElement, content: number, height = 300) {
  Object.defineProperty(log, "clientHeight", {
    configurable: true,
    value: height,
  })
  Object.defineProperty(log, "scrollHeight", {
    configurable: true,
    value: content,
  })
}

function Log({ lines, follow }: { lines: number; follow?: unknown }) {
  return (
    <AgentConversation agentName="Leo" follow={follow}>
      {Array.from({ length: lines }, (_, index) => (
        <p key={index}>Line {index}</p>
      ))}
    </AgentConversation>
  )
}

describe("AgentConversation", () => {
  it("keeps the end in view as the conversation grows", () => {
    const { rerender } = render(<Log lines={1} />)
    const log = screen.getByRole("log", { name: "Conversation with Leo" })
    layOut(log, 900)
    rerender(<Log lines={2} />)
    expect(log.scrollTop).toBe(900)
  })

  it("stays where the member scrolled up to, until their next message", () => {
    const { rerender } = render(<Log lines={1} follow="message_1" />)
    const log = screen.getByRole("log")
    layOut(log, 900)
    rerender(<Log lines={2} follow="message_1" />)

    // Reading something 500px above the end.
    log.scrollTop = 100
    fireEvent.scroll(log)
    layOut(log, 1200)
    rerender(<Log lines={3} follow="message_1" />)
    expect(log.scrollTop).toBe(100)

    // Their own new message brings them back to the end.
    rerender(<Log lines={4} follow="message_2" />)
    expect(log.scrollTop).toBe(1200)
  })

  it("says when it is loading or empty, and shows an error with a retry", () => {
    const { rerender } = render(
      <AgentConversation agentName="Leo" state="loading" />
    )
    expect(screen.getByText("Loading the conversation…")).toBeTruthy()
    rerender(<AgentConversation agentName="Leo" state="empty" />)
    expect(
      screen.getByText("Ask anything to start a conversation.")
    ).toBeTruthy()
    let retried = false
    rerender(
      <AgentConversation
        agentName="Leo"
        state="empty"
        error="Couldn't load your conversations."
        onRetry={() => {
          retried = true
        }}
      />
    )
    fireEvent.click(screen.getByRole("button", { name: "Try again" }))
    expect(retried).toBe(true)
    expect(screen.getByRole("alert").textContent).toContain(
      "Couldn't load your conversations."
    )
  })
})
