// Where a page reaches the direct messages' objects, on the app's own
// origin: the Worker (src/server.ts) serves them at /agents/{class}/{name},
// behind its checks with Go. The objects themselves are sockets (useInbox,
// useDirectMessages); this is only for asking why one would not open.
import { apiRequest } from "@/lib/api/client"
import { directMessageName, inboxName } from "./names"

/** A DM's object: `/agents/direct-message-agent/{org~userA~userB}`. */
export function directMessagePath(
  organizationId: string,
  userA: string,
  userB: string
): string {
  return `/agents/direct-message-agent/${encodeURIComponent(
    directMessageName(organizationId, userA, userB)
  )}`
}

/** A member's inbox: `/agents/inbox-agent/{org~user}`. */
export function inboxPath(organizationId: string, userId: string): string {
  return `/agents/inbox-agent/${encodeURIComponent(
    inboxName(organizationId, userId)
  )}`
}

/**
 * GET of an object's path: asks the Worker whether the member's socket
 * would be let in, since a browser is never told why an upgrade was
 * refused. The Worker makes the socket's own check with Go and answers
 * with its refusal (401, 403 `not_participant`, 404 `peer_not_found` or
 * Go's 404, 503), or lets it through to the object, which answers 204.
 * Through `apiFetch`, so an expired session is refreshed first. Throws the
 * refusal as an `ApiError`.
 */
export async function checkConversationSocket(path: string): Promise<void> {
  await apiRequest(path)
}
