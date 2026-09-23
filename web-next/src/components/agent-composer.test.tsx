// @vitest-environment jsdom

import { afterEach, describe, expect, it, vi } from "vitest"
import { cleanup, fireEvent, render, screen } from "@testing-library/react"
import { useState } from "react"
import { AgentComposer } from "./agent-composer"

afterEach(cleanup)

function Composer({ onSubmit }: { onSubmit: (message: string) => void }) {
  const [value, setValue] = useState("")
  return (
    <AgentComposer
      label="Message Primary agent"
      value={value}
      onChange={setValue}
      onSubmit={onSubmit}
    />
  )
}

describe("AgentComposer", () => {
  it("sends with Enter and keeps Shift+Enter and input methods for typing", () => {
    const onSubmit = vi.fn()
    render(<Composer onSubmit={onSubmit} />)
    const field = screen.getByRole("textbox", { name: "Message Primary agent" })

    fireEvent.keyDown(field, { key: "Enter" })
    expect(onSubmit).not.toHaveBeenCalled()

    fireEvent.change(field, { target: { value: "  Plan the launch  " } })
    fireEvent.keyDown(field, { key: "Enter", shiftKey: true })
    fireEvent.keyDown(field, { key: "Enter", isComposing: true })
    fireEvent.keyDown(field, { key: "Enter", keyCode: 229 })
    expect(onSubmit).not.toHaveBeenCalled()

    fireEvent.keyDown(field, { key: "Enter" })
    expect(onSubmit).toHaveBeenCalledExactlyOnceWith("Plan the launch")
  })

  it("does not send while disabled and says why", () => {
    const onSubmit = vi.fn()
    render(
      <AgentComposer
        label="Message Primary agent"
        value="Hello"
        onChange={vi.fn()}
        onSubmit={onSubmit}
        disabled
        status="Chat isn't connected yet."
      />
    )
    const field = screen.getByRole("textbox", { name: "Message Primary agent" })
    const send = screen.getByRole("button", { name: "Send message" })

    expect(field.hasAttribute("disabled")).toBe(true)
    expect(send.hasAttribute("disabled")).toBe(true)
    fireEvent.keyDown(field, { key: "Enter" })
    fireEvent.submit(field.closest("form")!)
    expect(onSubmit).not.toHaveBeenCalled()
    expect(
      document.getElementById(field.getAttribute("aria-describedby") ?? "")
        ?.textContent
    ).toBe("Chat isn't connected yet.")
  })

  it("offers stop instead of send while a reply streams", () => {
    const onSubmit = vi.fn()
    const onStop = vi.fn()
    render(
      <AgentComposer
        label="Message Primary agent"
        value="Next question"
        onChange={vi.fn()}
        onSubmit={onSubmit}
        streaming
        onStop={onStop}
      />
    )

    fireEvent.keyDown(screen.getByRole("textbox"), { key: "Enter" })
    expect(onSubmit).not.toHaveBeenCalled()
    expect(screen.queryByRole("button", { name: "Send message" })).toBeNull()
    fireEvent.click(screen.getByRole("button", { name: "Stop response" }))
    expect(onStop).toHaveBeenCalledOnce()
  })
})
