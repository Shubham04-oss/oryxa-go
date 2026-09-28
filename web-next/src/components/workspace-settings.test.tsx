// @vitest-environment jsdom

import { afterEach, describe, expect, it } from "vitest"
import { cleanup, render, screen } from "@testing-library/react"
import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import { WorkspaceSettings } from "./workspace-settings"
import type { SettingsPage } from "./workspace-settings"

function showSettings(initialPage?: SettingsPage) {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })
  return render(
    <QueryClientProvider client={client}>
      <WorkspaceSettings
        onClose={() => {}}
        identity={{ id: "user-1", name: "Avery", initials: "A", kind: "user" }}
        preview
        onSignOut={() => {}}
        signingOut={false}
        signOutError=""
        onOpenTools={() => {}}
        initialPage={initialPage}
      />
    </QueryClientProvider>
  )
}

afterEach(cleanup)

describe("WorkspaceSettings", () => {
  it("opens on People by default", () => {
    showSettings()
    expect(
      screen.getByRole("heading", { level: 2, name: "People & invitations" })
    ).toBeTruthy()
  })

  it("opens on the page a link asked for", () => {
    showSettings("billing")
    expect(
      screen.getByRole("heading", { level: 2, name: "Billing" })
    ).toBeTruthy()
  })
})
