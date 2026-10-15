// Settings → Slack (DESIGN.md §3, "Requests", and Settings): an organization's
// own connection to Oryxa's Slack app, for its owners and admins. Whether it
// is connected; Add to Slack, which sends the admin to Slack's consent page
// and back; Disconnect, behind a confirm step; and, once connected, which of
// the private channels the app is in are high priority: a question asked in
// one is answered without asking each person it names. Where the server runs
// no Slack app the page says so, and does nothing.
import { useEffect, useId, useState } from "react"
import type { ReactNode } from "react"
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query"
import { Lock } from "lucide-react"
import "./slack-settings.css"
import { clockTime, dayLabel } from "./direct-message-format"
import { usePeopleNames } from "./requests-view-queries"
import type { Organization, Role } from "@/lib/auth/client"
import { ApiError } from "@/lib/api/client"
import { appsErrorMessage } from "@/lib/apps/client"
import {
  disconnectSlack,
  isSlackAuthorizeUrl,
  isSlackUnavailable,
  setSlackHighPriorityChannels,
  slackChannelsQueryOptions,
  slackEndReasonMessage,
  slackInstallFailureMessage,
  slackInstallQueryOptions,
  slackKeys,
  slackLinkFailureMessage,
  slackLinkQueryOptions,
  slackLinkReturnOf,
  slackReturnOf,
  startSlackLink,
  unlinkSlack,
} from "@/lib/apps/slack"
import type { SlackChannel, SlackInstall } from "@/lib/apps/slack"

/** Owners and admins manage the Slack connection; apps refuses anyone else. */
function canManageSlack(role: Role | undefined): boolean {
  return role === "owner" || role === "admin"
}

/**
 * Slack's mark, in its own colours (Slack's brand assets): the app's logo
 * wherever Slack is named. Decorative, since its name is always beside it.
 */
export function SlackMark({ size = 18 }: { size?: number }) {
  return (
    <svg
      width={size}
      height={size}
      viewBox="0 0 122.8 122.8"
      aria-hidden="true"
      focusable="false"
    >
      <path
        d="M25.8 77.6c0 7.1-5.8 12.9-12.9 12.9S0 84.7 0 77.6s5.8-12.9 12.9-12.9h12.9v12.9zm6.5 0c0-7.1 5.8-12.9 12.9-12.9s12.9 5.8 12.9 12.9v32.3c0 7.1-5.8 12.9-12.9 12.9s-12.9-5.8-12.9-12.9V77.6z"
        fill="#e01e5a"
      />
      <path
        d="M45.2 25.8c-7.1 0-12.9-5.8-12.9-12.9S38.1 0 45.2 0s12.9 5.8 12.9 12.9v12.9H45.2zm0 6.5c7.1 0 12.9 5.8 12.9 12.9s-5.8 12.9-12.9 12.9H12.9C5.8 58.1 0 52.3 0 45.2s5.8-12.9 12.9-12.9h32.3z"
        fill="#36c5f0"
      />
      <path
        d="M97 45.2c0-7.1 5.8-12.9 12.9-12.9s12.9 5.8 12.9 12.9-5.8 12.9-12.9 12.9H97V45.2zm-6.5 0c0 7.1-5.8 12.9-12.9 12.9s-12.9-5.8-12.9-12.9V12.9C64.7 5.8 70.5 0 77.6 0s12.9 5.8 12.9 12.9v32.3z"
        fill="#2eb67d"
      />
      <path
        d="M77.6 97c7.1 0 12.9 5.8 12.9 12.9s-5.8 12.9-12.9 12.9-12.9-5.8-12.9-12.9V97h12.9zm0-6.5c-7.1 0-12.9-5.8-12.9-12.9s5.8-12.9 12.9-12.9h32.3c7.1 0 12.9 5.8 12.9 12.9s-5.8 12.9-12.9 12.9H77.6z"
        fill="#ecb22e"
      />
    </svg>
  )
}

/** The page Slack sends the admin's browser back to: Settings → Slack of this organization. */
function slackReturnUrl(organizationId: string): string {
  const url = new URL("/", window.location.origin)
  url.searchParams.set("org", organizationId)
  url.searchParams.set("view", "slack")
  return url.toString()
}

