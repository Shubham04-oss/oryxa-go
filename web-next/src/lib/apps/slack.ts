// An organization's install of Oryxa's Slack app: whether it is connected, the
// link that connects it, disconnecting it, and which private channels an admin
// marked high priority. The routes are apps' admin routes for the Slack
// surface (apps/internal/httpserver/slack.go, apps/internal/slackbot): each
// takes an organization admin's own session and refuses anyone else with 403.
// Where the deployment runs no Slack surface the routes are simply not there
// (404), and where apps cannot serve them it is a 503: `isSlackUnavailable`.
import { queryOptions } from "@tanstack/react-query"
import { ApiError, apiRequest } from "@/lib/api/client"
import { appsQueryKey } from "./client"

/** `not_installed`, `active`, or `ended` (Slack or an admin took the app away). */
export type SlackInstallStatus = "not_installed" | "active" | "ended"

export type SlackTeam = { id: string; name?: string }

/**
 * An organization's Slack install as its admins see it, with the link to
 * install it or to install it again (`slackbot.InstallView`).
 */
export type SlackInstall = {
  status: SlackInstallStatus
  team?: SlackTeam
  bot_user_id?: string
  /** What the install was granted. */
  scopes?: string[]
  installed_at?: string
  ended_at?: string
  /**
   * Why an ended install ended: `app_uninstalled` (removed in Slack),
   * `tokens_revoked`, `removed_by_admin` (disconnected here) or
   * `token_invalid`.
   */
  end_reason?: string
  /**
   * Slack's consent page for the app, with a state good for one use and for
   * `add_to_slack_expires_at` (ten minutes). Every read makes a new one.
   */
  add_to_slack_url: string
  add_to_slack_expires_at: string
}

/**
 * A private channel the bot can see, or one that stays marked after the bot
 * left it (`slackbot.AdminChannel`). Only a channel that is private and shared
 * with nobody is `markable`.
 */
export type SlackChannel = {
  channel_id: string
  name: string
  is_private: boolean
  is_shared: boolean
  markable: boolean
  high_priority: boolean
  /** The organization member who marked it, and when. */
  marked_by?: string
  marked_at?: string
}

function slackPath(organizationId: string, ...segments: string[]): string {
  return `/v1/organizations/${[organizationId, "apps", "slack", ...segments]
    .map(encodeURIComponent)
    .join("/")}`
}

/**
 * `GET …/apps/slack/install[?return_to=]`: the install's status and a fresh
 * Add to Slack link. With `returnTo` (an absolute URL under one of the web
 * app's origins) the browser is sent there when the install ends, with
 * `slack=connected`, or `slack=error` and `slack_error` saying why
 * (`slackReturnOf`).
 */
export function getSlackInstall(
  organizationId: string,
  { returnTo, signal }: { returnTo?: string; signal?: AbortSignal } = {}
): Promise<SlackInstall> {
  const path = slackPath(organizationId, "install")
  return apiRequest<SlackInstall>(
    returnTo ? `${path}?${new URLSearchParams({ return_to: returnTo })}` : path,
    { signal }
  )
}

/**
 * `DELETE …/apps/slack/install`: removes the app from the workspace, best
 * effort, and ends the install whether or not Slack confirms. 404 where no
 * install is active.
 */
export function disconnectSlack(organizationId: string): Promise<void> {
  return apiRequest(slackPath(organizationId, "install"), { method: "DELETE" })
}

/**
 * `GET …/apps/slack/channels`: the private channels the bot is in, with the
 * ones marked high priority, by name. A marked channel the bot cannot see any
 * more is listed too, so it can be unmarked. 409 `not_connected` without an
 * active install.
 */
export async function listSlackChannels(
  organizationId: string,
  { signal }: { signal?: AbortSignal } = {}
): Promise<SlackChannel[]> {
  const result = await apiRequest<{ channels: SlackChannel[] }>(
    slackPath(organizationId, "channels"),
    { signal }
  )
  return result.channels
}

/**
 * `PUT …/apps/slack/channels`: makes exactly the channels named the
 * organization's high priority ones (at most 100), and unmarks every other.
 * `[]` unmarks every one. Each must be private and shared with nobody, checked
 * with Slack now: one that is not, or that the bot cannot see, refuses the
 * whole change (400 `invalid_request`, whose message names the channel).
 * Answers the channels as they now stand.
 */
