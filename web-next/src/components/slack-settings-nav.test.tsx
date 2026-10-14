// @vitest-environment jsdom

import { afterEach, describe, expect, it, vi } from "vitest"
import {
  cleanup,
  fireEvent,
  render,
  screen,
  within,
} from "@testing-library/react"
import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import { WorkspaceSettings } from "./workspace-settings"
import type { SettingsPage } from "./workspace-settings"
import { ORG, fakeGo } from "./request-card-fixtures"
import type { Role } from "@/lib/auth/client"

const installPath = `/v1/organizations/${ORG}/apps/slack/install`

function showSettings({
  role,
  initialPage,
}: {
  role?: Role
  initialPage?: SettingsPage
}) {
  const go = fakeGo({
    [`GET ${installPath}`]: () => ({
      status: "not_installed",
      add_to_slack_url: "https://slack.com/oauth/v2/authorize?state=abc",
      add_to_slack_expires_at: "2999-01-01T00:00:00Z",
    }),
  })
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })
  render(
    <QueryClientProvider client={client}>
      <WorkspaceSettings
        onClose={() => {}}
        identity={{ id: "user-1", name: "Avery", initials: "A", kind: "user" }}
        session={{
          user: {
            id: "user-1",
            email: "avery@example.com",
            display_name: "Avery",
          },
          organizations: [{ id: ORG, name: "Simulated Org", slug: "sim" }],
        }}
        selectedOrganization={{ id: ORG, name: "Simulated Org", slug: "sim" }}
        organizationRole={role}
        preview={false}
        onSignOut={() => {}}
        signingOut={false}
        signOutError=""
        onOpenTools={() => {}}
        initialPage={initialPage}
      />
    </QueryClientProvider>
  )
  return go
}

afterEach(() => {
  cleanup()
  vi.unstubAllGlobals()
})

describe("Slack among Settings' sections", () => {
  it("is offered to an owner and to an admin, after the sign-in apps and before Email", () => {
    for (const role of ["owner", "admin"] as const) {
      showSettings({ role, initialPage: "email" })

      const sections = screen.getByRole("navigation", {
        name: "Settings sections",
      })
      const names = within(sections)
        .getAllByRole("button")
        .map((button) => button.textContent.trim())
      expect(names).toContain("Slack")
      expect(names.indexOf("Slack")).toBe(names.indexOf("Email") - 1)
      cleanup()
    }
  })

  it("sits under Connections, after You and Workspace", () => {
    showSettings({ role: "owner", initialPage: "email" })

    const sections = screen.getByRole("navigation", {
      name: "Settings sections",
    })
    expect(
      Array.from(sections.children, (item) => item.textContent.trim())
    ).toEqual([
      "You",
      "Account",
      "Workspace",
      "Organisation",
      "People & invitations",
      "Teams",
      "Billing",
      "Connections",
      "Tool access",
      ...(screen.queryByRole("button", { name: "Sign-in apps" })
        ? ["Sign-in apps"]
        : []),
      "Slack",
      "Email",
    ])
  })

  it("is not offered to a member", () => {
    showSettings({ role: "member", initialPage: "email" })

    expect(
      within(
        screen.getByRole("navigation", { name: "Settings sections" })
      ).queryByRole("button", { name: "Slack" })
    ).toBeNull()
  })

  it("opens the page from its entry", async () => {
    const go = showSettings({ role: "admin", initialPage: "email" })
    const sections = screen.getByRole("navigation", {
      name: "Settings sections",
    })

    fireEvent.click(within(sections).getByRole("button", { name: "Slack" }))

    expect(
      screen.getByRole("heading", { level: 2, name: "Slack" })
    ).toBeTruthy()
    expect(await screen.findByText("Slack isn't connected")).toBeTruthy()
    expect(go.count("GET", installPath)).toBe(1)
    expect(
      within(sections).getByRole("button", { name: "Slack" }).className
    ).toContain("is-active")
  })

  it("opens on Slack when Slack sends the browser back", async () => {
    showSettings({ role: "admin", initialPage: "slack" })

    expect(
      screen.getByRole("heading", { level: 2, name: "Slack" })
    ).toBeTruthy()
    await screen.findByText("Slack isn't connected")
  })
})