/** When, to follow "Marked": "today", "yesterday", or "on Mon, 28 Sep". */
function dayPhrase(iso: string | undefined, now: number): string {
  if (!iso) return ""
  const day = dayLabel(iso, now)
  if (day === "") return ""
  if (day === "Today") return " today"
  if (day === "Yesterday") return " yesterday"
  return ` on ${day}`
}

const slackCopy = {
  not_connected: "Slack isn't connected any more.",
  forbidden: "Only organisation owners and admins can manage Slack.",
  unavailable: "Slack or Oryxa didn't answer. Try again in a minute.",
} as const

function slackError(error: unknown, fallback: string): string {
  if (error instanceof ApiError && error.code) {
    const copy = (slackCopy as Record<string, string>)[error.code]
    if (copy) return copy
  }
  return appsErrorMessage(error, fallback)
}

/**
 * Settings → Slack. Every member links their own Slack account here; owners
 * and admins also manage the organisation's connection, and anyone else is
 * told who does (nothing of the install is read for them). Signed out, in
 * the dev-only layout preview, there is nothing to read.
 */
export function WorkspaceSlackSettings({
  organization,
  role,
  preview,
}: {
  organization?: Organization
  role?: Role
  preview: boolean
}) {
  return (
    <div className="workspace-settings-account">
      {preview ? (
        <SlackSection>
          <p className="workspace-settings-hint">
            Layout preview · Sign in to manage Slack.
          </p>
        </SlackSection>
      ) : !organization ? (
        <SlackSection>
          <p className="workspace-settings-hint">
            Select an organisation first.
          </p>
        </SlackSection>
      ) : (
        <>
          <SlackAccountLink
            key={`link-${organization.id}`}
            organizationId={organization.id}
          />
          {canManageSlack(role) ? (
            <SlackSettings
              key={organization.id}
              organizationId={organization.id}
            />
          ) : (
            <SlackSection>
              <p className="workspace-settings-hint">
                Organisation owners and admins manage Slack.
              </p>
            </SlackSection>
          )}
        </>
      )}
    </div>
  )
}

/**
 * The member's own Slack account. Linked, what they ask and decide in Slack
 * counts as them; unlinked, Slack can't tell who they are. Hidden where the
 * deployment runs no Slack app (404) or apps can't answer (503).
 */
