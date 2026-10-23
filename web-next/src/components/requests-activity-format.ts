// What Activity says happened to a member's Slack asks, and who reached their
// things, one sentence an entry. An entry is names, states and times, never a
// question or an answer, and so is what is said of it: it never repeats a
// code, an id, or anything Go held back. The words follow the request card's
// (request-card-format.ts).
import { consentWords, shortName } from "./request-card-format"
import type { Reader } from "./request-card-format"
import { clockTime } from "./direct-message-format"
import type {
  AgentAccessDecision,
  AgentActivityEntry,
} from "@/lib/agents/agent-requests"

/** What sort of thing an entry is, for its icon. */
export type ActivityIcon =
  | "asked"
  | "approved"
  | "denied"
  | "answered"
  | "secret"
  | "access"
  | "access-ended"
  | "reached"
  | "changed"
  | "deleted"

/**
 * The names of the things an access entry points to, where the page knows
 * them: agents and collections by id. Anything it does not know is said by
 * its kind.
 */
export type ThingNames = {
  agentName?: (agentId: string) => string | undefined
  resourceName?: (kind: string, resourceId: string) => string | undefined
}

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
      // An owner who answered in their own words decided by doing so.
      if (entry.consent === "reply") {
        return owner
          ? `You replied to ${other}'s request${where}.`
          : `${other} replied to your request${where}.`
      }
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

/** What a resource is called when its name is not known. */
function kindWord(kind: string | undefined): string {
  switch (kind) {
    case "file":
      return "file"
    case "corpus":
      return "collection"
    case "document":
      return "document"
    case "source":
      return "source"
    default:
      return "item"
  }
}

/** What was done to it, by a person in their own session or by an agent. */
function actionVerb(action: string | undefined, byAgent: boolean): string {
  switch (action) {
    case "read":
      return byAgent ? "read" : "opened"
    case "search":
      return "searched"
    case "write":
      return "changed"
    case "delete":
      return "deleted"
    default:
      return "reached"
  }
}

/** A member's first name, or nothing when the members list can't say. */
function knownName(reader: Reader, userId: string | undefined) {
  if (!userId) return undefined
  return reader.nameOf(userId) ? shortName(reader, userId) : undefined
}

/** Who reached it, as a sentence's subject. */
function accessActor(
  access: Partial<AgentAccessDecision>,
  reader: Reader,
  things: ThingNames
): string {
  const yours = access.actor_user_id === reader.viewerId
  const person = yours ? "you" : knownName(reader, access.actor_user_id)
  if (!access.actor_agent_id) {
    if (yours) return "You"
    if (access.reason === "org_admin") {
      return person ? `An admin, ${person},` : "An admin"
    }
    return person ? person : "Someone"
  }
  const specialist = access.actor_agent_role === "specialist"
  const named = things.agentName?.(access.actor_agent_id)
  const agent = specialist
    ? named
      ? `specialist ${named}`
      : "specialist"
    : "agent"
  if (yours) return `Your ${agent}`
  if (person) return `${person}'s ${agent}`
  return specialist && named ? `A specialist, ${named},` : "An agent"
}

/** What it reached, as a sentence's object. */
function accessObject(
  access: Partial<AgentAccessDecision>,
  reader: Reader,
  things: ThingNames
): string {
  const named =
    access.resource_id &&
    things.resourceName?.(access.resource_kind ?? "", access.resource_id)
  if (named) return named
  const kind = kindWord(access.resource_kind)
  if (!access.owner_id) return `${/^[aeiou]/.test(kind) ? "an" : "a"} ${kind}`
  if (access.owner_id === reader.viewerId) return `your ${kind}`
  const owner = knownName(reader, access.owner_id)
  return owner ? `${owner}'s ${kind}` : `someone's ${kind}`
}

/** One access entry: who reached what. */
function accessSentence(
  access: Partial<AgentAccessDecision>,
  reader: Reader,
  things: ThingNames
): string {
  const verb = actionVerb(access.action, Boolean(access.actor_agent_id))
  return `${accessActor(access, reader, things)} ${verb} ${accessObject(access, reader, things)}.`
}

/**
 * Why an access entry was allowed, as a short phrase, or nothing for a rule
 * this page doesn't know.
 */
export function accessReason(
  entry: AgentActivityEntry,
  reader: Reader
): string | undefined {
  if (entry.kind !== "accessed") return undefined
  switch (entry.access?.reason) {
    case "owner":
      return entry.access.actor_user_id === reader.viewerId
        ? "your own"
        : "their own"
    case "org_admin":
      return "org admin"
    case "team_member":
      return "on the same team"
    case "team_admin":
      return "team admin"
    case "organization":
      return "shared with everyone"
    case "grant":
      return "allowed by its grant"
    default:
      return undefined
  }
}

/**
 * The requests whose owner answered in their own words, read from the
 * `decided` entries among `entries`: an `answered` entry says no more than
 * that an answer was given, and whose words it was is only in the decision.
 */
export function repliedRequestIds(
  entries: readonly AgentActivityEntry[]
): Set<string> {
  return new Set(
    entries.flatMap((entry) =>
      entry.kind === "decided" && entry.consent === "reply" && entry.request_id
        ? [entry.request_id]
        : []
    )
  )
}

/**
 * One entry of Activity, as a sentence, for the reader. `replied` is the
 * requests whose owner replied themselves (`repliedRequestIds`): their
 * answers were not their agent's.
 */
export function activitySentence(
  entry: AgentActivityEntry,
  reader: Reader,
  replied?: ReadonlySet<string>,
  things: ThingNames = {}
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
      if (entry.request_id && replied?.has(entry.request_id)) {
        return owner
          ? `Your reply reached ${other}.`
          : `${other}'s reply reached you.`
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
    case "accessed":
      return accessSentence(entry.access ?? {}, reader, things)
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
      if (entry.consent === "reply") return "answered"
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
    case "accessed":
      return entry.access?.action === "write"
        ? "changed"
        : entry.access?.action === "delete"
          ? "deleted"
          : "reached"
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
