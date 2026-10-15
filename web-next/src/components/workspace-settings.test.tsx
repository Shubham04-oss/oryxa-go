// @vitest-environment jsdom

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"
import { cleanup, fireEvent, render, screen } from "@testing-library/react"
import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import { WorkspaceSettings } from "./workspace-settings"
import type { SettingsPage } from "./workspace-settings"
import type { Role } from "@/lib/auth/client"
import {
  rememberedSettingsPage,
  rememberSettingsPage,
} from "@/lib/settings/page-choice"
import { fakeGo } from "./request-card-fixtures"
import { browserStorage } from "@/test/browser-storage"

const scope = { organizationId: "org-1", userId: "user-1" }

function showSettings(
  initialPage?: SettingsPage,
  { live = false, role = "member" }: { live?: boolean; role?: Role } = {}
) {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })
  return render(
    <QueryClientProvider client={client}>
      <WorkspaceSettings
        onClose={() => {}}
        identity={{ id: "user-1", name: "Avery", initials: "A", kind: "user" }}
        session={
          live
            ? {
                user: { id: scope.userId, email: "avery@example.com" },
                organizations: [
                  { id: scope.organizationId, name: "Oryxa", slug: "oryxa" },
                ],
              }
            : undefined
        }
        organizationRole={role}
        preview={!live}
        onSignOut={() => {}}
        signingOut={false}
        signOutError=""
        onOpenTools={() => {}}
        initialPage={initialPage}
      />
    </QueryClientProvider>
  )
}

beforeEach(() => {
  vi.stubGlobal("localStorage", browserStorage())
  fakeGo({ "GET /v1/apps/integrations": () => ({ integrations: [] }) })
})
afterEach(() => {
  cleanup()
  vi.unstubAllGlobals()
  vi.restoreAllMocks()
})

describe("WorkspaceSettings", () => {
  it("opens on Account by default", () => {
    showSettings()
    expect(
      screen.getByRole("heading", { level: 2, name: "Account" })
    ).toBeTruthy()
  })

  it("remembers the last section across settings visits", () => {
    showSettings(undefined, { live: true })
    expect(
      screen.getByRole("heading", { level: 2, name: "Account" })
    ).toBeTruthy()
    fireEvent.click(screen.getByRole("button", { name: "Email" }))
    expect(rememberedSettingsPage(scope)).toBe("email")
    cleanup()

    showSettings(undefined, { live: true })
    expect(
      screen.getByRole("heading", { level: 2, name: "Email" })
    ).toBeTruthy()
  })

  it("opens on the page a Billing link asked for over the remembered page", () => {
    rememberSettingsPage(scope, "people")
    showSettings("billing", { live: true })
    expect(
      screen.getByRole("heading", { level: 2, name: "Billing" })
    ).toBeTruthy()
    expect(rememberedSettingsPage(scope)).toBe("billing")
  })

  it("does not share a member's remembered section with the layout preview", () => {
    rememberSettingsPage(scope, "people")
    showSettings()
    expect(
      screen.getByRole("heading", { level: 2, name: "Account" })
    ).toBeTruthy()
    fireEvent.click(screen.getByRole("button", { name: "Teams" }))
    expect(rememberedSettingsPage(scope)).toBe("people")
  })

  it("keeps a member's remembered Slack section, where they link their own account", () => {
    rememberSettingsPage(scope, "slack")
    fakeGo({})
    showSettings(undefined, { live: true })
    expect(
      screen.getByRole("heading", { level: 2, name: "Slack" })
    ).toBeTruthy()
    expect(screen.getByRole("button", { name: "Slack" })).toBeTruthy()
  })

  it("preserves the guarded explicit Slack return for a member", () => {
    rememberSettingsPage(scope, "people")
    const go = fakeGo({})
    showSettings("slack", { live: true })
    expect(
      screen.getByRole("heading", { level: 2, name: "Slack" })
    ).toBeTruthy()
    expect(
      screen.getByText("Organisation owners and admins manage Slack.")
    ).toBeTruthy()
    // Only the member's own link is read; nothing of the install.
    expect(go.sent().every((sent) => sent.endsWith("/apps/slack/link"))).toBe(
      true
    )
  })

  it("falls back to Account for an unavailable remembered Sign-in apps section", async () => {
    rememberSettingsPage(scope, "signin")
    showSettings(undefined, { live: true, role: "admin" })
    expect(
      screen.getByRole("heading", { level: 2, name: "Account" })
    ).toBeTruthy()
    expect(screen.queryByRole("button", { name: "Sign-in apps" })).toBeNull()
  })

  it("restores a remembered Sign-in apps section once availability is known", async () => {
    let resolve!: (value: object) => void
    fakeGo({
      "GET /v1/apps/integrations": () =>
        new Promise<object>((done) => {
          resolve = done
        }),
    })
    rememberSettingsPage(scope, "signin")
    showSettings(undefined, { live: true, role: "admin" })
    expect(
      screen.getByRole("heading", { level: 2, name: "Account" })
    ).toBeTruthy()
    expect(rememberedSettingsPage(scope)).toBe("signin")
    resolve({ integrations: [{ toolkit: "github", kinds: ["oauth2_code"] }] })
    expect(
      await screen.findByRole("heading", { level: 2, name: "Sign-in apps" })
    ).toBeTruthy()
  })

  it("keeps Account usable when browser storage is blocked", () => {
    vi.spyOn(window.localStorage, "getItem").mockImplementation(() => {
      throw new DOMException("Storage disabled", "SecurityError")
    })
    vi.spyOn(window.localStorage, "setItem").mockImplementation(() => {
      throw new DOMException("Storage full", "QuotaExceededError")
    })
    showSettings(undefined, { live: true })
    expect(
      screen.getByRole("heading", { level: 2, name: "Account" })
    ).toBeTruthy()
    fireEvent.click(
      screen.getByRole("button", { name: "People & invitations" })
    )
    expect(
      screen.getByRole("heading", { level: 2, name: "People & invitations" })
    ).toBeTruthy()
  })
})