function SlackAccountLink({ organizationId }: { organizationId: string }) {
  const queryClient = useQueryClient()
  // How the browser came back from Sign in with Slack, read once; the
  // address is cleaned of it after, so a reload doesn't say it again.
  const [returned] = useState(() => slackLinkReturnOf(window.location.search))
  const [problem, setProblem] = useState("")
  const [notice, setNotice] = useState("")
  const [opening, setOpening] = useState(false)
  const [confirming, setConfirming] = useState(false)
  const link = useQuery(slackLinkQueryOptions(organizationId))

  useEffect(() => {
    if (!returned) return
    const url = new URL(window.location.href)
    url.searchParams.delete("slack_link")
    url.searchParams.delete("slack_link_error")
    try {
      window.history.replaceState(window.history.state, "", url)
    } catch {
      // The address keeps what Slack added to it: nothing depends on it going.
    }
  }, [returned])

  const unlink = useMutation({
    mutationFn: () => unlinkSlack(organizationId),
    onSuccess: async () => {
      setConfirming(false)
      setProblem("")
      setNotice("Your Slack account is unlinked.")
      await queryClient.invalidateQueries({
        queryKey: slackKeys.link(organizationId),
      })
    },
    onError: (error) => {
      setConfirming(false)
      setNotice("")
      setProblem(slackError(error, "Could not unlink it. Try again."))
    },
  })

  async function startLink() {
    if (opening) return
    setOpening(true)
    setProblem("")
    setNotice("")
    try {
      const start = await startSlackLink(
        organizationId,
        slackReturnUrl(organizationId)
      )
      if (!isSlackAuthorizeUrl(start.sign_in_url)) {
        setProblem(
          "That isn't a link to Slack, so it wasn't opened. Try again."
        )
        return
      }
      window.location.assign(start.sign_in_url)
    } catch (error) {
      setProblem(
        error instanceof ApiError && error.code === "not_connected"
          ? "Slack isn't connected to this organisation yet. An owner or admin connects it first."
          : slackError(error, "Could not start linking. Try again.")
      )
    } finally {
      setOpening(false)
    }
  }

  if (link.isError && isSlackUnavailable(link.error)) return null
  const about =
    "Link your Slack account so Oryxa knows the messages are yours: what you ask, approve or answer in Slack counts as you."
  return (
    <SlackSection
      label="Your Slack account"
      title="Your Slack account"
      about={about}
    >
      {returned?.status === "linked" && (
        <p className="workspace-settings-notice" role="status">
          Your Slack account is linked.
        </p>
      )}
      {returned?.status === "error" && (
        <p className="workspace-settings-error" role="alert">
          {slackLinkFailureMessage(returned.reason)}
        </p>
      )}
      {link.isPending ? (
        <p className="workspace-settings-hint" role="status">
          Checking your Slack account…
        </p>
      ) : link.isError ? (
        <p className="workspace-settings-error" role="alert">
          {slackError(link.error, "Could not check your Slack account.")}{" "}
          <button
            type="button"
            className="underline"
            onClick={() => void link.refetch()}
          >
            Try again
          </button>
        </p>
      ) : (
        <>
          <p className="workspace-settings-hint">
            {link.data.linked
              ? `Linked${link.data.linked_at ? ` since ${new Date(link.data.linked_at).toLocaleDateString(undefined, { day: "numeric", month: "short", year: "numeric" })}` : ""}.`
              : "Not linked. Slack can't tell your messages apart from anyone else's."}
          </p>
          {notice && (
            <p className="workspace-settings-notice" role="status">
              {notice}
            </p>
          )}
          {problem && (
            <p className="workspace-settings-error" role="alert">
              {problem}
            </p>
          )}
          {confirming ? (
            <div className="workspace-settings-confirm">
              <p>
                Unlink your Slack account? What you ask or approve in Slack
                stops counting as you until you link it again.
              </p>
              <button
                type="button"
                disabled={unlink.isPending}
                onClick={() => setConfirming(false)}
              >
                Cancel
              </button>
              <button
                type="button"
                disabled={unlink.isPending}
                onClick={() => unlink.mutate()}
              >
                {unlink.isPending ? "Unlinking…" : "Unlink"}
              </button>
            </div>
          ) : (
            <div className="workspace-slack-actions">
              {link.data.linked ? (
                <button
                  type="button"
                  className="workspace-settings-action"
                  onClick={() => {
                    setNotice("")
                    setProblem("")
                    setConfirming(true)
                  }}
                >
                  Unlink
                </button>
              ) : (
                <button
                  type="button"
                  className="workspace-settings-action workspace-slack-add"
                  disabled={opening}
                  onClick={() => void startLink()}
                >
                  <SlackMark size={16} />
                  {opening ? "Opening Slack…" : "Link your Slack account"}
                </button>
              )}
            </div>
          )}
        </>
      )}
    </SlackSection>
  )
}

function SlackSection({
  children,
  label = "Slack",
  title = "Slack",
  about,
}: {
  children: ReactNode
  label?: string
  title?: string
  about?: string
}) {
  return (
    <section className="workspace-settings-section" aria-label={label}>
      <div className="workspace-settings-section-title">
        <h3>{title}</h3>
        {about && <p>{about}</p>}
      </div>
      {children}
    </section>
  )
}

