// The live layer's way in (F5, docs/design/live-layer/01-contracts.md §3):
// a page reaches its room on the edge at
//   /live/{org}/rooms/{kind}/{id}{rest}
// on the app's own origin. The Worker asks Go who the member is for their
// cookie, then hands the request to the edge's WebEdge entrypoint over the
// EDGE service binding, naming the member in x-oryxa-member. Nothing else
// in the browser's request says who they are: its cookie, any
// authorization and any x-oryxa-* header are dropped. The room itself
// checks the member is one of its members (edge/src/web.ts).

/** The header WebEdge reads the member from (edge/src/web.ts). */
export const EDGE_MEMBER_HEADER = "x-oryxa-member"

/** The room kinds the edge knows (edge/src/contracts ROOM_KINDS). */
export const ROOM_KINDS = ["thread", "teamspace", "dm", "side"] as const
export type RoomKind = (typeof ROOM_KINDS)[number]

/** The edge's id pattern: nothing that can break out of a name segment. */
const ID = /^[A-Za-z0-9_-]{1,128}$/

export const LIVE_PATH_PREFIX = "/live/"
const livePath = /^\/live\/([^/]+)\/rooms\/([^/]+)\/([^/]+)(\/.*)?$/

/**
 * What a page may ask of its room, by method: the log and its live socket,
 * the call and the agent's voice. Anything else (an agent's writes, the
 * sandbox and browser routes, which come with A8) is not passed on.
 */
const routes: { path: RegExp; methods: string[]; socket?: true }[] = [
  { path: /^\/log$/, methods: ["GET"] },
  { path: /^\/live$/, methods: ["GET"], socket: true },
  { path: /^\/call$/, methods: ["GET"] },
  {
    path: /^\/call\/(join|leave|end|transcript|recording|agent)$/,
    methods: ["POST"],
  },
  { path: /^\/voice\/live$/, methods: ["GET"], socket: true },
]

export type LiveRequest = {
  org: string
  kind: RoomKind
  id: string
  rest: string
}

/** The room a `/live/…` path names, or nothing for one that names none. */
export function parseLivePath(pathname: string): LiveRequest | undefined {
  const match = livePath.exec(pathname)
  if (!match) return undefined
  let org: string, kind: string, id: string
  try {
    org = decodeURIComponent(match[1])
    kind = decodeURIComponent(match[2])
    id = decodeURIComponent(match[3])
  } catch {
    return undefined
  }
  if (!ID.test(org) || !ID.test(id)) return undefined
  if (!(ROOM_KINDS as readonly string[]).includes(kind)) return undefined
  return {
    org,
    kind: kind as RoomKind,
    id,
    rest: match.at(4) ?? "",
  }
}

function failure(status: number, code: string, message: string): Response {
  return Response.json({ error: { code, message } }, { status })
}

/** The headers a page's request keeps on its way to the edge. */
const passedHeaders = [
  "accept",
  "content-type",
  "content-length",
  "upgrade",
  "connection",
  "sec-websocket-key",
  "sec-websocket-version",
  "sec-websocket-protocol",
  "sec-websocket-extensions",
]

export type EdgeEnv = {
  /** The edge's WebEdge entrypoint (wrangler.jsonc `services`). */
  EDGE?: Fetcher
}

/**
 * Serves `/live/…`. `member` asks Go who the cookie's member is in the
 * organization, and resolves their user id or the refusal to send.
 */
export async function handleLive(
  request: Request,
  env: EdgeEnv,
  live: LiveRequest,
  member: (
    org: string,
    cookie: string
  ) => Promise<Response | { userId: string }>
): Promise<Response> {
  const url = new URL(request.url)
  const route = routes.find((candidate) => candidate.path.test(live.rest))
  if (!route) return failure(404, "not_found", "Not found.")
  const isSocket = request.headers.get("Upgrade")?.toLowerCase() === "websocket"
  if (
    !route.methods.includes(request.method) ||
    Boolean(route.socket) !== isSocket
  ) {
    return failure(405, "method_not_allowed", "Method not allowed.")
  }
  // A cookie rides along on a cross-site socket or POST; only a page of this
  // app may open one or change a call.
  if (
    (isSocket || request.method !== "GET") &&
    request.headers.get("Origin") !== url.origin
  ) {
    return failure(403, "forbidden", "Origin not allowed.")
  }
  const cookie = request.headers.get("Cookie")
  if (!cookie) return failure(401, "unauthorized", "Sign in to join this room.")
  if (!env.EDGE) {
    return failure(503, "unavailable", "The live service is unavailable.")
  }

  const caller = await member(live.org, cookie)
  if (caller instanceof Response) return caller

  const headers = new Headers()
  for (const name of passedHeaders) {
    const value = request.headers.get(name)
    if (value !== null) headers.set(name, value)
  }
  headers.set(
    EDGE_MEMBER_HEADER,
    JSON.stringify({ org: live.org, member: caller.userId })
  )
  const target = new URL(
    `/v1/rooms/${live.kind}/${encodeURIComponent(live.id)}${live.rest}${url.search}`,
    "https://edge.internal"
  )
  try {
    return await env.EDGE.fetch(
      new Request(target, {
        method: request.method,
        headers,
        // A call's request is a small JSON body; read whole, it needs no
        // stream handling on the way through.
        body:
          request.method === "GET" ? undefined : await request.arrayBuffer(),
      })
    )
  } catch (error) {
    console.error("live: the edge could not be reached", error)
    return failure(503, "unavailable", "The live service is unavailable.")
  }
}
