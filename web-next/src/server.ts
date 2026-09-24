import handler from "@tanstack/react-start/server-entry"
import { getAgentByName, routeAgentRequest } from "agents"
import { parseSpecialistAgentName } from "./lib/specialists/chat-messages"
import { parseThreadAgentName } from "./lib/threads/chat-messages"
import {
  SpecialistChatAgent,
  specialistRequestHeaders,
} from "./specialist-chat-agent"
import { ThreadChatAgent, chatRequestHeaders } from "./thread-chat-agent"
import type { ThreadChatEnv } from "./thread-chat-agent"

// The Worker (wrangler.jsonc `main`). It serves the app through TanStack
// Start, and puts every realtime Durable Object (REALTIME.md) behind a check
// with Go: one entry each in `realtimeObjects` below.

export interface Env extends ThreadChatEnv {
  ThreadChatAgent: DurableObjectNamespace<ThreadChatAgent>
  SpecialistChatAgent: DurableObjectNamespace<SpecialistChatAgent>
}

export { SpecialistChatAgent, ThreadChatAgent }

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

/** Go failing to answer an access check, as the page sees it. */
function badGateway(): Response {
  return failure(
    502,
    "bad_gateway",
    "The agent service could not check this conversation."
  )
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
    return badGateway()
  }
  try {
    const body: { error?: unknown } = await response.json()
    if (body.error) return Response.json(body, { status: response.status })
  } catch {
    // Not Go's JSON: a proxy in between answered.
  }
  return failure(response.status, passedOn.code, passedOn.message)
}

/**
 * Asks Go for `path` (GET, from `ORYXA_API_ORIGIN`) with the member's cookie,
 * for an access check. Resolves nothing when the member may go on, or the
 * refusal to send: Go's own (`refusedByGo`), 503 when Go cannot be reached,
 * or whatever `read` makes of an answer Go accepted. Without `read`, only
 * the status matters and the body is not read.
 */
type AskGo = (
  path: string,
  read?: (accepted: Response) => Promise<Response | undefined>
) => Promise<Response | undefined>

function askGo(env: Env, cookie: string): AskGo {
  return async (path, read) => {
    let access: Response
    try {
      access = await fetch(new URL(path, env.ORYXA_API_ORIGIN), {
        headers: { Cookie: cookie },
      })
    } catch {
      return failure(503, "unavailable", "The agent service is unavailable.")
    }
    if (!access.ok) return refusedByGo(access)
    if (read) return read(access)
    await access.body?.cancel().catch(() => undefined)
    return undefined
  }
}

/** The bindings routeAgentRequest routes: the Durable Object namespaces in `Env`. */
type ObjectBinding = {
  [Key in keyof Env]: Env[Key] extends { idFromName: unknown } ? Key : never
}[keyof Env]

/**
 * One realtime Durable Object, as the Worker routes it. routeAgentRequest
 * serves a binding at `/agents/{binding in kebab case}/{name}`: the socket
 * (GET with `Upgrade: websocket`), the `reads` a page may GET beneath it,
 * and DELETE when the object has a `deletion`. For any of them the Worker
 * checks the name as it arrives, refuses a cross-site socket or DELETE,
 * requires the session cookie, asks Go (`checkAccess`), and only then hands
 * the request on with the headers it sets itself (`identity`).
 */
type RealtimeObject<TIds> = {
  /** Its Durable Object binding in wrangler.jsonc, which is also its class. */
  binding: ObjectBinding
  /** What a page may GET beside the socket, as the path's last segment. */
  reads: readonly string[]
  /**
   * The ids in a name, or nothing for a name the object's own name builder
   * could not have made. It reads the raw path segment: routeAgentRequest
   * names the object after it undecoded, so a name that reads the same once
   * decoded would otherwise reach a second object for the same thing.
   */
  parseName: (rawName: string) => TIds | undefined
  /**
   * Asks Go, as the member, whether they may open it: nothing lets the
   * request through, a response refuses it.
   */
  checkAccess: (ids: TIds, ask: AskGo) => Promise<Response | undefined>
  /**
   * The headers the object trusts. Every `X-Oryxa-*` header the browser
   * sent is dropped before these are set.
   */
  identity: (ids: TIds, cookie: string) => Record<string, string>
  /** DELETE, from the app's own origin. Without it, DELETE answers 405. */
  deletion?: {
    /**
     * Go's DELETE for what the object mirrors, sent first as the member
     * (cookie and origin): Go decides, its answer is passed on, and a
     * refusal leaves the transcript with what it belongs to. Leave it out
     * when Go has nothing to delete: `checkAccess` then decides, and the
     * purge is the whole deletion.
     */
    goPath?: (ids: TIds) => string
    /** Clears what the object stores, over RPC. */
    purge: (env: Env, rawName: string) => Promise<void>
  }
  /** Words for the object not answering. */
  unavailable: string
}

