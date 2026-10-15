// @vitest-environment jsdom

import { afterEach, describe, expect, it, vi } from "vitest"
import {
  cleanup,
  fireEvent,
  render as renderBase,
  screen,
  waitFor,
  within,
} from "@testing-library/react"
import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import type { ComponentProps } from "react"
import { WorkspaceAccountSettings } from "./workspace-account-settings"
import { WorkspaceAgentSettings } from "./workspace-agent-settings"
import { WorkspaceNotificationSettings } from "./workspace-notification-settings"

const organization = { id: "org-1", name: "Oryxa", slug: "oryxa" }
const session = {
  user: { id: "user-1", email: "avery@example.com", display_name: "Avery" },
  organizations: [organization],
}
const org = "/v1/organizations/org-1"
const guardPath = `${org}/answer-guard/preferences`

const guardSettings = {
  owner: {
    chosen: null,
    default: "secrets",
    floor: "secrets",
    effective: "secrets",
  },
  peer: {
    chosen: null,
    default: "strict",
    floor: "secrets",
    effective: "strict",
  },
  relevance: "shadow",
  regulated: false,
}

function json(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  })
}

/**
 * The services the account page reads, with the answer guard's settings as
 * `guard` answers them. `puts` collects what was sent to change them.
 */
function stubServices(
  guard: () => Response,
  { granted = false }: { granted?: boolean } = {}
) {
  const puts: unknown[] = []
  const patches: unknown[] = []
  const krude = {
    id: "agent-1",
    organization_id: "org-1",
    owner_user_id: "user-1",
    kind: "primary",
    name: "Krude",
    description: "",
    instructions: "",
    status: "active",
    revision: 1,
    created_at: "2026-10-01T00:00:00Z",
    updated_at: "2026-10-01T00:00:00Z",
  }
  const fetchMock = vi.fn(
    async (input: RequestInfo | URL, init?: RequestInit) => {
      const path = new URL(String(input), "http://localhost:3000").pathname
      if (path === guardPath) {
        if (init?.method === "PUT" && typeof init.body === "string") {
          puts.push(JSON.parse(init.body))
        }
        return guard()
      }
      if (path === `${org}/agents`) return json({ agents: [krude] })
      if (path === `${org}/agents/agent-1` && init?.method === "PATCH") {
        const body = JSON.parse(String(init.body)) as { name: string }
        patches.push(body)
        return json({ ...krude, ...body })
      }
      if (path === `${org}/apps/connections`) {
        return json({
          connections: [
            { id: "c1", toolkit: "slack", status: "active", member_id: "u" },
            { id: "c2", toolkit: "notion", status: "active", member_id: "u" },
            { id: "c3", toolkit: "github", status: "expired", member_id: "u" },
          ],
        })
      }
      if (path === `${org}/preferences`) return json({ app_updates: true })
      if (path === "/v1/agent-grant") return json({ granted })
      if (path === `${org}/teams`) return json({ teams: [] })
      return json({ error: { code: "not_found", message: "not found" } }, 404)
    }
  )
  vi.stubGlobal("fetch", fetchMock)
  const asked = (path: string) =>
    fetchMock.mock.calls.filter(
      ([input]) =>
        new URL(String(input), "http://localhost:3000").pathname === path
    )
  return { puts, patches, asked, fetchMock }
}

function showAccount(
  props: Partial<ComponentProps<typeof WorkspaceAccountSettings>> = {}
) {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  })
  return renderBase(
    <QueryClientProvider client={client}>
      <WorkspaceAccountSettings
        identity={{ id: "user-1", name: "Avery", initials: "A", kind: "user" }}
        session={session}
        organization={organization}
        role="member"
        preview={false}
        onSignOut={() => {}}
        signingOut={false}
        signOutError=""
        {...props}
      />
    </QueryClientProvider>
  )
}

const headings = () =>
  screen
    .getAllByRole("heading", { level: 3 })
    .map((heading) => heading.textContent)

afterEach(() => {
  cleanup()
  vi.unstubAllGlobals()
})

