import { afterEach, describe, expect, it, vi } from "vitest"
import { ApiError } from "@/lib/api/client"
import {
  disconnectSlack,
  getSlackInstall,
  isSlackAuthorizeUrl,
  isSlackUnavailable,
  listSlackChannels,
  setSlackHighPriorityChannels,
  slackEndReasonMessage,
  slackInstallFailureMessage,
  slackInstallQueryOptions,
  slackKeys,
  slackReturnOf,
} from "./slack"
import type { SlackChannel, SlackInstall } from "./slack"

function json(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  })
}

function stubFetch(respond: (url: string, init: RequestInit) => Response) {
  const fetchMock = vi.fn(async (url: string, init: RequestInit) =>
    respond(url, init)
  )
  vi.stubGlobal("fetch", fetchMock)
  return fetchMock
}

function sent(fetchMock: ReturnType<typeof stubFetch>) {
  return fetchMock.mock.calls.map(([url, init]) => [
    init.method,
    url,
    init.body,
  ])
}

const install: SlackInstall = {
  status: "active",
  team: { id: "T01", name: "Acme" },
  bot_user_id: "U01",
  scopes: ["app_mentions:read", "chat:write"],
  installed_at: "2026-09-29T10:00:00Z",
  add_to_slack_url: "https://slack.com/oauth/v2/authorize?state=abc",
  add_to_slack_expires_at: "2026-09-29T10:10:00Z",
}

const channel: SlackChannel = {
  channel_id: "C01",
  name: "leadership",
  is_private: true,
  is_shared: false,
  markable: true,
  high_priority: false,
}

afterEach(() => vi.unstubAllGlobals())

describe("Slack install API", () => {
  it("reads the install, asking to be sent back to a page when there is one", async () => {
    const fetchMock = stubFetch(() => json(install))

    expect(await getSlackInstall("org-1")).toEqual(install)
    await getSlackInstall("org-1", {
      returnTo: "https://app.oryxa.in/?org=org-1&view=slack",
    })

    expect(sent(fetchMock)).toEqual([
      ["GET", "/v1/organizations/org-1/apps/slack/install", undefined],
      [
        "GET",
        "/v1/organizations/org-1/apps/slack/install?return_to=https%3A%2F%2Fapp.oryxa.in%2F%3Forg%3Dorg-1%26view%3Dslack",
        undefined,
      ],
    ])
  })

  it("disconnects with a DELETE", async () => {
    const fetchMock = stubFetch(() => new Response(null, { status: 204 }))

    await expect(disconnectSlack("org-1")).resolves.toBeUndefined()
    expect(sent(fetchMock)).toEqual([
      ["DELETE", "/v1/organizations/org-1/apps/slack/install", undefined],
    ])
  })

  it("keeps the codes of a refusal: a member is forbidden, no install is not found", async () => {
    const answers = [
      json({ error: { code: "forbidden", message: "admins only" } }, 403),
      json({ error: { code: "not_found", message: "not installed" } }, 404),
    ]
    stubFetch(() => answers.shift() ?? json({}))

    await expect(getSlackInstall("org-1")).rejects.toMatchObject({
      status: 403,
      code: "forbidden",
    })
    await expect(disconnectSlack("org-1")).rejects.toMatchObject({
      status: 404,
      code: "not_found",
    })
  })
})

describe("Slack channels API", () => {
  it("lists the channels", async () => {
    const fetchMock = stubFetch(() => json({ channels: [channel] }))

    expect(await listSlackChannels("org-1")).toEqual([channel])
    expect(sent(fetchMock)).toEqual([
      ["GET", "/v1/organizations/org-1/apps/slack/channels", undefined],
    ])
  })

  it("sets exactly the channels named, and [] unmarks every one", async () => {
    const marked = { ...channel, high_priority: true }
    const fetchMock = stubFetch(() => json({ channels: [marked] }))

    expect(await setSlackHighPriorityChannels("org-1", ["C01"])).toEqual([
      marked,
    ])
    await setSlackHighPriorityChannels("org-1", [])

    expect(sent(fetchMock)).toEqual([
      [
        "PUT",
        "/v1/organizations/org-1/apps/slack/channels",
        '{"high_priority":["C01"]}',
      ],
      [
        "PUT",
        "/v1/organizations/org-1/apps/slack/channels",
        '{"high_priority":[]}',
      ],
    ])
  })

  it("says why a set was refused", async () => {
    stubFetch(() =>
      json(
        {
          error: {
            code: "invalid_request",
            message: "the bot cannot see C09: invite it to the channel first",
          },
        },
        400
      )
    )

    await expect(
      setSlackHighPriorityChannels("org-1", ["C09"])
    ).rejects.toMatchObject({
      status: 400,
      message: "the bot cannot see C09: invite it to the channel first",
    })
  })
})

