// Pure helpers that mirror the teamspace service's own checks
// (agents/primary/internal/teamspaces/service.go), so a page offers only
// what the server would accept. The server stays the authority.
import type { ManagedAgent } from "@/lib/agents/managed-agents"
import type { Teamspace, TeamspaceContribution } from "./client"

/** An agent the caller may publish to the ledger as. */
export type PublicationAuthor = {
  agent_id: string
  agent_name: string
  /** Their own primary agent, or one of their contributed specialists. */
  source: "whisperer" | "contribution"
}

/**
 * The agents `userId` may publish as, as teamspaces.publicationAuthor
 * decides: their whisperer, and each of their contributed specialists that
 * is active.
 */
export function publicationAuthors(
  space: Teamspace,
  userId: string
): PublicationAuthor[] {
  const whisperers = space.whisperers
    .filter((whisperer) => whisperer.user_id === userId)
    .map((whisperer) => ({
      agent_id: whisperer.agent_id,
      agent_name: whisperer.agent_name,
      source: "whisperer" as const,
    }))
  const contributions = space.contributions
    .filter(
      (contribution) =>
        contribution.owner_user_id === userId &&
        contribution.status === "active"
    )
    .map((contribution) => ({
      agent_id: contribution.agent_id,
      agent_name: contribution.name,
      source: "contribution" as const,
    }))
  return [...whisperers, ...contributions]
}

/**
 * Whether `userId` can publish shared work now: a team member, in an active
 * space whose organization agent is active, with an agent to publish as.
 */
export function canPublishSharedWork(
  space: Teamspace,
  userId: string
): boolean {
  return (
    space.is_member &&
    space.status === "active" &&
    space.organization_agent.status === "active" &&
    publicationAuthors(space, userId).length > 0
  )
}

/** Whether the caller can contribute a specialist: a team member, in an active space. */
export function canContribute(space: Teamspace): boolean {
  return space.is_member && space.status === "active"
}

/** The caller's specialists the space would accept: active, and not already contributed. */
export function contributionCandidates(
  agents: readonly ManagedAgent[],
  space: Teamspace
): ManagedAgent[] {
  const contributed = new Set(
    space.contributions.map((contribution) => contribution.agent_id)
  )
  return agents.filter(
    (agent) =>
      agent.kind === "specialist" &&
      agent.status === "active" &&
      !contributed.has(agent.id)
  )
}

/** Only a specialist's owner may change its role or delegation mode. */
export function canChangeContribution(
  contribution: TeamspaceContribution,
  userId: string
): boolean {
  return contribution.owner_user_id === userId
}

/** The owner may withdraw a contribution, and so may whoever manages the space. */
export function canRemoveContribution(
  space: Teamspace,
  contribution: TeamspaceContribution,
  userId: string
): boolean {
  return contribution.owner_user_id === userId || space.can_manage
}

/**
 * Whether to say anything in the space's room: a team member, while the
 * space is active. Only a person with a seat (a team member with an active
 * primary agent) may speak in it, and Go refuses a post in a paused space's
 * room 409 `teamspace_disabled` (a Go before read-only rooms, 400).
 */
export function canUseRoom(space: Teamspace): boolean {
  return space.is_member && space.status === "active"
}

/**
 * Whether to open the space's room: a team member, paused or not. Go reads
 * a paused space's room, read-only, and says `paused`; a Go before
 * read-only rooms refuses its reads 400, which the room shows as paused.
 */
export function canReadRoom(space: Teamspace): boolean {
  return space.is_member
}

// Resolving a path against a stand-in origin shows where a browser would
// really send it: "/\evil.example" and "/\t/evil.example" both leave it.
const firstParty = "https://first-party.invalid"

/**
 * An artifact address is untrusted: an agent wrote it. Only an HTTPS URL
 * (without credentials) or a path on this origin becomes a link; anything
 * else, including a path a browser would read as another host, stays text.
 * Returns the normalized address, or undefined.
 */
export function safeArtifactHref(uri: string): string | undefined {
  const value = uri.trim()
  if (value.startsWith("/")) {
    try {
      const url = new URL(value, firstParty)
      const path = `${url.pathname}${url.search}${url.hash}`
      // Normalizing can turn "/.//evil.example" into "//evil.example", which
      // as an href is another host again.
      if (url.origin !== firstParty || /^\/[/\\]/.test(path)) return undefined
      return path
    } catch {
      return undefined
    }
  }
  try {
    const url = new URL(value)
    if (url.protocol !== "https:" || url.username || url.password) {
      return undefined
    }
    return url.toString()
  } catch {
    return undefined
  }
}
