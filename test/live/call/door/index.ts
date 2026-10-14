// The local door (wrangler.jsonc): /edge/v1/rooms/{kind}/{id}/… goes to the
// edge's WebEdge as /v1/rooms/{kind}/{id}/…, with the member x-live-identity
// names as WebEdge's x-oryxa-member. This is the shape proposed for web-next's
// binding on discussion #217; there, the identity comes from the member's
// session, never from a header the browser sets. Anything else goes to the
// edge's public routes as it is, for the agent tokens that seed rooms.
interface Env {
  EDGE: Fetcher;
  EDGE_PUBLIC: Fetcher;
}

const PREFIX = "/edge";

export default {
  async fetch(request: Request, env: Env): Promise<Response> {
    const url = new URL(request.url);
    if (!url.pathname.startsWith(`${PREFIX}/v1/rooms/`)) return env.EDGE_PUBLIC.fetch(request);
    url.pathname = url.pathname.slice(PREFIX.length);
    const headers = new Headers(request.headers);
    headers.delete("x-oryxa-member");
    const identity = headers.get("x-live-identity");
    headers.delete("x-live-identity");
    if (identity) headers.set("x-oryxa-member", identity);
    const body = request.method === "GET" || request.method === "HEAD" ? null : request.body;
    return env.EDGE.fetch(new Request(url, { method: request.method, headers, body }));
  },
};
