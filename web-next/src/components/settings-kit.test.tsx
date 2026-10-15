// @vitest-environment jsdom

import { afterEach, describe, expect, it, vi } from "vitest"
import { cleanup, fireEvent, render, screen } from "@testing-library/react"
import {
  SettingsChips,
  SettingsGroup,
  SettingsRow,
  SettingsSelect,
  SettingsSwitch,
} from "./settings-kit"

afterEach(cleanup)

describe("the settings kit", () => {
  it("labels a group by its heading and keeps a row's words together", () => {
    render(
      <SettingsGroup label="Danger zone">
        <SettingsRow
          tone="danger"
          title="Revoke background access"
          description="Your agent stops working for you."
        />
      </SettingsGroup>
    )
    expect(screen.getByRole("region", { name: "Danger zone" })).toBeTruthy()
    expect(screen.getByText("Revoke background access")).toBeTruthy()
  })

  it("names a switch for its row and reports the new state", () => {
    const change = vi.fn()
    render(
      <SettingsSwitch
        label="Works while you're away"
        checked={false}
        onCheckedChange={change}
      />
    )
    fireEvent.click(
      screen.getByRole("switch", { name: "Works while you're away" })
    )
    expect(change).toHaveBeenCalledWith(true)
  })

  it("shows the chosen option's words in a select", () => {
    render(
      <SettingsSelect
        label="Role"
        value="member"
        options={[
          { value: "admin", label: "Admin" },
          { value: "member", label: "Member" },
        ]}
        onValueChange={() => {}}
      />
    )
    expect(
      screen.getByRole("combobox", { name: "Role" }).textContent
    ).toContain("Member")
  })

  it("lists chips under a name", () => {
    render(<SettingsChips label="Masked" items={["Email addresses"]} />)
    expect(screen.getByRole("list", { name: "Masked" }).textContent).toBe(
      "Email addresses"
    )
  })
})
