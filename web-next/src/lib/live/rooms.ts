// The edge's names for two people's rooms (A18, edge/src/contracts
// directRoomId and sideRoomId; Go: DirectRoomID, SideRoomID). The edge takes
// no other id for these kinds. A member id it can't take (one holding "_")
// has no room here, so nothing live is offered for it.
import type { LiveRoom } from "./client"

const ID = /^[A-Za-z0-9_-]{1,128}$/

function pair(a: string, b: string): [string, string] | undefined {
  if (!a || !b || a === b || a.includes("_") || b.includes("_"))
    return undefined
  if (!ID.test(a) || !ID.test(b)) return undefined
  return a < b ? [a, b] : [b, a]
}

/** Two members' direct message: their ids sorted, joined by "_". */
export function directRoom(
  memberA: string,
  memberB: string
): LiveRoom | undefined {
  const members = pair(memberA, memberB)
  if (!members) return undefined
  const id = `${members[0]}_${members[1]}`
  return ID.test(id) ? { kind: "dm", id } : undefined
}

/** A side conversation: its teamspace, then the members sorted. */
export function sideRoom(
  teamspaceId: string,
  memberA: string,
  memberB: string
): LiveRoom | undefined {
  const members = pair(memberA, memberB)
  if (!members || !ID.test(teamspaceId)) return undefined
  const id = `${teamspaceId}_${members[0]}_${members[1]}`
  return ID.test(id) ? { kind: "side", id } : undefined
}
