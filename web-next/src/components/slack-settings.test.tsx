// @vitest-environment jsdom

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"
import {
  cleanup,
  fireEvent,
  render,
  screen,
  waitFor,
  within,
} from "@testing-library/react"
import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import { WorkspaceSlackSettings } from "./slack-settings"
import { clockTime } from "./time-format"
import { NOW, ORG, OWNER, failure, fakeGo } from "./request-card-fixtures"
import type { Route } from "./request-card-fixtures"
import type { Role } from "@/lib/auth/client"
import type { SlackChannel, SlackInstall } from "@/lib/apps/slack"

const installPath = `/v1/organizations/${ORG}/apps/slack/install`
const channelsPath = `/v1/organizations/${ORG}/apps/slack/channels`
const organization = { id: ORG, name: "Simulated Org", slug: "sim" }
const consentPage = "https://slack.com/oauth/v2/authorize?state=abc"

const notInstalled: SlackInstall = {
  status: "not_installed",
  add_to_slack_url: consentPage,
  add_to_slack_expires_at: "2026-09-29T12:40:00Z",
}

const active: SlackInstall = {
  status: "active",
  team: { id: "T01", name: "Acme" },
  bot_user_id: "U01",
  installed_at: "2026-09-29T12:00:00Z",
  add_to_slack_url: consentPage,
  add_to_slack_expires_at: "2026-09-29T12:40:00Z",
}

function channel(overrides: Partial<SlackChannel> = {}): SlackChannel {
  return {
    channel_id: "C01",
    name: "leadership",
    is_private: true,
    is_shared: false,
    markable: true,
    high_priority: false,
    ...overrides,
  }
}

let client: QueryClient

function stubLocation(search = "") {
  const url = new URL(`https://app.oryxa.in/${search}`)
  const assign = vi.fn()
  vi.stubGlobal("location", {
    assign,
    hash: url.hash,
    href: url.href,
    origin: url.origin,
    pathname: url.pathname,
    search: url.search,
  })
  return assign
}

function renderSettings(
  routes: Record<string, Route>,
  {
    role = "admin",
    preview = false,
    withOrganization = true,
  }: { role?: Role; preview?: boolean; withOrganization?: boolean } = {}
) {
  const go = fakeGo(routes)
  client = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  render(
    <QueryClientProvider client={client}>
      <WorkspaceSlackSettings
        organization={withOrganization ? organization : undefined}
        role={role}
        preview={preview}
      />
    </QueryClientProvider>
  )
  return go
}

beforeEach(() => {
  vi.useFakeTimers({ toFake: ["Date"] })
  vi.setSystemTime(NOW)
})

afterEach(() => {
  cleanup()
  client.clear()
  vi.useRealTimers()
  vi.unstubAllGlobals()
})

describe("who sees Slack's settings", () => {
  it("says a preview has nothing to manage, and reads nothing", () => {
    const go = renderSettings({}, { preview: true })

    expect(
      screen.getByText(/Layout preview · Sign in to manage Slack/)
    ).toBeTruthy()
    expect(go.calls).toHaveLength(0)
  })

  it("asks for an organisation first", () => {
    const go = renderSettings({}, { withOrganization: false })

    expect(screen.getByText("Select an organisation first.")).toBeTruthy()
    expect(go.calls).toHaveLength(0)
  })

  it("tells a member who manages it, and reads nothing", () => {
    const go = renderSettings({}, { role: "member" })

    expect(
      screen.getByText("Organisation owners and admins manage Slack.")
    ).toBeTruthy()
    expect(go.calls).toHaveLength(0)
  })

  it("says plainly where the server runs no Slack app, for a 404 and for a 503", async () => {
    renderSettings({
      [`GET ${installPath}`]: () =>
        failure(404, "not_found", "resource not found"),
    })
    expect(await screen.findByText("Slack isn't available here.")).toBeTruthy()
    expect(screen.queryByRole("button", { name: /Add to Slack/ })).toBeNull()
    cleanup()

    renderSettings({
      [`GET ${installPath}`]: () =>
        failure(503, "unavailable", "did not answer"),
    })
    expect(await screen.findByText("Slack isn't available here.")).toBeTruthy()
  })

  it("says so when the session is not an admin's after all", async () => {
    renderSettings({
      [`GET ${installPath}`]: () => failure(403, "forbidden", "admins only"),
    })

    expect(
      await screen.findByText(
        "Only organisation owners and admins can manage Slack."
      )
    ).toBeTruthy()
  })

  it("says why it could not be loaded, and tries again", async () => {
    let broken = true
    renderSettings({
      [`GET ${installPath}`]: () =>
        broken
          ? failure(500, "internal", "internal server error")
          : notInstalled,
    })

    expect((await screen.findByRole("alert")).textContent).toContain(
      "Slack could not be loaded."
    )
    broken = false
    fireEvent.click(screen.getByRole("button", { name: "Retry" }))

    await screen.findByText("Slack isn't connected")
  })
})

