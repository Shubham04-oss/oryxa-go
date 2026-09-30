import { describe, expect, it } from "vitest"
import { capitalized, toolsLine } from "./request-words"

describe("capitalized", () => {
  it("starts a sentence with a name", () => {
    expect(capitalized("your agent")).toBe("Your agent")
    expect(capitalized("Nia")).toBe("Nia")
    expect(capitalized("")).toBe("")
  })
})

describe("toolsLine", () => {
  it("says what approving lets the agent read, from the toolboxes it asked for", () => {
    expect(toolsLine("Nia", ["apps", "knowledge"])).toBe(
      "Nia may read your connected apps and team knowledge."
    )
    expect(toolsLine("Nia", ["knowledge", "apps"])).toBe(
      "Nia may read your connected apps and team knowledge."
    )
    expect(toolsLine("Nia", ["apps"])).toBe("Nia may read your connected apps.")
    expect(toolsLine("your agent", ["knowledge"])).toBe(
      "Your agent may read team knowledge."
    )
    // Nothing asked for, or nothing that opens anything: no line.
    for (const tools of [undefined, [], ["github.search"]]) {
      expect(toolsLine("Nia", tools)).toBeUndefined()
    }
  })
})