function SlackSettings({ organizationId }: { organizationId: string }) {
  const queryClient = useQueryClient()
  // How the browser came back from Slack, from the page's address. Read once;
  // the address is cleaned of it after, so a reload doesn't say it again.
  const [returned] = useState(() => slackReturnOf(window.location.search))
  const [returnTo] = useState(() => slackReturnUrl(organizationId))
  const [notice, setNotice] = useState("")
  const [problem, setProblem] = useState("")
  const [adding, setAdding] = useState(false)
  const [confirmingDisconnect, setConfirmingDisconnect] = useState(false)
  const install = useQuery(slackInstallQueryOptions(organizationId, returnTo))

  useEffect(() => {
    if (!returned) return
    const url = new URL(window.location.href)
    url.searchParams.delete("slack")
    url.searchParams.delete("slack_error")
    try {
      window.history.replaceState(window.history.state, "", url)
    } catch {
      // The address keeps what Slack added to it: nothing depends on it going.
    }
  }, [returned])

  const disconnect = useMutation({
    mutationFn: () => disconnectSlack(organizationId),
    onSuccess: async () => {
      setConfirmingDisconnect(false)
      setProblem("")
      setNotice("Slack is disconnected.")
      queryClient.removeQueries({
        queryKey: slackKeys.channels(organizationId),
      })
      await queryClient.invalidateQueries({
        queryKey: slackKeys.install(organizationId),
      })
    },
    onError: async (error) => {
      setConfirmingDisconnect(false)
      setNotice("")
      setProblem(
        error instanceof ApiError && error.status === 404
          ? "Slack wasn't connected."
          : slackError(error, "Could not disconnect Slack. Try again.")
      )
      await queryClient.invalidateQueries({
        queryKey: slackKeys.install(organizationId),
      })
    },
  })

  /** Sends the admin to Slack's consent page, on a link that has not lapsed. */
  async function addToSlack() {
    if (adding) return
    setAdding(true)
    setProblem("")
    setNotice("")
    try {
      let read = install.data
      // The link is good for ten minutes and for one use: an old one is read
      // again before the page leaves for it.
      if (
        !read ||
        Date.parse(read.add_to_slack_expires_at) - Date.now() < 60_000
      ) {
        read = await queryClient.fetchQuery({
          ...slackInstallQueryOptions(organizationId, returnTo),
          staleTime: 0,
        })
      }
      if (!isSlackAuthorizeUrl(read.add_to_slack_url)) {
        setProblem(
          "That isn't a link to Slack, so it wasn't opened. Try again."
        )
        return
      }
      window.location.assign(read.add_to_slack_url)
    } catch (error) {
      setProblem(slackError(error, "Could not start the install. Try again."))
    } finally {
      setAdding(false)
    }
  }

  if (install.isPending) {
    return (
      <SlackSection>
        <p className="workspace-settings-hint" role="status">
          Loading Slack…
        </p>
      </SlackSection>
    )
  }
  if (install.isError) {
    const denied =
      install.error instanceof ApiError && install.error.status === 403
    return (
      <SlackSection>
        {isSlackUnavailable(install.error) ? (
          <p className="workspace-settings-hint">
            <strong>Slack isn't available here.</strong> This server doesn't run
            Oryxa's Slack app.
          </p>
        ) : denied ? (
          <p className="workspace-settings-hint">{slackCopy.forbidden}</p>
        ) : (
          <p className="workspace-settings-error" role="alert">
            {slackError(install.error, "Slack could not be loaded.")}{" "}
            <button
              type="button"
              className="underline"
              onClick={() => void install.refetch()}
            >
              Retry
            </button>
          </p>
        )}
      </SlackSection>
    )
  }

  const data = install.data
  const active = data.status === "active"
  return (
    <>
      <SlackSection about="Connect your Slack workspace so people can mention Oryxa, name a teammate, and ask that teammate's agent a question. The person asked chooses what their agent shares, in Slack or in Requests, before anything is answered.">
        {returned?.status === "connected" && (
          <p className="workspace-settings-notice" role="status">
            Slack is connected.
          </p>
        )}
        {returned?.status === "error" && (
          <p className="workspace-settings-error" role="alert">
            {slackInstallFailureMessage(returned.reason)}
          </p>
        )}
        <InstallCard install={data} />
        {notice && (
          <p className="workspace-settings-notice" role="status">
            {notice}
          </p>
        )}
        {problem && (
          <p className="workspace-settings-error" role="alert">
            {problem}
          </p>
        )}
        {confirmingDisconnect ? (
          <div className="workspace-settings-confirm">
            <p>
              Disconnect Slack{data.team?.name ? ` from ${data.team.name}` : ""}
              ? Oryxa's app is removed from the workspace, so nobody can ask
              through Slack until you add it again. Requests already made stay
              in Requests.
            </p>
            <button
              type="button"
              disabled={disconnect.isPending}
              onClick={() => setConfirmingDisconnect(false)}
            >
              Cancel
            </button>
            <button
              type="button"
              disabled={disconnect.isPending}
              onClick={() => disconnect.mutate()}
            >
              {disconnect.isPending ? "Disconnecting…" : "Disconnect"}
            </button>
          </div>
        ) : (
          <div className="workspace-slack-actions">
            {active ? (
              <button
                type="button"
                className="workspace-settings-action"
                onClick={() => {
                  setNotice("")
                  setProblem("")
                  setConfirmingDisconnect(true)
                }}
              >
                Disconnect
              </button>
            ) : (
              <button
                type="button"
                className="workspace-settings-action workspace-slack-add"
                disabled={adding}
                onClick={() => void addToSlack()}
              >
                <SlackMark size={16} />
                {adding
                  ? "Opening Slack…"
                  : data.status === "ended"
                    ? "Add to Slack again"
                    : "Add to Slack"}
              </button>
            )}
          </div>
        )}
      </SlackSection>
      {active && <SlackChannels organizationId={organizationId} />}
    </>
  )
}

