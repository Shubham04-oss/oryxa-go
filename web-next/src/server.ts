import handler from "@tanstack/react-start/server-entry"
import { getAgentByName, routeAgentRequest } from "agents"
import { conversationRequestHeaders } from "./conversation-env"
import type { ConversationEnv } from "./conversation-env"
import { DirectMessageAgent } from "./direct-message-agent"
import { InboxAgent } from "./inbox-agent"
import {
  isNameId,
  parseDirectMessageName,
  parseInboxName,
  parseSideConversationName,
  peerIn,
} from "./lib/dms/names"
import { parseTeamspaceRoomAgentName } from "./lib/rooms/room-agent"
import { encodeRoster } from "./lib/side/protocol"
import type { SideRoster } from "./lib/side/protocol"
import { rosterOf } from "./lib/side/roster"
import { parseSpecialistAgentName } from "./lib/specialists/chat-messages"
import { parseThreadAgentName } from "./lib/threads/chat-messages"
import { REALTIME_PURGE_PATH, handlePurge } from "./realtime-purge"
import { RealtimeRegistryAgent } from "./realtime-registry-agent"
import {
  SideConversationAgent,
  sideRequestHeaders,
} from "./side-conversation-agent"
import {
  SpecialistChatAgent,
  specialistRequestHeaders,
} from "./specialist-chat-agent"
import { TeamspaceRoomAgent, roomRequestHeaders } from "./teamspace-room-agent"
import { ThreadChatAgent, chatRequestHeaders } from "./thread-chat-agent"
import type { ThreadChatEnv } from "./thread-chat-agent"

// The Worker (wrangler.jsonc `main`). It serves the app through TanStack
// Start, puts every realtime Durable Object (REALTIME.md) behind a check
// with Go (one entry each in `realtimeObjects` below), and takes Go's signed
// purges of the conversations a member leaves behind (src/realtime-purge.ts).

export interface Env extends ThreadChatEnv, ConversationEnv {
  ThreadChatAgent: DurableObjectNamespace<ThreadChatAgent>
  SpecialistChatAgent: DurableObjectNamespace<SpecialistChatAgent>
  TeamspaceRoomAgent: DurableObjectNamespace<TeamspaceRoomAgent>
}

export {
  DirectMessageAgent,
  InboxAgent,
  RealtimeRegistryAgent,
  SideConversationAgent,
  SpecialistChatAgent,
  TeamspaceRoomAgent,
  ThreadChatAgent,
}

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
type AskGo = {
  (
    path: string,
    read?: (accepted: Response) => Promise<Response | undefined>
  ): Promise<Response | undefined>
  /**
   * Go's JSON answer to `path`, as `{ answer }`, or the refusal to send, as
   * for any ask; an answer that is not JSON is a bad gateway.
   */
  json: (path: string) => Promise<Response | { answer: unknown }>
}