describe("connecting Slack", () => {
  it("says it isn't connected, and sends the admin to Slack with a page to come back to", async () => {
    const assign = stubLocation("?org=org-1")
    const go = renderSettings({ [`GET ${installPath}`]: () => notInstalled })

    expect(screen.getByRole("status").textContent).toBe("Loading Slack…")
    await screen.findByText("Slack isn't connected")
    expect(screen.queryByText("High-priority channels")).toBeNull()
    expect(screen.queryByRole("button", { name: "Disconnect" })).toBeNull()
    fireEvent.click(screen.getByRole("button", { name: "Add to Slack" }))

    await waitFor(() => expect(assign).toHaveBeenCalledWith(consentPage))
    expect(assign).toHaveBeenCalledTimes(1)
    expect(go.count("GET", installPath)).toBe(1)
  })

  it("asks apps to send the browser back to Settings → Slack of this organisation", async () => {
    stubLocation()
    const requested: string[] = []
    renderSettings({
      [`GET ${installPath}`]: (_call, url) => {
        requested.push(url.searchParams.get("return_to") ?? "")
        return notInstalled
      },
    })

    await screen.findByText("Slack isn't connected")

    expect(requested).toEqual(["https://app.oryxa.in/?org=org-1&view=slack"])
  })

  it("reads a fresh link when the one it has is about to lapse", async () => {
    const assign = stubLocation()
    let reads = 0
    renderSettings({
      [`GET ${installPath}`]: () => {
        reads += 1
        return reads === 1
          ? { ...notInstalled, add_to_slack_expires_at: "2026-09-29T12:30:30Z" }
          : {
              ...notInstalled,
              add_to_slack_url:
                "https://slack.com/oauth/v2/authorize?state=fresh",
            }
      },
    })
    await screen.findByText("Slack isn't connected")

    fireEvent.click(screen.getByRole("button", { name: "Add to Slack" }))

    await waitFor(() =>
      expect(assign).toHaveBeenCalledWith(
        "https://slack.com/oauth/v2/authorize?state=fresh"
      )
    )
    expect(reads).toBe(2)
  })

  it("opens only Slack's own page, and says when it was not", async () => {
    const assign = stubLocation()
    renderSettings({
      [`GET ${installPath}`]: () => ({
        ...notInstalled,
        add_to_slack_url: "https://slack.com.evil.example/oauth?state=abc",
      }),
    })
    await screen.findByText("Slack isn't connected")

    fireEvent.click(screen.getByRole("button", { name: "Add to Slack" }))

    expect((await screen.findByRole("alert")).textContent).toBe(
      "That isn't a link to Slack, so it wasn't opened. Try again."
    )
    expect(assign).not.toHaveBeenCalled()
  })

  it("says how the install went when Slack sends the browser back", async () => {
    stubLocation("?org=org-1&view=slack&slack=connected")
    const replace = vi
      .spyOn(window.history, "replaceState")
      .mockImplementation(() => {})
    renderSettings({
      [`GET ${installPath}`]: () => active,
      [`GET ${channelsPath}`]: () => ({ channels: [] }),
    })

    await screen.findByText("Slack is connected.")
    expect(screen.getByText("Acme")).toBeTruthy()
    // The address is cleaned of it, so a reload doesn't say it again.
    await waitFor(() => expect(replace).toHaveBeenCalled())
    const cleaned = String(replace.mock.calls[0][2])
    expect(cleaned).toContain("view=slack")
    expect(cleaned).not.toContain("slack=connected")
    replace.mockRestore()
  })

  it("says why the install failed, in words, and never the code", async () => {
    stubLocation("?org=org-1&view=slack&slack=error&slack_error=team_bound")
    vi.spyOn(window.history, "replaceState").mockImplementation(() => {})
    renderSettings({ [`GET ${installPath}`]: () => notInstalled })

    const alert = await screen.findByRole("alert")
    expect(alert.textContent).toBe(
      "That Slack workspace is already connected to another organization."
    )
    expect(alert.textContent).not.toContain("team_bound")
    // The admin can add it again from here.
    expect(screen.getByRole("button", { name: "Add to Slack" })).toBeTruthy()
  })
})

