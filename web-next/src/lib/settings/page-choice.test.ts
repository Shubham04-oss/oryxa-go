// @vitest-environment jsdom

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"
import { rememberedSettingsPage, rememberSettingsPage } from "./page-choice"
import { browserStorage } from "@/test/browser-storage"

const scope = { organizationId: "org-1", userId: "user-1" }

beforeEach(() => vi.stubGlobal("localStorage", browserStorage()))
afterEach(() => {
  vi.unstubAllGlobals()
  vi.restoreAllMocks()
})

describe("remembered Settings section", () => {
  it("keeps each member's section separate in each workspace", () => {
    rememberSettingsPage(scope, "people")
    rememberSettingsPage({ ...scope, userId: "user-2" }, "billing")
    rememberSettingsPage({ ...scope, organizationId: "org-2" }, "teams")

    expect(rememberedSettingsPage(scope)).toBe("people")
    expect(rememberedSettingsPage({ ...scope, userId: "user-2" })).toBe(
      "billing"
    )
    expect(rememberedSettingsPage({ ...scope, organizationId: "org-2" })).toBe(
      "teams"
    )
    expect(rememberedSettingsPage({ ...scope, userId: "user-3" })).toBeNull()
  })

  it("rejects stale or invalid stored sections", () => {
    rememberSettingsPage(scope, "account")
    const key = window.localStorage.key(0)!
    for (const invalid of ["old-section", "constructor", '{"page":"people"}']) {
      window.localStorage.setItem(key, invalid)
      expect(rememberedSettingsPage(scope)).toBeNull()
    }
  })

  it("does not store signed-out or preview choices", () => {
    rememberSettingsPage(null, "people")
    expect(window.localStorage.length).toBe(0)
    expect(rememberedSettingsPage(null)).toBeNull()
  })

  it("falls back when storage cannot be read or written", () => {
    vi.spyOn(window.localStorage, "getItem").mockImplementation(() => {
      throw new DOMException("Storage disabled", "SecurityError")
    })
    vi.spyOn(window.localStorage, "setItem").mockImplementation(() => {
      throw new DOMException("Storage full", "QuotaExceededError")
    })
    expect(rememberedSettingsPage(scope)).toBeNull()
    expect(() => rememberSettingsPage(scope, "teams")).not.toThrow()
  })
})
