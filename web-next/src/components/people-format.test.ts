import { describe, expect, it } from "vitest"
import { firstName, initialsOf, personIdentity } from "./people-format"

describe("firstName", () => {
  it("greets people by their first name, and an email address as it is", () => {
    expect(firstName("Priya Shah")).toBe("Priya")
    expect(firstName("  Ada  ")).toBe("Ada")
    expect(firstName("sam@sim.oryxa")).toBe("sam@sim.oryxa")
  })
})

describe("personIdentity", () => {
  it("is a user's avatar identity, keyed by their id, with the first letters of their name", () => {
    expect(initialsOf("Priya Shah")).toBe("PS")
    expect(initialsOf("  ada lovelace byron ")).toBe("AL")
    expect(initialsOf("   ")).toBe("?")
    expect(personIdentity("user-priya", "Priya Shah")).toEqual({
      id: "user-priya",
      name: "Priya Shah",
      initials: "PS",
      kind: "user",
    })
  })
})
