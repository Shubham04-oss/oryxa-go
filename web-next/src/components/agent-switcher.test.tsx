// @vitest-environment jsdom

import { afterEach, describe, expect, it, vi } from "vitest"
import {
  cleanup,
  fireEvent,
  render,
  screen,
  within,
} from "@testing-library/react"
import { AgentSwitcher } from "./agent-switcher"

afterEach(cleanup)

describe("AgentSwitcher", () => {
  it("marks the selected agent pressed and named, and reports clicks on the rest", () => {
    const onSelect = vi.fn()
    render(
      <AgentSwitcher
        agents={[
          { id: "agent-ada", name: "Ada", avatarSeed: "seed-ada" },
          { id: "agent-rex", name: "Rex", avatarSeed: "seed-rex" },
        ]}
        selectedId="agent-ada"
        onSelect={onSelect}
        placeholders={0}
      />
    )

    const selected = screen.getByRole("button", { name: "Ada", pressed: true })
    expect(selected).toBeTruthy()
    expect(within(selected).getByText("Ada")).toBeTruthy()

    const other = screen.getByRole("button", { name: "Rex", pressed: false })
    expect(other).toBeTruthy()
    expect(within(other).queryByText("Rex")).toBeNull()

    fireEvent.click(other)
    expect(onSelect).toHaveBeenCalledExactlyOnceWith("agent-rex")
  })

  it("falls back to an accessible-only name when the agent's name is unknown", () => {
    render(
      <AgentSwitcher
        agents={[{ id: "agent-primary", avatarSeed: "seed-primary" }]}
        selectedId="agent-primary"
        onSelect={vi.fn()}
        placeholders={0}
      />
    )

    const selected = screen.getByRole("button", {
      name: "Primary agent",
      pressed: true,
    })
    expect(selected).toBeTruthy()
    expect(screen.queryByText("Primary agent")).toBeNull()
  })

  it("renders empty placeholder slots that are disabled and never look like real agents", () => {
    render(
      <AgentSwitcher
        agents={[{ id: "agent-primary", avatarSeed: "seed-primary" }]}
        selectedId="agent-primary"
        onSelect={vi.fn()}
        placeholders={3}
      />
    )

    const slots = screen.getAllByRole("button", { name: "Empty agent slot" })
    expect(slots).toHaveLength(3)
    for (const slot of slots) {
      expect(slot.hasAttribute("disabled")).toBe(true)
      expect(slot.textContent).toBe("")
      expect(slot.querySelector("img, svg")).toBeNull()
    }
  })
})
