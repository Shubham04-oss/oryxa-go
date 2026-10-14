// The Go → edge API (contract §2, §5, §6, §8): /v1/rooms/{kind}/{id}/…, each
// request carrying an edge token. The token is verified here; whether it may
// do what it asks in this room is the RoomDO's to decide, so it travels on
// to the room as CLAIMS_HEADER, which only this Worker can set.
import { Room, roomDOName, type TokenClaims } from "./contracts";
import { importKeys, verifyEdgeToken } from "./auth/token";
import type { Env } from "./env";
import { EdgeError, toResponse } from "./http";
import { CLAIMS_HEADER, REST_HEADER, ROOM_HEADER, WEB_HEADER, type WebIdentity } from "./room/room";

const ROOM_PATH = /^\/v1\/rooms\/([^/]+)\/([^/]+)(\/.*)?$/;

// parseRoomPath splits /v1/rooms/{kind}/{id}{rest}, or refuses it.
export function parseRoomPath(pathname: string): { room: Room; rest: string } {
  const match = ROOM_PATH.exec(pathname);
  if (!match) throw new EdgeError("not_found", "no such route");
  const parsed = Room.safeParse({ kind: match[1], id: decodeURIComponent(match[2]!) });
  if (!parsed.success) throw new EdgeError("not_found", "no such room");
  return { room: parsed.data, rest: match[3] ?? "" };
}

export async function authenticate(request: Request, env: Env): Promise<TokenClaims> {
  const header = request.headers.get("authorization") ?? "";
  if (!header.startsWith("Bearer ")) throw new EdgeError("unauthorized", "no edge token");
  const keys = await importKeys(env.AGENTS_TOKEN_PUBLIC_KEYS);
  const result = await verifyEdgeToken(header.slice("Bearer ".length).trim(), keys);
  if (!result.ok) {
    console.warn(JSON.stringify({ msg: "edge token refused", reason: result.reason, path: new URL(request.url).pathname }));
    throw new EdgeError("unauthorized", `edge token refused: ${result.reason}`);
  }
  return result.claims;
}

// forward sends request to the room's DO, naming the room, the rest of the
// path and exactly one caller header; any copy of those headers the client
// sent is dropped.
export function forward(
  request: Request,
  env: Env,
  org: string,
  room: Room,
  rest: string,
  caller: { claims: TokenClaims } | { web: WebIdentity },
): Promise<Response> {
  const headers = new Headers(request.headers);
  for (const name of [CLAIMS_HEADER, WEB_HEADER, ROOM_HEADER, REST_HEADER, "authorization"]) headers.delete(name);
  headers.set(ROOM_HEADER, JSON.stringify(room));
  headers.set(REST_HEADER, rest);
  if ("claims" in caller) headers.set(CLAIMS_HEADER, JSON.stringify(caller.claims));
  else headers.set(WEB_HEADER, JSON.stringify(caller.web));
  const stub = env.ROOM.getByName(roomDOName(org, room));
  return stub.fetch(new Request(request, { headers }));
}

export async function handleApi(request: Request, env: Env): Promise<Response> {
  try {
    const url = new URL(request.url);
    const { room, rest } = parseRoomPath(url.pathname);
    const claims = await authenticate(request, env);
    if (claims.room.kind !== room.kind || claims.room.id !== room.id) {
      console.warn(JSON.stringify({ msg: "edge token for another room", org: claims.org, token: claims.room, path: room }));
      throw new EdgeError("forbidden", "token is for another room");
    }
    return await forward(request, env, claims.org, room, rest, { claims });
  } catch (error) {
    return toResponse(error);
  }
}