describe("Slack availability", () => {
  it("takes a 404 and a 503 for a server without Slack", () => {
    expect(isSlackUnavailable(new ApiError("not found", 404))).toBe(true)
    expect(isSlackUnavailable(new ApiError("down", 503, "unavailable"))).toBe(
      true
    )
    expect(isSlackUnavailable(new ApiError("admins only", 403))).toBe(false)
    expect(isSlackUnavailable(new ApiError("boom", 500))).toBe(false)
    expect(isSlackUnavailable(new Error("offline"))).toBe(false)
  })

  it("never re-reads the install on focus: every read makes a new link", () => {
    const options = slackInstallQueryOptions("org-1", "https://app/?a=b")

    expect(options.refetchOnWindowFocus).toBe(false)
    expect(options.staleTime).toBeGreaterThan(60_000)
    expect(options.queryKey.slice(0, 4)).toEqual(
      slackKeys.install("org-1").slice(0, 4)
    )
    expect(slackKeys.channels("org-1")[0]).toBe("apps")
  })

  it("lets only Slack's own https pages take the browser away", () => {
    expect(isSlackAuthorizeUrl(install.add_to_slack_url)).toBe(true)
    expect(isSlackAuthorizeUrl("https://acme.slack.com/oauth")).toBe(true)
    expect(isSlackAuthorizeUrl("http://slack.com/oauth")).toBe(false)
    expect(isSlackAuthorizeUrl("https://slack.com.evil.example/oauth")).toBe(
      false
    )
    expect(isSlackAuthorizeUrl("javascript:alert(1)")).toBe(false)
    expect(isSlackAuthorizeUrl("")).toBe(false)
  })
})

describe("coming back from Slack", () => {
  it("reads how the install ended from the page's address", () => {
    expect(slackReturnOf("?org=org-1&view=slack&slack=connected")).toEqual({
      status: "connected",
    })
    expect(
      slackReturnOf("?view=slack&slack=error&slack_error=team_bound")
    ).toEqual({ status: "error", reason: "team_bound" })
    expect(slackReturnOf("?slack=error")).toEqual({
      status: "error",
      reason: "",
    })
    expect(slackReturnOf("?org=org-1&view=slack")).toBeUndefined()
    expect(slackReturnOf("?slack=maybe")).toBeUndefined()
  })

  it("puts each reason an install can fail for in words, and never a code", () => {
    const reasons = [
      "state",
      "denied",
      "enterprise",
      "rotation",
      "scopes",
      "team_bound",
      "organization_bound",
      "unavailable",
      "slack_refused",
      "something_new",
    ]
    const words = reasons.map(slackInstallFailureMessage)

    expect(new Set(words).size).toBe(words.length)
    for (const sentence of words) {
      expect(sentence).toMatch(/\.$/)
      expect(sentence).not.toMatch(/_/)
    }
    expect(slackInstallFailureMessage("something_new")).toBe(
      "Slack wasn't connected. Try again."
    )
  })

  it("says why an install ended", () => {
    expect(slackEndReasonMessage("app_uninstalled")).toMatch(/removed the app/)
    expect(slackEndReasonMessage("tokens_revoked")).toMatch(/revoked/)
    expect(slackEndReasonMessage("removed_by_admin")).toMatch(/admin/)
    expect(slackEndReasonMessage("token_invalid")).toMatch(/no longer accepts/)
    expect(slackEndReasonMessage(undefined)).toBe("The connection ended.")
  })
})
