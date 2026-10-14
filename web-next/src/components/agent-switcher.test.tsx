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

  it("names the agent in use for screen readers even before its name loads", () => {
    render(
      <AgentSwitcher
        agents={[
          { id: "agent-primary", avatarSeed: "seed-primary" },
          { id: "agent-rex", name: "Rex", avatarSeed: "seed-rex" },
        ]}
        selectedId="agent-primary"
        onSelect={vi.fn()}
      />
    )

    const selected = screen.getByRole("button", {
      name: "Primary agent",
      pressed: true,
    })
    expect(selected).toBeTruthy()
    expect(screen.queryByText("Primary agent")).toBeNull()
  })

  it("shows nothing when there is only one agent to choose", () => {
    const { container } = render(
      <AgentSwitcher
        agents={[{ id: "agent-primary", name: "Krude", avatarSeed: "seed" }]}
        selectedId="agent-primary"
        onSelect={vi.fn()}
      />
    )

    expect(container.innerHTML).toBe("")
    expect(screen.queryByRole("group", { name: "Switch agent" })).toBeNull()
  })
})
