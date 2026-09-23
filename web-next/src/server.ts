import handler from "@tanstack/react-start/server-entry"
import { getAgentByName, routeAgentRequest } from "agents"
import { parseThreadAgentName } from "./lib/threads/chat-messages"
import { ThreadChatAgent, chatRequestHeaders } from "./thread-chat-agent"
import type { ThreadChatEnv } from "./thread-chat-agent"

// The Worker (wrangler.jsonc `main`). It serves the app through TanStack
// Start, and puts the primary chat's Durable Object behind a check with Go.

export interface Env extends ThreadChatEnv {
  ThreadChatAgent: DurableObjectNamespace<ThreadChatAgent>
}

export { ThreadChatAgent }

/**
 * Everything a page may ask of a thread's chat object: its socket (and the
 * DELETE) at `/agents/thread-chat-agent/{organization}~{thread}`, and the
 * stored transcript at `.../get-messages`. `routeAgentRequest` names the
 * object after the raw path segment, so the name is checked as it arrives:
 * ids are only word characters and "-", which no client percent-encodes, and
 * a percent-encoded name would otherwise reach a second object for the same
 * thread.
 */
const chatPath = /^\/agents\/thread-chat-agent\/([^/]+)(\/get-messages)?\/?$/

/** An error in Go's shape, which the app's `requestError` reads. */
function failure(
  status: number,
  code: string,
  message: string,
  headers?: HeadersInit
): Response {
  return Response.json({ error: { code, message } }, { status, headers })
}

/** The statuses of Go's access check the page can act on, passed on as they are. */
const passedOnStatuses: Record<number, { code: string; message: string }> = {
  401: { code: "unauthorized", message: "Sign in to open this conversation." },
  403: { code: "forbidden", message: "You cannot open this conversation." },
  404: { code: "not_found", message: "This conversation no longer exists." },
  503: { code: "unavailable", message: "The agent service is unavailable." },
}

/**
 * Go's refusal of the access check. Sign in again, no access, gone and
 * unavailable keep their status, and Go's own code and words when it sent
 * them; any other status is Go failing, which the page sees as a bad gateway.
 */
async function refusedByGo(response: Response): Promise<Response> {
  const passedOn = passedOnStatuses[response.status] as
    { code: string; message: string } | undefined
  if (!passedOn) {
    await response.body?.cancel().catch(() => undefined)
    return failure(
      502,
      "bad_gateway",
      "The agent service could not check this conversation."
    )
  }
  try {
    const body: { error?: unknown } = await response.json()
    if (body.error) return Response.json(body, { status: response.status })
  } catch {
    // Not Go's JSON: a proxy in between answered.
  }
  return failure(response.status, passedOn.code, passedOn.message)
}

async function threadChat(
  request: Request,
  env: Env,
  url: URL,
  rawName: string,
  isMessages: boolean
): Promise<Response> {
  const ids = parseThreadAgentName(rawName)
  if (!ids) {
    return failure(400, "invalid_request", "Invalid conversation.")
  }
  const isWebSocket =
    request.headers.get("Upgrade")?.toLowerCase() === "websocket"
  const isDelete = request.method === "DELETE"
  if (
    !(request.method === "GET" || isDelete) ||
    (isMessages && (isDelete || isWebSocket))
  ) {
    return failure(405, "method_not_allowed", "Method not allowed.", {
      Allow: isMessages ? "GET" : "GET, DELETE",
    })
  }
  // A cookie rides along on a cross-site socket or DELETE; only a page of
  // this app may open one or delete a conversation.
  const origin = request.headers.get("Origin")
  if ((isWebSocket || isDelete) && origin !== url.origin) {
    return failure(403, "forbidden", "Origin not allowed.")
  }
  const cookie = request.headers.get("Cookie")
  if (!cookie) {
    return failure(401, "unauthorized", "Sign in to open this conversation.")
  }

  const threadUrl = new URL(
    `/v1/organizations/${encodeURIComponent(ids.organizationId)}/threads/${encodeURIComponent(ids.threadId)}`,
    env.ORYXA_API_ORIGIN
  )

  if (isDelete) {
    // Go deletes the thread first, and decides whether the member may: a
    // refusal leaves the transcript with the thread it belongs to.
    let deleted: Response
    try {
      deleted = await fetch(threadUrl, {
        method: "DELETE",
        headers: { Cookie: cookie, Origin: url.origin },
      })
    } catch {
      return failure(503, "unavailable", "The agent service is unavailable.")
    }
    if (!deleted.ok) return deleted
    // The thread is gone. A transcript left behind could no longer be
    // opened (the check below now answers 404), and a retry could not reach
    // it either, so a purge that fails is logged rather than reported.
    try {
      const agent = await getAgentByName(env.ThreadChatAgent, rawName)
      await agent.purgeTranscript()
    } catch (error) {
      console.error("thread chat: could not purge a deleted thread", error)
    }
    return deleted
  }

  // The member may reach the object only for a thread Go lets them read.
  let access: Response
  try {
    access = await fetch(threadUrl, { headers: { Cookie: cookie } })
  } catch {
    return failure(503, "unavailable", "The agent service is unavailable.")
  }
  if (!access.ok) return refusedByGo(access)
  // Only the status matters; the thread's history is not read here.
  await access.body?.cancel().catch(() => undefined)

  const headers = new Headers(request.headers)
  headers.set(chatRequestHeaders.cookie, cookie)
  headers.set(chatRequestHeaders.organization, ids.organizationId)
  headers.set(chatRequestHeaders.thread, ids.threadId)
  let routed: Response | null
  try {
    routed = await routeAgentRequest(new Request(request, { headers }), env)
  } catch (error) {
    console.error("thread chat: could not reach the Durable Object", error)
    return failure(
      503,
      "unavailable",
      "Your primary agent's conversation is unavailable."
    )
  }
  return routed ?? failure(404, "not_found", "Conversation agent unavailable.")
}

export default {
  async fetch(request, env) {
    const url = new URL(request.url)
    const match = chatPath.exec(url.pathname)
    if (match) {
      return threadChat(request, env, url, match[1], Boolean(match[2]))
    }
    if (url.pathname.startsWith("/agents/")) {
      return failure(404, "not_found", "Not found.")
    }
    return handler.fetch(request)
  },
} satisfies ExportedHandler<Env>
