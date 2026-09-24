// Who is in a side conversation, from Go's teamspace (`GET …/teamspaces/{id}`,
// teamspaces.Space): the Worker reads it for its access check and hands it
// to the object, and the object reads it again when it needs to.
import type { SideRoster, SideRosterAgent, SideRosterPerson } from "./protocol"

/** The most contributed specialists a roster carries, so it stays small enough for a header. */
export const MAX_ROSTER_CONTRIBUTIONS = 30

const maxName = 200
const maxRole = 300

function text(value: unknown, limit: number): string | undefined {
  if (typeof value !== "string") return undefined
  const trimmed = value.trim()
  if (!trimmed) return undefined
  const characters = Array.from(trimmed)
  return characters.length <= limit
    ? trimmed
    : `${characters.slice(0, limit - 1).join("")}…`
}

/**
 * The roster of the conversation between `userA` and `userB` in Go's
 * teamspace `space`: its name, whether it is paused, the two people's
 * primary agents (their whisperer seats, which Go derives from the live
 * team directory, so a person is on the team exactly when they are listed),
 * and the active specialists either of them contributed. Nothing for an
 * answer that is not a teamspace.
 */
export function rosterOf(
  space: unknown,
  { userA, userB }: { userA: string; userB: string }
): SideRoster | undefined {
  if (typeof space !== "object" || space === null) return undefined
  const detail = space as {
    name?: unknown
    status?: unknown
    whisperers?: unknown
    contributions?: unknown
  }
  const name = typeof detail.name === "string" ? detail.name.trim() : undefined
  if (name === undefined || !Array.isArray(detail.whisperers)) return undefined
  const inIt = (userId: unknown): userId is string =>
    userId === userA || userId === userB

  const people: SideRosterPerson[] = []
  for (const seat of detail.whisperers as unknown[]) {
    if (typeof seat !== "object" || seat === null) continue
    const {
      user_id: userId,
      user_display_name: displayName,
      agent_id: agentId,
      agent_name: agentName,
    } = seat as Record<string, unknown>
    if (!inIt(userId) || typeof agentId !== "string" || !agentId) continue
    if (people.some((person) => person.userId === userId)) continue
    const shownName = text(displayName, maxName)
    people.push({
      userId,
      ...(shownName ? { displayName: shownName } : {}),
      agentId,
      agentName: text(agentName, maxName) ?? "Agent",
    })
  }

  const contributions: SideRosterAgent[] = []
  const offered = Array.isArray(detail.contributions)
    ? (detail.contributions as unknown[])
    : []
  for (const contribution of offered) {
    if (contributions.length >= MAX_ROSTER_CONTRIBUTIONS) break
    if (typeof contribution !== "object" || contribution === null) continue
    const {
      agent_id: agentId,
      owner_user_id: ownerId,
      name: agentName,
      role,
      delegation_mode: delegationMode,
      status,
    } = contribution as Record<string, unknown>
    if (
      !inIt(ownerId) ||
      typeof agentId !== "string" ||
      !agentId ||
      status !== "active" ||
      (delegationMode !== "request" && delegationMode !== "automatic")
    ) {
      continue
    }
    const shownRole = text(role, maxRole)
    contributions.push({
      agentId,
      ownerId,
      name: text(agentName, maxName) ?? "Specialist",
      ...(shownRole ? { role: shownRole } : {}),
      delegationMode,
    })
  }

  return {
    teamspaceName: text(name, maxName) ?? "",
    paused: detail.status === "disabled",
    people,
    contributions,
  }
}

/**
 * The agents of `userId` in the conversation a request may go to: their
 * primary agent first, then the specialists they contributed.
 */
export function agentsOf(
  roster: SideRoster,
  userId: string
): Array<{
  id: string
  name: string
  kind: "primary" | "specialist"
  approval: "owner" | "agent"
  role?: string
}> {
  const agents: ReturnType<typeof agentsOf> = []
  const person = roster.people.find((each) => each.userId === userId)
  if (person) {
    agents.push({
      id: person.agentId,
      name: person.agentName,
      kind: "primary",
      approval: "owner",
    })
  }
  for (const contribution of roster.contributions) {
    if (contribution.ownerId !== userId) continue
    agents.push({
      id: contribution.agentId,
      name: contribution.name,
      kind: "specialist",
      approval: contribution.delegationMode === "automatic" ? "agent" : "owner",
      ...(contribution.role ? { role: contribution.role } : {}),
    })
  }
  return agents
}
