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
function stubServices(guard: () => Response) {
  const puts: unknown[] = []
  const fetchMock = vi.fn(
    async (input: RequestInfo | URL, init?: RequestInit) => {
      const path = new URL(String(input), "http://localhost:3000").pathname
      if (path === guardPath) {
        if (init?.method === "PUT" && typeof init.body === "string") {
          puts.push(JSON.parse(init.body))
        }
        return guard()
      }
      if (path === `${org}/preferences`) return json({ app_updates: true })
      if (path === "/v1/agent-grant") return json({ granted: false })
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
  return { puts, asked, fetchMock }
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
  it("still shows the account and its sections, with What your agents share once Go answers, after What your agents may do", async () => {
    stubServices(() => json(guardSettings))
    showAccount()

    expect(
      await screen.findByText("You are not on any teams yet.")
    ).toBeTruthy()
    expect(
      await screen.findByRole("radiogroup", {
        name: "Answers you start yourself",
      })
    ).toBeTruthy()
    expect(screen.getByText("avery@example.com")).toBeTruthy()
    expect(screen.getByRole("button", { name: "Send reset link" })).toBeTruthy()
    expect(headings()).toEqual([
      "Sign-in",
      "What your agents may do",
      "What your agents share",
      "Team call emails",
    ])
  })

  it("leaves the section out, and the rest as it was, when Go does not serve the settings", async () => {
    const services = stubServices(() =>
      json({ error: { code: "not_found", message: "not found" } }, 404)
    )
    showAccount()

    expect(
      await screen.findByText("You are not on any teams yet.")
    ).toBeTruthy()
    await waitFor(() => expect(services.asked(guardPath)).toHaveLength(1))
    await waitFor(() =>
      expect(
        screen.getByRole("switch", { name: "Updates from your apps" })
      ).toBeTruthy()
    )

    expect(headings()).toEqual([
      "Sign-in",
      "What your agents may do",
      "Team call emails",
    ])
    expect(screen.queryByRole("radiogroup")).toBeNull()
    expect(screen.queryByRole("alert")).toBeNull()
  })

  it("saves a choice made in the account page, changing only that row", async () => {
    const services = stubServices(() => json(guardSettings))
    showAccount()

    const peer = await screen.findByRole("radiogroup", {
      name: "Answers other people read",
    })
    fireEvent.click(within(peer).getByRole("radio", { name: "Secrets only" }))

    await waitFor(() => expect(services.puts).toEqual([{ peer: "secrets" }]))
  })

  it("asks Go for nothing, and shows nothing of it, in the layout preview", () => {
    const services = stubServices(() => json(guardSettings))
    showAccount({ preview: true, session: undefined, organization: undefined })

    expect(headings()).toEqual([
      "Sign-in",
      "What your agents may do",
      "Team call emails",
    ])
    expect(services.fetchMock).not.toHaveBeenCalled()
  })
})