function askGo(env: Env, cookie: string): AskGo {
  const ask = async (
    path: string,
    read?: (accepted: Response) => Promise<Response | undefined>
  ) => {
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
  const json = async (path: string) => {
    let answer: { answer: unknown } | undefined
    const refused = await ask(path, async (accepted) => {
      try {
        answer = { answer: await accepted.json() }
        return undefined
      } catch {
        return badGateway()
      }
    })
    return refused ?? answer ?? badGateway()
  }
  return Object.assign(ask, { json })
}

/**
 * Headers only the Worker may set on a request it routes: its identity
 * headers (`X-Oryxa-*`), and those the Agents SDK trusts from a request as
 * its own routing (`x-cf-agents-subagent-url` hands a socket to a sub-agent
 * before the object's `onConnect` runs; `x-agents-lifecycle-props` sets its
 * props), with PartyKit's prefix beside them. A browser's copy of any of
 * them is dropped.
 */
const internalHeader = /^(x-oryxa-|x-cf-agents-|x-agents-|x-partykit-)/i

/** The bindings routeAgentRequest routes: the Durable Object namespaces in `Env`. */
type ObjectBinding = {
  [Key in keyof Env]-?: Env[Key] extends { idFromName: unknown } ? Key : never
}[keyof Env]

/**
 * One realtime Durable Object, as the Worker routes it. routeAgentRequest
 * serves a binding at `/agents/{binding in kebab case}/{name}`: the socket
 * (GET with `Upgrade: websocket`), the `reads` a page may GET beneath it,
 * and DELETE when the object has a `deletion`. For any of them the Worker
 * checks the name as it arrives, refuses a cross-site socket or DELETE,
 * requires the session cookie, asks Go (`checkAccess`), and only then hands
 * the request on with the headers it sets itself (`identity`), from what
 * the check learned from Go (`TGrant`: who the member is, for a DM).
 */
type RealtimeObject<TIds, TGrant = undefined> = {
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
   * Asks Go, as the member, whether they may open it: a response refuses
   * the request; anything else lets it through, and is what the check
   * learned for `identity`.
   */
  checkAccess: (ids: TIds, ask: AskGo) => Promise<Response | TGrant>
  /**
   * The headers the object trusts. Every `X-Oryxa-*` header the browser
   * sent is dropped before these are set.
   */
  identity: (ids: TIds, cookie: string, grant: TGrant) => Record<string, string>
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

function realtimeObject<TIds, TGrant = undefined>(
  object: RealtimeObject<TIds, TGrant>
): RealtimeRoute {
  const segment = kebabCase(object.binding)
  return {
    segment,
    reads: object.reads,
    route: (request, env, url, rawName, read) =>
      routeToObject(object, segment, request, env, url, rawName, read),
  }
}

async function routeToObject<TIds, TGrant>(
  object: RealtimeObject<TIds, TGrant>,
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
      const access = await object.checkAccess(ids, ask)
      if (access instanceof Response) return access
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
  const access = await object.checkAccess(ids, ask)
  if (access instanceof Response) return access

  const headers = new Headers(request.headers)
  for (const name of [...headers.keys()]) {
    if (internalHeader.test(name)) headers.delete(name)
  }
  for (const [name, value] of Object.entries(
    object.identity(ids, cookie, access)
  )) {
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

const teamspacePath = (ids: { organizationId: string; teamspaceId: string }) =>
  `${organizationPath(ids.organizationId)}/teamspaces/${encodeURIComponent(ids.teamspaceId)}`

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
 * Whether the member is on the teamspace's team. Go shows a teamspace to
 * whoever manages its team too, and would let such a manager read the room
 * and be counted present in it, but the room is the team's: a manager who
 * is not on the team has no seat, and the page keeps them out
 * (`canUseRoom`), so the Worker does as well.
 */
async function isOnTheTeam(accepted: Response): Promise<Response | undefined> {
  let space: { is_member?: unknown }
  try {
    space = await accepted.json()
  } catch {
    return badGateway()
  }
  if (space.is_member !== true) {
    return failure(
      403,
      "forbidden",
      "Only the teamspace's team can open its room."
    )
  }
  return undefined
}

/** Who the member is, as Go answered the Worker for their cookie. */
type Caller = { userId: string }

/**
 * The member, from Go's answer to `GET /v1/organizations/{org}`: their own
 * membership, so the organization is theirs, and its `user_id` is the same
 * subject `GET /v1/auth/me` answers with. Go marks an agent's token
 * (`agent: true`); DMs are between people, so it is refused.
 */
function callerIn(
  organization: Response | { answer: unknown }
): Response | Caller {
  if (organization instanceof Response) return organization
  const answer = organization.answer as {
    membership?: { user_id?: unknown } | null
    agent?: unknown
  } | null
  if (answer?.agent === true) {
    return failure(
      403,
      "forbidden",
      "An agent cannot open a member's conversations."
    )
  }
  const userId = answer?.membership?.user_id
  if (!isNameId(userId)) return badGateway()
  return { userId }
}

/** The user ids in Go's `GET /v1/organizations/{org}/members` answer, or nothing for an answer that is not a list of members. */
function memberIds(answer: unknown): Set<string> | undefined {
  const members = (answer as { members?: unknown } | null)?.members
  if (!Array.isArray(members)) return undefined
  const ids = new Set<string>()
  for (const member of members as unknown[]) {
    const userId = (member as { user_id?: unknown } | null)?.user_id
    if (typeof userId === "string") ids.add(userId)
  }
  return ids
}

/**
 * Whether the member may open a DM: one of its two people, with the other
 * still in the organization. Both of Go's answers are asked for at once;
 * the member's own refusal (401, or 404 for an organization that is not
 * theirs) comes first, then 403 `not_participant`, then 404
 * `peer_not_found`.
 */
async function directMessageAccess(
  ids: { organizationId: string; userA: string; userB: string },
  ask: AskGo
): Promise<Response | Caller> {
  const [organization, members] = await Promise.all([
    ask.json(organizationPath(ids.organizationId)),
    ask.json(`${organizationPath(ids.organizationId)}/members`),
  ])
  const caller = callerIn(organization)
  if (caller instanceof Response) return caller
  const peerId = peerIn(ids, caller.userId)
  if (!peerId) {
    return failure(
      403,
      "not_participant",
      "Only the two people in a conversation can open it."
    )
  }
  if (members instanceof Response) return members
  const listed = memberIds(members.answer)
  if (!listed) return badGateway()
  if (!listed.has(peerId)) {
    return failure(
      404,
      "peer_not_found",
      "This person is not in your organization."
    )
  }
  return caller
}

/** Whether the member may open an inbox: their own, in an organization of theirs. */
async function inboxAccess(
  ids: { organizationId: string; userId: string },
  ask: AskGo
): Promise<Response | Caller> {
  const caller = callerIn(await ask.json(organizationPath(ids.organizationId)))
  if (caller instanceof Response) return caller
  if (caller.userId !== ids.userId) {
    return failure(403, "forbidden", "This is another member's inbox.")
  }
  return caller
}

/** What the side conversation's check learned: who the member is, and who is in the conversation. */
type SideGrant = Caller & { roster: SideRoster }

/**
 * Whether the member may open a side conversation: one of its two people,
 * on the teamspace's team (`is_member`: Go shows a teamspace to a manager
 * who is not on its team too, but its conversations are the team's), with
 * the other person on the team as well (in the teamspace's people, the
 * whisperer seats Go derives from the live team directory). Both of Go's
 * answers are asked for at once; the member's own refusal comes first
 * (401, or 404 for an organization that is not theirs), then 403
 * `not_participant`, Go's refusal of the teamspace (404 for one the member
 * cannot see), 403 `forbidden` off the team, and 404 `peer_not_found`.
 */
async function sideConversationAccess(
  ids: {
    organizationId: string
    teamspaceId: string
    userA: string
    userB: string
  },
  ask: AskGo
): Promise<Response | SideGrant> {
  const [organization, space] = await Promise.all([
    ask.json(organizationPath(ids.organizationId)),
    ask.json(teamspacePath(ids)),
  ])
  const caller = callerIn(organization)
  if (caller instanceof Response) return caller
  const peerId = peerIn(ids, caller.userId)
  if (!peerId) {
    return failure(
      403,
      "not_participant",
      "Only the two people in a conversation can open it."
    )
  }
  if (space instanceof Response) return space
  if ((space.answer as { is_member?: unknown } | null)?.is_member !== true) {
    return failure(
      403,
      "forbidden",
      "Only the teamspace's team can open its side conversations."
    )
  }
  const roster = rosterOf(space.answer, ids)
  if (!roster) return badGateway()
  if (!roster.people.some((person) => person.userId === peerId)) {
    return failure(
      404,
      "peer_not_found",
      "This person is not on the teamspace's team."
    )
  }
  return { userId: caller.userId, roster }
}

/** The headers a side conversation trusts: the member as Go said, their cookie, and who is in it. */
function sideConversationIdentity(
  ids: { organizationId: string },
  cookie: string,
  grant: SideGrant
): Record<string, string> {
  return {
    [sideRequestHeaders.organization]: ids.organizationId,
    [sideRequestHeaders.user]: grant.userId,
    [sideRequestHeaders.cookie]: cookie,
    [sideRequestHeaders.roster]: encodeRoster(grant.roster),
  }
}

/** The headers a DM or an inbox trusts: the organization, and the member as Go said. */
function conversationIdentity(
  ids: { organizationId: string },
  _cookie: string,
  caller: Caller
): Record<string, string> {
  return {
    [conversationRequestHeaders.organization]: ids.organizationId,
    [conversationRequestHeaders.user]: caller.userId,
  }
}

/**
 * Every realtime object, by the path segment its binding is served at. A
 * new one is one more entry.
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
    // A teamspace's room, `{organization}~{teamspace}`, shared by everyone
    // on its team who has it open. Go keeps the room and deletes it with
    // the teamspace, so there is no DELETE; the object lets go of its copy
    // itself (REALTIME.md).
    realtimeObject({
      binding: "TeamspaceRoomAgent",
      reads: [],
      parseName: parseTeamspaceRoomAgentName,
      checkAccess: (ids, ask) => ask(teamspacePath(ids), isOnTheTeam),
      identity: (ids, cookie) => ({
        [roomRequestHeaders.cookie]: cookie,
        [roomRequestHeaders.organization]: ids.organizationId,
        [roomRequestHeaders.teamspace]: ids.teamspaceId,
      }),
      unavailable: "This teamspace's room is unavailable.",
    }),
    // A DM between two people, `{organization}~{userA}~{userB}` (ids
    // sorted), only in its Durable Object. Nobody deletes one; a purge from
    // Go empties it when one of them leaves (src/realtime-purge.ts).
    realtimeObject({
      binding: "DirectMessageAgent",
      reads: [],
      parseName: parseDirectMessageName,
      checkAccess: directMessageAccess,
      identity: conversationIdentity,
      unavailable: "This conversation is unavailable.",
    }),
    // A teamspace's side conversation, `{organization}~{teamspace}~{userA}~{userB}`
    // (user ids sorted): two teammates and both their agents. People's
    // messages live only in its Durable Object; it asks Go as each of them
    // for their agents and requests. Nobody deletes one; a purge from Go
    // empties it when one of them leaves (src/realtime-purge.ts).
    realtimeObject({
      binding: "SideConversationAgent",
      reads: [],
      parseName: parseSideConversationName,
      checkAccess: sideConversationAccess,
      identity: sideConversationIdentity,
      unavailable: "This side conversation is unavailable.",
    }),
    // A member's conversation list, `{organization}~{user}`, which their
    // conversations keep current.
    realtimeObject({
      binding: "InboxAgent",
      reads: [],
      parseName: parseInboxName,
      checkAccess: inboxAccess,
      identity: conversationIdentity,
      unavailable: "Your conversations are unavailable.",
    }),
  ].map((route) => [route.segment, route])
)

/** `/agents/{segment}/{name}`, optionally with one more segment: a read. */
const realtimePath = /^\/agents\/([^/]+)\/([^/]+)(?:\/([^/]+))?\/?$/

export default {
  async fetch(request, env) {
    const url = new URL(request.url)
    if (url.pathname === REALTIME_PURGE_PATH) return handlePurge(request, env)
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
