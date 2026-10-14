// Where a page reaches a side conversation's object, on the app's own
// origin: the Worker (src/server.ts) serves it at /agents/{class}/{name},
// behind its checks with Go. The object itself is a socket
// (useSideConversation); this is only for asking why one would not open.
import { sideConversationName } from "./names"

export { checkConversationSocket } from "@/lib/dms/client"

/** A side conversation's object: `/agents/side-conversation-agent/{org~teamspace~userA~userB}`. */
export function sideConversationPath(
  organizationId: string,
  teamspaceId: string,
  userA: string,
  userB: string
): string {
  return `/agents/side-conversation-agent/${encodeURIComponent(
    sideConversationName(organizationId, teamspaceId, userA, userB)
  )}`
}