/** A `RealtimeObject` ready to route, whatever its ids. */
type RealtimeRoute = {
  /** The path segment routeAgentRequest serves its binding at. */
  segment: string
  reads: readonly string[]
  route: (
    request: Request,
    env: Env,
    url: URL,
    rawName: string,
    read: string | undefined
  ) => Promise<Response>
}

/**
 * A binding as routeAgentRequest's path names it (`ThreadChatAgent` is
 * `thread-chat-agent`), converted as the SDK's own camelCaseToKebabCase does.
 */
function kebabCase(binding: string): string {
  if (binding === binding.toUpperCase() && binding !== binding.toLowerCase()) {
    return binding.toLowerCase().replace(/_/g, "-")
  }
  const kebab = binding
    .replace(/[A-Z]/g, (letter) => `-${letter.toLowerCase()}`)
    .replace(/^-/, "")
  return kebab.replace(/_/g, "-").replace(/-$/, "")
}

function realtimeObject<TIds>(object: RealtimeObject<TIds>): RealtimeRoute {
  const segment = kebabCase(object.binding)
  return {
    segment,
    reads: object.reads,
    route: (request, env, url, rawName, read) =>
      routeToObject(object, segment, request, env, url, rawName, read),
  }
}

async function routeToObject<TIds>(
  object: RealtimeObject<TIds>,
  segment: string,
  request: Request,
  env: Env,
  url: URL,
  rawName: string,
  read: string | undefined
): Promise<Response> {
  const ids = object.parseName(rawName)
  if (!ids) {
    return failure(400, "invalid_request", "Invalid conversation.")
  }
  const isWebSocket =
    request.headers.get("Upgrade")?.toLowerCase() === "websocket"
  const isDelete = request.method === "DELETE"
  const deletion = read === undefined ? object.deletion : undefined
  if (
    !(request.method === "GET" || (isDelete && deletion)) ||
    (read !== undefined && isWebSocket)
  ) {
    return failure(405, "method_not_allowed", "Method not allowed.", {
      Allow: deletion ? "GET, DELETE" : "GET",
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
  const ask = askGo(env, cookie)

  if (isDelete && deletion) {
    if (!deletion.goPath) {
      // Nothing in Go to delete: the member may clear the object when they
      // may open it.
      const refused = await object.checkAccess(ids, ask)
      if (refused) return refused
      try {
        await deletion.purge(env, rawName)
      } catch (error) {
        console.error(`${segment}: could not clear a conversation`, error)
        return failure(503, "unavailable", object.unavailable)
      }
      return new Response(null, { status: 204 })
    }
    // Go deletes first, and decides whether the member may: a refusal
    // leaves the transcript with what it belongs to.
    let deleted: Response
    try {
      deleted = await fetch(
        new URL(deletion.goPath(ids), env.ORYXA_API_ORIGIN),
        { method: "DELETE", headers: { Cookie: cookie, Origin: url.origin } }
      )
    } catch {
      return failure(503, "unavailable", "The agent service is unavailable.")
    }
    if (!deleted.ok) return deleted
    // It is gone. A transcript left behind could no longer be opened (the
    // access check now answers 404), and a retry could not reach it either,
    // so a purge that fails is logged rather than reported.
    try {
      await deletion.purge(env, rawName)
    } catch (error) {
      console.error(`${segment}: could not purge a deleted conversation`, error)
    }
    return deleted
  }

  // The member may reach the object only for what Go lets them open.
  const refused = await object.checkAccess(ids, ask)
  if (refused) return refused

  const headers = new Headers(request.headers)
  for (const name of [...headers.keys()]) {
    if (name.toLowerCase().startsWith("x-oryxa-")) headers.delete(name)
  }
  for (const [name, value] of Object.entries(object.identity(ids, cookie))) {
    headers.set(name, value)
  }
  let routed: Response | null
  try {
    routed = await routeAgentRequest(new Request(request, { headers }), env)
  } catch (error) {
    console.error(`${segment}: could not reach the Durable Object`, error)
    return failure(503, "unavailable", object.unavailable)
  }
  return routed ?? failure(404, "not_found", "Conversation agent unavailable.")
}

const organizationPath = (organizationId: string) =>
  `/v1/organizations/${encodeURIComponent(organizationId)}`

const threadPath = (ids: { organizationId: string; threadId: string }) =>
  `${organizationPath(ids.organizationId)}/threads/${encodeURIComponent(ids.threadId)}`

const agentPath = (ids: { organizationId: string; agentId: string }) =>
  `${organizationPath(ids.organizationId)}/agents/${encodeURIComponent(ids.agentId)}`

/**
 * Whether the agent Go answered with is a specialist. Go's `getAgent`
 * answers only the member's own agents, so the status has already said it
 * is theirs; the primary agent's chat is its threads, not this object.
 */
async function isSpecialist(accepted: Response): Promise<Response | undefined> {
  let agent: { kind?: unknown }
  try {
    agent = await accepted.json()
  } catch {
    return badGateway()
  }
  if (agent.kind !== "specialist") {
    return failure(404, "not_found", "This is not one of your specialists.")
  }
  return undefined
}

/**
 * Every realtime object, by the path segment its binding is served at. A
 * new one (REALTIME.md: the teamspace room, a side conversation, a DM) is
 * one more entry.
 */
const realtimeObjects = new Map(
  [
    // The primary agent's chat of one thread, `{organization}~{thread}`.
    realtimeObject({
      binding: "ThreadChatAgent",
      reads: ["get-messages"],
      parseName: parseThreadAgentName,
      checkAccess: (ids, ask) => ask(threadPath(ids)),
      identity: (ids, cookie) => ({
        [chatRequestHeaders.cookie]: cookie,
        [chatRequestHeaders.organization]: ids.organizationId,
        [chatRequestHeaders.thread]: ids.threadId,
      }),
      deletion: {
        goPath: threadPath,
        purge: async (env, name) => {
          const agent = await getAgentByName(env.ThreadChatAgent, name)
          await agent.purgeTranscript()
        },
      },
      unavailable: "Your primary agent's conversation is unavailable.",
    }),
    // The member's chat with one of their specialists,
    // `{organization}~{agent}`. Go has no route that deletes a specialist
    // conversation, so DELETE only starts the chat over: the object clears
    // its transcript and forgets its conversation, which Go keeps.
    realtimeObject({
      binding: "SpecialistChatAgent",
      reads: ["get-messages"],
      parseName: parseSpecialistAgentName,
      checkAccess: (ids, ask) => ask(agentPath(ids), isSpecialist),
      identity: (ids, cookie) => ({
        [specialistRequestHeaders.cookie]: cookie,
        [specialistRequestHeaders.organization]: ids.organizationId,
        [specialistRequestHeaders.agent]: ids.agentId,
      }),
      deletion: {
        purge: async (env, name) => {
          const agent = await getAgentByName(env.SpecialistChatAgent, name)
          await agent.purgeTranscript()
        },
      },
      unavailable: "Your specialist's conversation is unavailable.",
    }),
  ].map((route) => [route.segment, route])
)

/** `/agents/{segment}/{name}`, optionally with one more segment: a read. */
const realtimePath = /^\/agents\/([^/]+)\/([^/]+)(?:\/([^/]+))?\/?$/

export default {
  async fetch(request, env) {
    const url = new URL(request.url)
    const match = realtimePath.exec(url.pathname)
    const object = match ? realtimeObjects.get(match[1]) : undefined
    if (match && object) {
      const [, , rawName, read] = match as Array<string | undefined>
      if (read === undefined || object.reads.includes(read)) {
        return object.route(request, env, url, rawName ?? "", read)
      }
    }
    if (url.pathname.startsWith("/agents/")) {
      return failure(404, "not_found", "Not found.")
    }
    return handler.fetch(request)
  },
} satisfies ExportedHandler<Env>