export async function setSlackHighPriorityChannels(
  organizationId: string,
  channelIds: readonly string[]
): Promise<SlackChannel[]> {
  const result = await apiRequest<{ channels: SlackChannel[] }>(
    slackPath(organizationId, "channels"),
    { method: "PUT", body: { high_priority: [...channelIds] } }
  )
  return result.channels
}

/** Whether an error says this server does not run the Slack surface (404), or apps cannot serve it now (503). */
export function isSlackUnavailable(error: unknown): boolean {
  return (
    error instanceof ApiError && (error.status === 404 || error.status === 503)
  )
}

export const slackKeys = {
  all: (organizationId: string) =>
    [...appsQueryKey(organizationId), "slack"] as const,
  install: (organizationId: string) =>
    [...slackKeys.all(organizationId), "install"] as const,
  channels: (organizationId: string) =>
    [...slackKeys.all(organizationId), "channels"] as const,
  link: (organizationId: string) =>
    [...slackKeys.all(organizationId), "link"] as const,
}

/**
 * `getSlackInstall`, read once and kept a few minutes: every read makes a
 * new single-use link, so it is never re-read on focus or on a timer. A
 * return page apps does not allow (a build served from another origin) is a
 * 400: the install is read again without one, so it can still be connected,
 * and Slack's callback then answers in JSON instead of sending the browser
 * back.
 */
export function slackInstallQueryOptions(
  organizationId: string,
  returnTo?: string
) {
  return queryOptions({
    queryKey: [...slackKeys.install(organizationId), returnTo ?? ""] as const,
    queryFn: async ({ signal }) => {
      try {
        return await getSlackInstall(organizationId, { returnTo, signal })
      } catch (error) {
        if (returnTo && error instanceof ApiError && error.status === 400) {
          return getSlackInstall(organizationId, { signal })
        }
        throw error
      }
    },
    staleTime: 5 * 60_000,
    refetchOnWindowFocus: false,
    retry: false,
  })
}

/** `listSlackChannels`. */
export function slackChannelsQueryOptions(organizationId: string) {
  return queryOptions({
    queryKey: slackKeys.channels(organizationId),
    queryFn: ({ signal }) => listSlackChannels(organizationId, { signal }),
    retry: false,
  })
}

/**
 * Whether a link may take the browser away from the app: Slack's own consent
 * page, over https. The link comes from apps, and is still checked before the
 * page leaves for it.
 */
export function isSlackAuthorizeUrl(value: string): boolean {
  try {
    const url = new URL(value)
    return (
      url.protocol === "https:" &&
      (url.hostname === "slack.com" || url.hostname.endsWith(".slack.com"))
    )
  } catch {
    return false
  }
}

/** How an install that ended on Slack's side or an admin's reads, as one sentence. */
export function slackEndReasonMessage(reason: string | undefined): string {
  switch (reason) {
    case "app_uninstalled":
      return "Someone removed the app from the Slack workspace."
    case "tokens_revoked":
      return "Slack revoked the app's access."
    case "removed_by_admin":
      return "An admin disconnected Slack."
    case "token_invalid":
      return "Slack no longer accepts the app's access."
    default:
      return "The connection ended."
  }
}

/** What the browser came back from Slack with: it connected, or it didn't, and why. */
export type SlackReturn =
  { status: "connected" } | { status: "error"; reason: string }

/**
 * The outcome apps' install callback adds to the page it sends the browser
 * back to: `?slack=connected`, or `?slack=error&slack_error=<reason>`.
 * Nothing when the page was not reached that way.
 */
export function slackReturnOf(search: string): SlackReturn | undefined {
  const params = new URLSearchParams(search)
  switch (params.get("slack")) {
    case "connected":
      return { status: "connected" }
    case "error":
      return { status: "error", reason: params.get("slack_error") ?? "" }
    default:
      return undefined
  }
}

