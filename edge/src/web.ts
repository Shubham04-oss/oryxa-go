// WebEdge is web-next's way in (contract §3), bound as a service binding to
// this named entrypoint, which nothing on the internet can reach. web-next
// has already checked the member's session; it says who they are in
// WEB_IDENTITY_HEADER, and the room checks they are one of its members.
import { WorkerEntrypoint } from "cloudflare:workers";
import { Id } from "./contracts";
import type { Env } from "./env";
import { EdgeError, toResponse } from "./http";
import { forward, parseRoomPath } from "./router";

export const WEB_IDENTITY_HEADER = "x-oryxa-member";

export class WebEdge extends WorkerEntrypoint<Env> {
  override async fetch(request: Request): Promise<Response> {
    try {
      const { room, rest } = parseRoomPath(new URL(request.url).pathname);
      let identity: { org: string; member: string };
      try {
        identity = JSON.parse(request.headers.get(WEB_IDENTITY_HEADER) ?? "");
      } catch {
        throw new EdgeError("unauthorized", "no member identity");
      }
      if (!Id.safeParse(identity?.org).success || typeof identity.member !== "string" || identity.member === "") {
        throw new EdgeError("unauthorized", "no member identity");
      }
      return await forward(request, this.env, identity.org, room, rest, { web: { org: identity.org, member: identity.member } });
    } catch (error) {
      return toResponse(error);
    }
  }
}