describe("a connected workspace", () => {
  const connected = () =>
    renderSettings({
      [`GET ${installPath}`]: () => active,
      [`GET ${channelsPath}`]: () => ({ channels: [channel()] }),
    })

  it("shows the workspace, since when, and the channels", async () => {
    stubLocation()
    connected()

    expect(await screen.findByText("Acme")).toBeTruthy()
    expect(
      screen.getByText(
        `Connected today at ${clockTime(active.installed_at ?? "")}.`
      )
    ).toBeTruthy()
    expect(screen.getByText("Connected")).toBeTruthy()
    expect(screen.getByRole("button", { name: "Disconnect" })).toBeTruthy()
    expect(screen.queryByRole("button", { name: /Add to Slack/ })).toBeNull()
    await screen.findByRole("switch", { name: "High priority for #leadership" })
  })

  it("disconnects only after a confirm step, which can be cancelled", async () => {
    stubLocation()
    const go = connected()
    fireEvent.click(await screen.findByRole("button", { name: "Disconnect" }))

    expect(
      screen.getByText(/Disconnect Slack from Acme\? Oryxa's app is removed/)
    ).toBeTruthy()
    expect(go.count("DELETE", installPath)).toBe(0)
    fireEvent.click(screen.getByRole("button", { name: "Cancel" }))

    expect(screen.queryByText(/Oryxa's app is removed/)).toBeNull()
    expect(screen.getByRole("button", { name: "Disconnect" })).toBeTruthy()
    expect(go.count("DELETE", installPath)).toBe(0)
  })

  it("disconnects, and then says it is no longer connected", async () => {
    stubLocation()
    let ended = false
    const go = renderSettings({
      [`GET ${installPath}`]: () =>
        ended
          ? {
              ...notInstalled,
              status: "ended",
              team: active.team,
              end_reason: "removed_by_admin",
              installed_at: active.installed_at,
            }
          : active,
      [`GET ${channelsPath}`]: () => ({ channels: [channel()] }),
      [`DELETE ${installPath}`]: () => {
        ended = true
        return new Response(null, { status: 204 })
      },
    })
    fireEvent.click(await screen.findByRole("button", { name: "Disconnect" }))
    fireEvent.click(screen.getByRole("button", { name: "Disconnect" }))

    await screen.findByText("Slack is disconnected.")
    expect(go.count("DELETE", installPath)).toBe(1)
    await screen.findByText("An admin disconnected Slack.")
    expect(
      screen.getByRole("button", { name: "Add to Slack again" })
    ).toBeTruthy()
    expect(screen.queryByText("High-priority channels")).toBeNull()
  })

  it("says so when the disconnect was refused, and reads the install again", async () => {
    stubLocation()
    const go = renderSettings({
      [`GET ${installPath}`]: () => active,
      [`GET ${channelsPath}`]: () => ({ channels: [] }),
      [`DELETE ${installPath}`]: () =>
        failure(404, "not_found", "not installed"),
    })
    fireEvent.click(await screen.findByRole("button", { name: "Disconnect" }))
    fireEvent.click(screen.getByRole("button", { name: "Disconnect" }))

    expect((await screen.findByRole("alert")).textContent).toBe(
      "Slack wasn't connected."
    )
    await waitFor(() => expect(go.count("GET", installPath)).toBe(2))
  })

  it("says an ended install ended, and why", async () => {
    stubLocation()
    renderSettings({
      [`GET ${installPath}`]: () => ({
        ...notInstalled,
        status: "ended",
        team: active.team,
        end_reason: "app_uninstalled",
      }),
    })

    await screen.findByText("Someone removed the app from the Slack workspace.")
    expect(screen.getByText("Not connected")).toBeTruthy()
    expect(
      screen.getByRole("button", { name: "Add to Slack again" })
    ).toBeTruthy()
  })
})

describe("high-priority channels", () => {
  const marked = channel({
    channel_id: "C02",
    name: "finance",
    high_priority: true,
    marked_by: OWNER,
    marked_at: "2026-09-28T09:00:00Z",
  })

  function withChannels(
    channels: SlackChannel[],
    extra: Record<string, Route> = {}
  ) {
    stubLocation()
    return renderSettings({
      [`GET ${installPath}`]: () => active,
      [`GET ${channelsPath}`]: () => ({ channels }),
      ...extra,
    })
  }

  it("lists each private channel with where it stands", async () => {
    withChannels([
      channel(),
      marked,
      channel({
        channel_id: "C03",
        name: "partners",
        is_shared: true,
        markable: false,
      }),
    ])

    await screen.findByRole("switch", { name: "High priority for #leadership" })
    const switches = screen.getAllByRole("switch")
    expect(switches).toHaveLength(2)
    expect(
      screen.getByRole("switch", { name: "High priority for #finance" })
    ).toHaveProperty("ariaChecked", "true")
    expect(
      screen.getByRole("switch", { name: "High priority for #leadership" })
    ).toHaveProperty("ariaChecked", "false")
    expect(screen.getByText("Not marked.")).toBeTruthy()
    await screen.findByText(/Marked by Ada Lovelace yesterday\./)
    expect(
      screen.getByText(
        "Shared with another organisation, so it can't be marked."
      )
    ).toBeTruthy()
    // A shared channel has no switch to turn on.
    expect(
      screen.queryByRole("switch", { name: "High priority for #partners" })
    ).toBeNull()
  })

  it("asks before marking one, and marks exactly the channels named", async () => {
    const puts: unknown[] = []
    withChannels([channel(), marked], {
      [`PUT ${channelsPath}`]: (call) => {
        puts.push(call.body)
        return {
          channels: [
            channel({
              high_priority: true,
              marked_by: OWNER,
              marked_at: "2026-09-29T12:31:00Z",
            }),
            marked,
          ],
        }
      },
    })
    fireEvent.click(
      await screen.findByRole("switch", {
        name: "High priority for #leadership",
      })
    )

    expect(
      screen.getByText(
        /Answer questions in #leadership without asking each person they name\?/
      )
    ).toBeTruthy()
    expect(puts).toEqual([])
    fireEvent.click(screen.getByRole("button", { name: "Mark high priority" }))

    await screen.findByText("#leadership is high priority.")
    expect(puts).toEqual([{ high_priority: ["C02", "C01"] }])
    expect(
      screen.getByRole("switch", { name: "High priority for #leadership" })
    ).toHaveProperty("ariaChecked", "true")
    expect(
      screen.queryByText(/without asking each person they name/)
    ).toBeNull()
  })

  it("can be cancelled before anything is marked", async () => {
    const puts: unknown[] = []
    withChannels([channel()], {
      [`PUT ${channelsPath}`]: (call) => {
        puts.push(call.body)
        return { channels: [channel()] }
      },
    })
    fireEvent.click(
      await screen.findByRole("switch", {
        name: "High priority for #leadership",
      })
    )

    fireEvent.click(screen.getByRole("button", { name: "Cancel" }))

    expect(
      screen.queryByText(/without asking each person they name/)
    ).toBeNull()
    expect(puts).toEqual([])
  })

  it("unmarks at once, without asking, and leaves the others marked", async () => {
    const other = channel({
      channel_id: "C05",
      name: "legal",
      high_priority: true,
    })
    const puts: unknown[] = []
    withChannels([marked, other], {
      [`PUT ${channelsPath}`]: (call) => {
        puts.push(call.body)
        return {
          channels: [channel({ channel_id: "C02", name: "finance" }), other],
        }
      },
    })
    fireEvent.click(
      await screen.findByRole("switch", { name: "High priority for #finance" })
    )

    await screen.findByText("#finance is not high priority.")
    expect(puts).toEqual([{ high_priority: ["C05"] }])
    expect(
      screen.getByRole("switch", { name: "High priority for #finance" })
    ).toHaveProperty("ariaChecked", "false")
  })

  it("unmarks a channel Oryxa can no longer see, which has no switch", async () => {
    const stale = channel({
      channel_id: "C09",
      name: "old-plans",
      is_private: false,
      markable: false,
      high_priority: true,
    })
    const puts: unknown[] = []
    withChannels([channel(), stale], {
      [`PUT ${channelsPath}`]: (call) => {
        puts.push(call.body)
        return { channels: [channel()] }
      },
    })
    await screen.findByText(
      /Oryxa can no longer see this channel, or it is shared now/
    )
    expect(
      screen.queryByRole("switch", { name: "High priority for #old-plans" })
    ).toBeNull()

    fireEvent.click(screen.getByRole("button", { name: "Unmark" }))

    await screen.findByText("#old-plans is not high priority.")
    expect(puts).toEqual([{ high_priority: [] }])
    expect(screen.queryByRole("button", { name: "Unmark" })).toBeNull()
  })

  it("says why a mark was refused, and changes nothing", async () => {
    withChannels([channel()], {
      [`PUT ${channelsPath}`]: () =>
        failure(
          400,
          "invalid_request",
          "C01 is not a private channel shared with nobody, and only those can be marked high priority"
        ),
    })
    fireEvent.click(
      await screen.findByRole("switch", {
        name: "High priority for #leadership",
      })
    )
    fireEvent.click(screen.getByRole("button", { name: "Mark high priority" }))

    expect((await screen.findByRole("alert")).textContent).toContain(
      "only those can be marked high priority"
    )
    expect(
      screen.getByRole("switch", { name: "High priority for #leadership" })
    ).toHaveProperty("ariaChecked", "false")
  })

  it("says when the app is in no private channel yet", async () => {
    withChannels([])

    await screen.findByText(/The Oryxa app isn't in any private channel yet/)
    expect(screen.queryByRole("switch")).toBeNull()
  })

  it("says why the channels could not be loaded, and tries again", async () => {
    stubLocation()
    let broken = true
    renderSettings({
      [`GET ${installPath}`]: () => active,
      [`GET ${channelsPath}`]: () =>
        broken
          ? failure(500, "internal", "internal server error")
          : { channels: [channel()] },
    })

    const alert = await screen.findByRole("alert")
    expect(alert.textContent).toContain("Channels could not be loaded.")
    broken = false
    fireEvent.click(within(alert).getByRole("button", { name: "Retry" }))

    await screen.findByRole("switch", { name: "High priority for #leadership" })
  })

  it("explains what marking a channel does", async () => {
    withChannels([channel()])

    await screen.findByRole("switch", { name: "High priority for #leadership" })
    expect(
      screen.getByText(/answered without asking each person it names/)
    ).toBeTruthy()
    expect(
      screen.getByText(/Invite the Oryxa app to a channel in Slack first/)
    ).toBeTruthy()
  })
})