/** Why an install did not finish (`slackbot.Failure*`), in words an admin can act on. */
export function slackInstallFailureMessage(reason: string): string {
  switch (reason) {
    case "state":
      return "That Add to Slack link had expired or was already used. Add it again."
    case "denied":
      return "Slack wasn't given permission, so nothing was connected."
    case "enterprise":
      return "That is an Enterprise Grid workspace, which Oryxa can't connect yet."
    case "rotation":
      return "The Slack app rotates its tokens, which Oryxa doesn't support yet. Whoever runs the Slack app needs to turn that off."
    case "scopes":
      return "Slack didn't grant every permission Oryxa asks for. Add it again and allow them all."
    case "team_bound":
      return "That Slack workspace is already connected to another organization."
    case "organization_bound":
      return "This organization is already connected to a different Slack workspace. Disconnect that one first."
    case "unavailable":
      return "Slack or Oryxa didn't answer. Try again in a minute."
    case "slack_refused":
      return "Slack refused the install. Try again."
    default:
      return "Slack wasn't connected. Try again."
  }
}

// ---------------------------------------------------------------------------
// A member's own Slack account (apps/internal/slackbot/link.go): linking it
// lets what they ask and decide in Slack count as them. Every member, not
// only admins, links their own; an agent's session is refused.
// ---------------------------------------------------------------------------

/** `slackbot.LinkView`: whether the caller's Slack account is linked. */
export type SlackLink = {
  linked: boolean
  team_id?: string
  slack_user_id?: string
  linked_at?: string
}

/** `slackbot.LinkStart`: where Sign in with Slack starts, good once. */
export type SlackLinkStart = { sign_in_url: string; expires_at: string }

/** `GET …/apps/slack/link`: the caller's own link. */
export function getSlackLink(
  organizationId: string,
  { signal }: { signal?: AbortSignal } = {}
): Promise<SlackLink> {
  return apiRequest<SlackLink>(slackPath(organizationId, "link"), { signal })
}

/**
 * `POST …/apps/slack/link`: a Sign in with Slack link for the workspace this
 * organization installed. With `returnTo`, the browser comes back there with
 * `slack_link=linked`, or `slack_link=error` and `slack_link_error`
 * (`slackLinkReturnOf`). 409 `not_connected` without an active install.
 */
export function startSlackLink(
  organizationId: string,
  returnTo?: string
): Promise<SlackLinkStart> {
  return apiRequest<SlackLinkStart>(slackPath(organizationId, "link"), {
    method: "POST",
    body: returnTo ? { return_to: returnTo } : {},
  })
}

/** `DELETE …/apps/slack/link`: unlinks the caller's Slack account (204). */
export function unlinkSlack(organizationId: string): Promise<void> {
  return apiRequest(slackPath(organizationId, "link"), { method: "DELETE" })
}

export function slackLinkQueryOptions(organizationId: string) {
  return queryOptions({
    queryKey: slackKeys.link(organizationId),
    queryFn: ({ signal }) => getSlackLink(organizationId, { signal }),
    retry: (count, error) => !isSlackUnavailable(error) && count < 2,
  })
}

/** What the browser came back from Sign in with Slack with. */
export type SlackLinkReturn =
  { status: "linked" } | { status: "error"; reason: string }

/** `?slack_link=linked`, or `?slack_link=error&slack_link_error=<reason>`. */
export function slackLinkReturnOf(search: string): SlackLinkReturn | undefined {
  const params = new URLSearchParams(search)
  switch (params.get("slack_link")) {
    case "linked":
      return { status: "linked" }
    case "error":
      return { status: "error", reason: params.get("slack_link_error") ?? "" }
    default:
      return undefined
  }
}

/** Why a link didn't finish (`slackbot.Failure*`), in words a member can act on. */
export function slackLinkFailureMessage(reason: string): string {
  switch (reason) {
    case "state":
      return "That link had expired or was already used. Link again."
    case "denied":
      return "Slack wasn't given permission, so nothing was linked."
    case "wrong_team":
      return "You signed in to a different Slack workspace from the one connected here. Link again and choose that one."
    case "taken":
      return "That Slack account is already linked to someone else in Oryxa."
    case "session":
      return "Your Oryxa session had ended. Sign in again, then link again."
    case "unavailable":
      return "Slack or Oryxa didn't answer. Try again in a minute."
    case "slack_refused":
      return "Slack refused the sign-in. Try again."
    default:
      return "Your Slack account wasn't linked. Try again."
  }
}
