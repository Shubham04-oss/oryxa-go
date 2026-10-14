// oryxa-edge: the live layer on Cloudflare (docs/design/live-layer). Go's
// agents reach it over HTTPS with an edge token (router.ts); web-next
// through the WebEdge service binding (web.ts); members' browsers only over
// signed preview and terminal links (links.ts) and Browser Run's live view.
import type { Env } from "./env";
import { errorResponse } from "./http";
import { handleLink, isLink } from "./links";
import { handleRealtimeKitWebhook, WEBHOOK_PATH } from "./call/webhook";
import { handleApi } from "./router";

export { RoomDO } from "./room/room";
export { SandboxDO } from "./sandbox/sandbox";
export { SandboxEgress } from "./sandbox/egress";
export { BrowserDO } from "./browser/browser";
export { VoiceDO } from "./voice/voice";
export { WebEdge } from "./web";
export { DirectoryBackupGateway } from "@cloudflare/sandbox";

export default {
  async fetch(request, env): Promise<Response> {
    const url = new URL(request.url);
    if (url.pathname === "/healthz") return new Response("ok");
    if (url.pathname.startsWith("/v1/rooms/")) return handleApi(request, env);
    if (isLink(url.pathname)) return handleLink(request, env);
    if (url.pathname === WEBHOOK_PATH) return handleRealtimeKitWebhook(request, env);
    return errorResponse("not_found", "no such route");
  },
} satisfies ExportedHandler<Env>;
