// The names of the Durable Objects behind direct messages (REALTIME.md): a
// page builds them, and the Worker (src/server.ts) and the objects parse them
// back before trusting them. A name holds ids only, joined by `~`.

/** The DM object's class and binding, useAgent's `agent`: the Worker serves it at `/agents/direct-message-agent/{name}`. */
export const DIRECT_MESSAGE_AGENT = "DirectMessageAgent"

/** The inbox object's class and binding: `/agents/inbox-agent/{name}`. */
export const INBOX_AGENT = "InboxAgent"

// An organization's or a person's id as a name may hold it. Nothing else
// fits, so a name always splits back into the same ids, and a name that only
// reads the same once URL-decoded (`%7E` for `~`) is refused.
const nameId = /^[\w-]{1,128}$/

/** Whether `value` may stand in a name: 1 to 128 of `A-Z a-z 0-9 _ -`. */
export function isNameId(value: unknown): value is string {
  return typeof value === "string" && nameId.test(value)
}

/**
 * The DM between two people, `{organization}~{userA}~{userB}`, the two user
 * ids sorted (by UTF-16 code unit, which for these ASCII ids is byte order),
 * so both people reach the same object whoever opens it. It is for two
 * different people: the same id twice makes a name no object answers to
 * (`parseDirectMessageName` refuses it).
 */
export function directMessageName(
  organizationId: string,
  userA: string,
  userB: string
): string {
  const [first, second] = userA < userB ? [userA, userB] : [userB, userA]
  return `${organizationId}~${first}~${second}`
}

/** The ids of a DM's name: the organization and its two people, sorted. */
export type DirectMessageIds = {
  organizationId: string
  userA: string
  userB: string
}

/**
 * The ids a DM's name was made from, or nothing for a name
 * `directMessageName` could not have made: three ids, the two people
 * different and in order.
 */
export function parseDirectMessageName(
  name: string
): DirectMessageIds | undefined {
  const parts = name.split("~")
  if (parts.length !== 3) return undefined
  const [organizationId, userA, userB] = parts
  if (!isNameId(organizationId) || !isNameId(userA) || !isNameId(userB)) {
    return undefined
  }
  // Sorted, which also means different.
  if (!(userA < userB)) return undefined
  return { organizationId, userA, userB }
}

/** The other person in a DM, or nothing when `userId` is not one of its two. */
export function peerIn(
  ids: Pick<DirectMessageIds, "userA" | "userB">,
  userId: string
): string | undefined {
  if (userId === ids.userA) return ids.userB
  if (userId === ids.userB) return ids.userA
  return undefined
}

/**
 * A side conversation (REALTIME.md), `{organization}~{teamspace}~{userA}~{userB}`,
 * the user ids sorted as a DM's are. Its object comes later; an inbox already
 * lists one by this name.
 */
export function sideConversationName(
  organizationId: string,
  teamspaceId: string,
  userA: string,
  userB: string
): string {
  const [first, second] = userA < userB ? [userA, userB] : [userB, userA]
  return `${organizationId}~${teamspaceId}~${first}~${second}`
}

/** The ids of a side conversation's name, or nothing for one `sideConversationName` could not have made. */
export function parseSideConversationName(
  name: string
): (DirectMessageIds & { teamspaceId: string }) | undefined {
  const parts = name.split("~")
  if (parts.length !== 4) return undefined
  const [organizationId, teamspaceId, userA, userB] = parts
  if (
    !isNameId(organizationId) ||
    !isNameId(teamspaceId) ||
    !isNameId(userA) ||
    !isNameId(userB)
  ) {
    return undefined
  }
  if (!(userA < userB)) return undefined
  return { organizationId, teamspaceId, userA, userB }
}

/** A member's inbox in one organization, `{organization}~{user}`: their conversation list. */
export function inboxName(organizationId: string, userId: string): string {
  return `${organizationId}~${userId}`
}

/** The ids an inbox's name was made from, or nothing for a name `inboxName` could not have made. */
export function parseInboxName(
  name: string
): { organizationId: string; userId: string } | undefined {
  const parts = name.split("~")
  if (parts.length !== 2) return undefined
  const [organizationId, userId] = parts
  if (!isNameId(organizationId) || !isNameId(userId)) return undefined
  return { organizationId, userId }
}

/**
 * The organization's registry of inboxes, named after the organization
 * alone. No page reaches it: only the Worker's purge and the inboxes do.
 */
export function registryName(organizationId: string): string {
  return organizationId
}