/** Where the install stands: the workspace, and since when, or why it ended. */
function InstallCard({ install }: { install: SlackInstall }) {
  const now = Date.now()
  const active = install.status === "active"
  const name = install.team?.name || "Slack workspace"
  return (
    <div className="workspace-slack-card">
      <span className="workspace-tool-logo">
        <SlackMark size={23} />
      </span>
      <div>
        {install.status === "not_installed" ? (
          <>
            <strong>Slack isn't connected</strong>
            <small>Add Oryxa to a workspace you administer.</small>
          </>
        ) : (
          <>
            <strong>{name}</strong>
            <small>
              {active
                ? `Connected${dayPhrase(install.installed_at, now)}${
                    install.installed_at
                      ? ` at ${clockTime(install.installed_at)}`
                      : ""
                  }.`
                : slackEndReasonMessage(install.end_reason)}
            </small>
          </>
        )}
      </div>
      {install.status !== "not_installed" && (
        <span className="workspace-slack-status" data-status={install.status}>
          {active ? "Connected" : "Not connected"}
        </span>
      )}
    </div>
  )
}

/** A channel's name as a person reads it in Slack. */
function channelName(channel: SlackChannel): string {
  return channel.name ? `#${channel.name}` : "A channel Oryxa can't see"
}

/**
 * The private channels the app is in, each with a switch for high priority.
 * Turning one on asks first, since a question in it is then answered without
 * asking each person it names. Only a private channel no other organization
 * shares can be marked; one that stays marked after Oryxa lost sight of it, or
 * after it was shared, can only be unmarked.
 */
