// What Activity says happened to a member's Slack asks, one sentence an entry.
// An entry is names, states and times, never a question or an answer, and so
// is what is said of it: it never repeats a code, an id, or anything Go held
// back. The words follow the request card's (request-card-format.ts).
import { consentWords, shortName } from "./request-card-format"
import type { Reader } from "./request-card-format"
import { clockTime } from "./direct-message-format"
import type { AgentActivityEntry } from "@/lib/agents/agent-requests"

/** What sort of thing an entry is, for its icon. */
export type ActivityIcon =
  | "asked"
  | "approved"
  | "denied"
  | "answered"
  | "secret"
  | "access"
  | "access-ended"

/** Why a bypass applied, as a clause, from the point of view of the reader ("you" is the reader). */
function bypassWhy(
  reason: string | undefined,
  owner: boolean,
  other: string
): string | undefined {
  switch (reason) {
    case "priority_channel":
      return "it was asked in a high-priority channel"
    case "org_chart":
      return owner
        ? `${other} is above you in your organization's chart`
        : `you are above ${other} in your organization's chart`
    default:
      return undefined
  }
}

function decidedSentence(
  entry: AgentActivityEntry,
  owner: boolean,
  other: string
): string {
  const by = entry.decided_by
  const rejected = entry.decision === "rejected"
  switch (by?.kind) {
    case "lease":
      return owner
        ? `${other}'s follow-up was approved under the access you gave.`
        : `Your follow-up was approved under the access ${other} gave you.`
    case "bypass": {
      const why = bypassWhy(by.reason, owner, other)
      const without = owner ? `without asking you` : `without asking ${other}`
      const request = owner ? `${other}'s request` : "Your request"
      return `${request} was approved ${without}${why ? `: ${why}` : ""}.`
    }
    case "system":
      return by.reason === "answered_elsewhere"
        ? "Closed because another owner answered first."
        : "Oryxa closed the request."
    case "agent":
      return owner
        ? "Your agent decided the request."
        : `${other}'s agent decided your request.`
    default: {
      const where =
        by?.surface === "slack"
          ? " in Slack"
          : by?.surface === "web"
            ? " in Oryxa"
            : ""
      const consent = entry.consent ? ` ${consentWords(entry.consent)}` : ""
      if (owner) {
        return rejected
          ? `You denied ${other}'s request${where}.`
          : `You approved ${other}'s request${where}.${consent}`
      }
      return rejected
        ? `${other} denied your request${where}.`
        : `${other} approved your request${where}.${consent}`
    }
  }
}

/** One entry of Activity, as a sentence, for the reader. */
export function activitySentence(
  entry: AgentActivityEntry,
  reader: Reader
): string {
  const owner = entry.role === "owner"
  const other = shortName(reader, entry.with_user_id)
  switch (entry.kind) {
    case "asked":
      return owner
        ? `${other} asked you in Slack.`
        : `You asked ${other} in Slack.`
    case "decided":
      return decidedSentence(entry, owner, other)
    case "answered":
      if (entry.status === "failed") {
        return owner
          ? `Your agent couldn't answer ${other}.`
          : `${other}'s agent couldn't answer you.`
      }
      return owner
        ? `Your agent answered ${other}.`
        : `${other}'s agent answered you.`
    case "secret_viewed":
      return owner
        ? `${other} opened the secret you shared.`
        : `You opened the secret ${other} shared.`
    case "lease_given": {
      const until = entry.lease
        ? ` until ${clockTime(entry.lease.expires_at)}`
        : ""
      return owner
        ? `You gave ${other} access${until}.`
        : `${other} gave you access${until}.`
    }
    case "lease_revoked":
      return owner
        ? `You ended ${other}'s access.`
        : `${other} ended your access.`
    default:
      return "Something happened to a Slack request."
  }
}

/** Which icon an entry reads with. */
export function activityIcon(entry: AgentActivityEntry): ActivityIcon {
  switch (entry.kind) {
    case "asked":
      return "asked"
    case "decided":
      return entry.decision === "rejected" ||
        entry.decided_by?.kind === "system"
        ? "denied"
        : "approved"
    case "answered":
      return "answered"
    case "secret_viewed":
      return "secret"
    case "lease_given":
      return "access"
    case "lease_revoked":
      return "access-ended"
    default:
      return "asked"
  }
}

/**
 * Which rows offer to open their request: the oldest listed row of each
 * request (its `asked` row, when it is listed), so a request that has four
 * rows is offered once. Entries are newest first.
 */
export function openableRows(
  entries: readonly AgentActivityEntry[]
): Set<number> {
  const seen = new Set<string>()
  const rows = new Set<number>()
  for (let index = entries.length - 1; index >= 0; index -= 1) {
    const id = entries[index].request_id
    if (id && !seen.has(id)) {
      seen.add(id)
      rows.add(index)
    }
  }
  return rows
}
