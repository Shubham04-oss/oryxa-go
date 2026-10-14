// The local door (wrangler.jsonc), in the shape of web-next's binding (F5,
// web-next/src/edge-proxy.ts):
//
//   /live/{org}/rooms/{kind}/{id}/…  →  WebEdge /v1/rooms/{kind}/{id}/…
//
// with x-oryxa-member set to {org, member}. In web-next the member comes from
// their session, checked with Go for that org; here x-live-identity names
// them, which is why this is never deployed. Anything else goes to the edge's
// public routes as it is, for the agent tokens that seed rooms.
interface Env {
  EDGE: Fetcher;
  EDGE_PUBLIC: Fetcher;
}

const DOOR = /^\/live\/([^/]+)(\/rooms\/[^/]+\/[^/]+(?:\/.*)?)$/;

export default {
  async fetch(request: Request, env: Env): Promise<Response> {
    const url = new URL(request.url);
    const match = DOOR.exec(url.pathname);
    if (!match) return env.EDGE_PUBLIC.fetch(request);
    const org = decodeURIComponent(match[1]!);
    url.pathname = `/v1${match[2]}`;

    const headers = new Headers(request.headers);
    for (const name of ["x-oryxa-member", "x-live-identity", "cookie", "authorization"]) headers.delete(name);
    const identity = request.headers.get("x-live-identity");
    if (identity) {
      const { member } = JSON.parse(identity) as { member: string };
      headers.set("x-oryxa-member", JSON.stringify({ org, member }));
    }
    const body = request.method === "GET" || request.method === "HEAD" ? null : request.body;
    return env.EDGE.fetch(new Request(url, { method: request.method, headers, body }));
  },
};