function SlackChannels({ organizationId }: { organizationId: string }) {
  const queryClient = useQueryClient()
  const people = usePeopleNames(organizationId)
  const channels = useQuery(slackChannelsQueryOptions(organizationId))
  const [confirming, setConfirming] = useState<string | null>(null)
  const [saving, setSaving] = useState<{ id: string; on: boolean } | null>(null)
  const [notice, setNotice] = useState("")
  const [problem, setProblem] = useState("")
  const headingId = useId()

  const update = useMutation({
    mutationFn: (input: { ids: string[]; done: string }) =>
      setSlackHighPriorityChannels(organizationId, input.ids),
    onSuccess: (listed, input) => {
      queryClient.setQueryData(slackKeys.channels(organizationId), listed)
      setNotice(input.done)
      setProblem("")
    },
    onError: async (error) => {
      setNotice("")
      setProblem(slackError(error, "Could not save that. Try again."))
      if (error instanceof ApiError && error.status === 409) {
        await queryClient.invalidateQueries({
          queryKey: slackKeys.install(organizationId),
        })
      }
    },
    onSettled: () => setSaving(null),
  })

  const section = (body: ReactNode) => (
    <SlackSection
      label="High-priority channels"
      title="High-priority channels"
      about="In a private channel you mark, a question is answered without asking each person it names, and the answer is posted in the thread. Each person is told at once, and it shows in Requests → Activity. Only private channels that no other organisation shares can be marked, and Oryxa checks that again each time. Invite the Oryxa app to a channel in Slack first."
    >
      {body}
    </SlackSection>
  )

  if (channels.isPending) {
    return section(
      <p className="workspace-settings-hint" role="status">
        Loading channels…
      </p>
    )
  }
  if (channels.isError) {
    return section(
      isSlackUnavailable(channels.error) ? (
        <p className="workspace-settings-hint">
          <strong>Slack isn't available here.</strong>
        </p>
      ) : (
        <p className="workspace-settings-error" role="alert">
          {slackError(channels.error, "Channels could not be loaded.")}{" "}
          <button
            type="button"
            className="underline"
            onClick={() => void channels.refetch()}
          >
            Retry
          </button>
        </p>
      )
    )
  }

  const listed = channels.data
  const marked = listed
    .filter((channel) => channel.high_priority)
    .map((channel) => channel.channel_id)
  const now = Date.now()

  function mark(channel: SlackChannel, on: boolean) {
    setConfirming(null)
    setSaving({ id: channel.channel_id, on })
    update.mutate({
      ids: on
        ? [...marked, channel.channel_id]
        : marked.filter((id) => id !== channel.channel_id),
      done: on
        ? `${channelName(channel)} is high priority.`
        : `${channelName(channel)} is not high priority.`,
    })
  }

  if (listed.length === 0) {
    return section(
      <p className="workspace-settings-hint">
        The Oryxa app isn't in any private channel yet. Invite it to one in
        Slack, and it appears here.
      </p>
    )
  }

  return section(
    <>
      <div role="list" aria-labelledby={headingId}>
        <span className="sr-only" id={headingId}>
          Private channels the app is in
        </span>
        {listed.map((channel) => {
          const on =
            saving?.id === channel.channel_id
              ? saving.on
              : channel.high_priority
          const busy = update.isPending
          const marker = channel.marked_by
            ? people.nameOf(channel.marked_by)
            : undefined
          return (
            <div role="listitem" key={channel.channel_id}>
              <div className="workspace-settings-row">
                <div>
                  <strong>
                    {channel.is_private && (
                      <Lock
                        size={12}
                        strokeWidth={1.8}
                        aria-label="Private"
                        className="workspace-slack-lock"
                      />
                    )}
                    {channelName(channel)}
                  </strong>
                  <small>
                    {channel.high_priority && channel.markable
                      ? `Marked${marker ? ` by ${marker}` : ""}${dayPhrase(channel.marked_at, now)}.`
                      : channel.high_priority
                        ? "Oryxa can no longer see this channel, or it is shared now, so the mark does nothing. Unmark it."
                        : channel.markable
                          ? "Not marked."
                          : "Shared with another organisation, so it can't be marked."}
                  </small>
                </div>
                {channel.markable ? (
                  <button
                    type="button"
                    role="switch"
                    aria-label={`High priority for ${channelName(channel)}`}
                    aria-checked={on}
                    className="workspace-settings-switch"
                    disabled={busy}
                    onClick={() =>
                      on
                        ? mark(channel, false)
                        : setConfirming(channel.channel_id)
                    }
                  >
                    <span />
                  </button>
                ) : (
                  channel.high_priority && (
                    <button
                      type="button"
                      disabled={busy}
                      onClick={() => mark(channel, false)}
                    >
                      Unmark
                    </button>
                  )
                )}
              </div>
              {confirming === channel.channel_id && (
                <div className="workspace-settings-confirm">
                  <p>
                    Answer questions in {channelName(channel)} without asking
                    each person they name? Answers are posted in the thread, and
                    each person is told. You can turn this off any time.
                  </p>
                  <button type="button" onClick={() => setConfirming(null)}>
                    Cancel
                  </button>
                  <button
                    type="button"
                    disabled={busy}
                    onClick={() => mark(channel, true)}
                  >
                    Mark high priority
                  </button>
                </div>
              )}
            </div>
          )
        })}
      </div>
      {notice && (
        <p className="workspace-settings-notice" role="status">
          {notice}
        </p>
      )}
      {problem && (
        <p className="workspace-settings-error" role="alert">
          {problem}
        </p>
      )}
    </>
  )
}
