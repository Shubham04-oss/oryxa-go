// Activity, the third tab of Requests (DESIGN.md §3, "Requests"): what
// happened to the member's Slack asks, newest first and by day: asks made
// and received, how each was decided and where, bypasses, answers, access
// given and ended, and secrets opened; and, from the access decision log, who
// reached the member's things and what their own agents reached, with the rule
// that let them. Names, states and times, never a question or an answer: the
// request an entry points to is the place for those. It is re-read on focus
// and on a timer, like the lists.
import { useMemo, useState } from "react"
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query"
import {
  Ban,
  Check,
  KeyRound,
  Eye,
  MessageSquare,
  PenLine,
  Reply,
  Timer,
  TimerOff,
  Trash2,
} from "lucide-react"
import { AgentsNotice } from "./agents-parts"
import { agentsErrorMessage } from "./agents-format"
import { clockTime, dayLabel } from "./direct-message-format"
import {
  accessReason,
  activityIcon,
  activitySentence,
  openableRows,
  repliedRequestIds,
} from "./requests-activity-format"
import type { ActivityIcon, ThingNames } from "./requests-activity-format"
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
import { agentsQueryOptions } from "@/lib/agents/queries"
import { corporaQueryOptions } from "@/lib/rag/client"

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
    case "reached":
      return <Eye {...props} />
    case "changed":
      return <PenLine {...props} />
    case "deleted":
      return <Trash2 {...props} />
  }
}

/**
 * The names of the agents and collections access entries point to, from the
 * lists the page can read in one call each, and only once there is an entry
 * to name. A list that can't be read leaves those entries saying the kind.
 */
function useThingNames(
  organizationId: string,
  entries: readonly AgentActivityEntry[] | undefined
): ThingNames {
  const accesses = (entries ?? []).flatMap((entry) =>
    entry.kind === "accessed" && entry.access ? [entry.access] : []
  )
  const agents = useQuery({
    ...agentsQueryOptions(organizationId),
    enabled: accesses.some((access) => access.actor_agent_id),
    retry: false,
  })
  const corpora = useQuery({
    ...corporaQueryOptions(organizationId),
    enabled: accesses.some((access) => access.resource_kind === "corpus"),
    retry: false,
  })
  return useMemo(() => {
    const agentNames = new Map(
      (agents.data ?? []).map((agent) => [agent.id, agent.name.trim()])
    )
    const corpusNames = new Map(
      (corpora.data ?? []).map((corpus) => [corpus.id, corpus.name.trim()])
    )
    return {
      agentName: (id) => agentNames.get(id) || undefined,
      resourceName: (kind, id) =>
        kind === "corpus" ? corpusNames.get(id) || undefined : undefined,
    }
  }, [agents.data, corpora.data])
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
  const things = useThingNames(organizationId, activity.data)
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
              const reason = accessReason(entry, reader)
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
                    {activitySentence(entry, reader, replied, things)}
                    {reason && (
                      <span className="workspace-requests-activity-reason">
                        {" · "}
                        {reason}
                      </span>
                    )}
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
