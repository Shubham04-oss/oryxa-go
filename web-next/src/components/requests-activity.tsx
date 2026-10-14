// Activity, the third tab of Requests (DESIGN.md §3, "Requests"): what
// happened to the member's Slack asks, newest first and by day: asks made
// and received, how each was decided and where, bypasses, answers, access
// given and ended, and secrets opened. Names, states and times, never a
// question or an answer: the request an entry points to is the place for
// those. It is re-read on focus and on a timer, like the lists.
import { useState } from "react"
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query"
import {
  Ban,
  Check,
  KeyRound,
  MessageSquare,
  Reply,
  Timer,
  TimerOff,
} from "lucide-react"
import { AgentsNotice } from "./agents-parts"
import { agentsErrorMessage } from "./agents-format"
import { clockTime, dayLabel } from "./direct-message-format"
import {
  activityIcon,
  activitySentence,
  openableRows,
  repliedRequestIds,
} from "./requests-activity-format"
import type { ActivityIcon } from "./requests-activity-format"
import {
  activityQueryOptions,
  refreshRequests,
  usePeopleNames,
} from "./requests-view-queries"
import { shortName } from "./request-card-format"
import type { Reader } from "./request-card-format"
import { fullTime } from "./teamspace-format"
import { isLeaseLive, revokeAgentLease } from "@/lib/agents/agent-requests"
import type { AgentActivityEntry } from "@/lib/agents/agent-requests"
import { isUnavailableHere } from "@/lib/agents/policy"

function EntryIcon({ icon }: { icon: ActivityIcon }) {
  const props = { size: 14, strokeWidth: 1.8, "aria-hidden": true } as const
  switch (icon) {
    case "asked":
      return <MessageSquare {...props} />
    case "approved":
      return <Check {...props} />
    case "denied":
      return <Ban {...props} />
    case "answered":
      return <Reply {...props} />
    case "secret":
      return <KeyRound {...props} />
    case "access":
      return <Timer {...props} />
    case "access-ended":
      return <TimerOff {...props} />
  }
}

/** The entries grouped by the day they happened, in the order they came. */
function byDay(
  entries: readonly AgentActivityEntry[],
  now: number
): Array<{
  day: string
  rows: Array<{ entry: AgentActivityEntry; index: number }>
}> {
  const groups: Array<{
    day: string
    rows: Array<{ entry: AgentActivityEntry; index: number }>
  }> = []
  entries.forEach((entry, index) => {
    const day = dayLabel(entry.at, now)
    const last = groups.at(-1)
    if (last?.day === day) last.rows.push({ entry, index })
    else groups.push({ day, rows: [{ entry, index }] })
  })
  return groups
}

export function RequestsActivity({
  organizationId,
  viewerId,
  onOpenRequest,
}: {
  organizationId: string
  viewerId: string
  /** Opens a request on its card. */
  onOpenRequest: (requestId: string) => void
}) {
  const queryClient = useQueryClient()
  const people = usePeopleNames(organizationId, viewerId)
  const activity = useQuery(activityQueryOptions(organizationId))
  const [problem, setProblem] = useState("")
  const endAccess = useMutation({
    mutationFn: (leaseId: string) => revokeAgentLease(organizationId, leaseId),
    onSuccess: async () => {
      setProblem("")
      await refreshRequests(queryClient, organizationId)
    },
    onError: (error) =>
      setProblem(
        agentsErrorMessage(error, "Couldn't end that access. Try again.", {
          not_found: "That access has already ended.",
        })
      ),
  })

  if (activity.isPending || people.status === "loading") {
    return (
      <p className="workspace-agents-muted" role="status">
        Loading activity…
      </p>
    )
  }
  if (activity.isError) {
    const unavailable = isUnavailableHere(activity.error)
    return (
      <AgentsNotice
        title={
          unavailable
            ? "Activity isn't set up on this server."
            : agentsErrorMessage(activity.error, "Could not load activity.")
        }
        alert={!unavailable}
        action={
          !unavailable && (
            <button
              className="workspace-agents-button"
              type="button"
              onClick={() => void activity.refetch()}
            >
              Try again
            </button>
          )
        }
      />
    )
  }

  const entries = activity.data
  if (entries.length === 0) {
    return (
      <AgentsNotice title="No Slack activity yet.">
        Asks, decisions, bypasses, answers, access and secrets from Slack show
        up here. It lists names, states and times, never a question or an
        answer.
      </AgentsNotice>
    )
  }

  const now = Date.now()
  const reader: Reader = { viewerId, nameOf: people.nameOf, now }
  const openable = openableRows(entries)
  const replied = repliedRequestIds(entries)

  return (
    <div className="workspace-requests-activity">
      <p className="workspace-agents-note">
        Names, states and times only: open a request to read it.
      </p>
      {byDay(entries, now).map((group) => (
        <section key={group.day} aria-label={group.day}>
          <p className="workspace-eyebrow">{group.day}</p>
          <ul className="workspace-requests-activity-list">
            {group.rows.map(({ entry, index }) => {
              const lease = entry.lease
              const canEnd =
                entry.kind === "lease_given" &&
                entry.role === "owner" &&
                lease !== undefined &&
                isLeaseLive(lease, now)
              return (
                <li
                  className="workspace-requests-activity-row"
                  data-kind={entry.kind}
                  key={`${entry.kind}-${entry.at}-${entry.request_id ?? index}-${index}`}
                >
                  <span className="workspace-requests-activity-icon">
                    <EntryIcon icon={activityIcon(entry)} />
                  </span>
                  <span className="workspace-requests-activity-text">
                    {activitySentence(entry, reader, replied)}
                  </span>
                  <span className="workspace-requests-activity-actions">
                    {canEnd && (
                      <button
                        className="workspace-agent-pill"
                        type="button"
                        aria-label={`End ${shortName(reader, entry.with_user_id)}'s access`}
                        disabled={endAccess.isPending}
                        onClick={() => endAccess.mutate(lease.id)}
                      >
                        {endAccess.isPending && endAccess.variables === lease.id
                          ? "Ending…"
                          : "End access"}
                      </button>
                    )}
                    {entry.request_id && openable.has(index) && (
                      <button
                        className="workspace-agent-text-button"
                        type="button"
                        onClick={() => onOpenRequest(entry.request_id ?? "")}
                      >
                        View request
                      </button>
                    )}
                    <time dateTime={entry.at} title={fullTime(entry.at)}>
                      {clockTime(entry.at)}
                    </time>
                  </span>
                </li>
              )
            })}
          </ul>
        </section>
      ))}
      {problem && (
        <p className="workspace-agents-error" role="alert">
          {problem}
        </p>
      )}
    </div>
  )
}