describe("WorkspaceAccountSettings", () => {
  it("shows the profile, sign-in and appearance, and reads nothing of the agent", () => {
    const services = stubServices(() => json(guardSettings))
    showAccount()

    expect(headings()).toEqual(["Profile", "Sign-in", "Appearance"])
    expect(screen.getByText("avery@example.com · Member of Oryxa")).toBeTruthy()
    expect(screen.getByRole("button", { name: "Send reset link" })).toBeTruthy()
    expect(screen.getByRole("button", { name: "Sign out" })).toBeTruthy()
    expect(screen.getByRole("radiogroup", { name: "Theme" })).toBeTruthy()
    for (const name of ["Light", "Dark", "System"]) {
      expect(screen.getByRole("radio", { name })).toBeTruthy()
    }
    // No profile edit until there is a route for it.
    expect(screen.queryByRole("button", { name: "Edit" })).toBeNull()
    expect(services.fetchMock).not.toHaveBeenCalled()
  })

  it("keeps the preview's controls off", () => {
    showAccount({ preview: true, session: undefined, organization: undefined })
    expect(
      screen
        .getByRole("button", { name: "Send reset link" })
        .hasAttribute("disabled")
    ).toBe(true)
  })
})

function showAgent(preview = false) {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  })
  return renderBase(
    <QueryClientProvider client={client}>
      <WorkspaceAgentSettings
        organization={preview ? undefined : organization}
        viewerName="Avery"
        preview={preview}
      />
    </QueryClientProvider>
  )
}

describe("WorkspaceAgentSettings", () => {
  it("shows the agent, what it reads, its switches and what it hides", async () => {
    stubServices(() => json(guardSettings))
    showAgent()

    expect(
      await screen.findByText("Avery's agent · reads Slack and Notion")
    ).toBeTruthy()
    expect(screen.getByRole("button", { name: "Rename" })).toBeTruthy()
    expect(
      await screen.findByRole("switch", { name: "Works while you're away" })
    ).toBeTruthy()
    expect(
      await screen.findByRole("switch", {
        name: "Keeps updates from your apps",
      })
    ).toBeTruthy()
    expect(
      await screen.findByRole("radiogroup", {
        name: "Answers other people read",
      })
    ).toBeTruthy()
    expect(headings()).toEqual(["Krude", "What it hides when it answers"])
  })

  it("saves a choice of what it hides, changing only that row", async () => {
    const services = stubServices(() => json(guardSettings))
    showAgent()

    const peer = await screen.findByRole("radiogroup", {
      name: "Answers other people read",
    })
    fireEvent.click(within(peer).getByRole("radio", { name: "Secrets only" }))
    await waitFor(() => expect(services.puts).toEqual([{ peer: "secrets" }]))
  })

  it("renames the agent with Cancel and Save at the right of the form", async () => {
    const services = stubServices(() => json(guardSettings))
    showAgent()

    fireEvent.click(await screen.findByRole("button", { name: "Rename" }))
    const form = screen.getByRole("form", { name: "Rename Krude" })
    const buttons = within(form)
      .getAllByRole("button")
      .map((button) => button.textContent)
    expect(buttons).toEqual(["Cancel", "Save"])
    fireEvent.change(
      within(form).getByRole("textbox", { name: "Agent name" }),
      {
        target: { value: "Juno" },
      }
    )
    fireEvent.click(within(form).getByRole("button", { name: "Save" }))
    await waitFor(() => expect(services.patches).toEqual([{ name: "Juno" }]))
  })

  it("offers Revoke in a danger zone once access is on, with a destructive confirm", async () => {
    stubServices(() => json(guardSettings), { granted: true })
    showAgent()

    fireEvent.click(await screen.findByRole("button", { name: "Revoke" }))
    const confirm = screen.getByRole("button", { name: "Revoke access" })
    expect(confirm.getAttribute("data-tone")).toBe("danger")
    expect(
      screen.getByRole("region", { name: "Danger zone" }).contains(confirm)
    ).toBe(true)
  })

  it("asks Go for nothing in the layout preview", () => {
    const services = stubServices(() => json(guardSettings))
    showAgent(true)
    expect(
      screen.getByText("Sign in to a workspace to manage your agent.")
    ).toBeTruthy()
    expect(services.fetchMock).not.toHaveBeenCalled()
  })
})

describe("WorkspaceNotificationSettings", () => {
  it("says honestly that channels are coming, and lists team parley emails", async () => {
    stubServices(() => json(guardSettings))
    const client = new QueryClient({
      defaultOptions: { queries: { retry: false } },
    })
    renderBase(
      <QueryClientProvider client={client}>
        <WorkspaceNotificationSettings
          organization={organization}
          preview={false}
        />
      </QueryClientProvider>
    )
    expect(headings()).toEqual(["What and where", "Parley emails by team"])
    expect(screen.queryByRole("checkbox")).toBeNull()
    expect(
      await screen.findByText("You are not on any teams yet.")
    ).toBeTruthy()
  })
})
