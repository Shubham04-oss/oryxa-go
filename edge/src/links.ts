// The two signed-URL routes (contract §3): /p/{token}/… serves a sandbox
// preview, /t/{token} its terminal. The token is the grant: the signature
// and expiry are checked here, then the request goes to that one sandbox.
import { sandboxDOName } from "./contracts";
import type { Env } from "./env";
import { verifyTarget } from "./signed";

const LINK = /^\/(p|t)\/([A-Za-z0-9_-]+\.[A-Za-z0-9_-]+)(\/.*)?$/;

export function isLink(pathname: string): boolean {
  return LINK.test(pathname);
}

export async function handleLink(request: Request, env: Env): Promise<Response> {
  const url = new URL(request.url);
  const match = LINK.exec(url.pathname);
  if (!match) return new Response("not found", { status: 404 });
  const target = await verifyTarget(env.EDGE_URL_SIGNING_KEY, match[2]!);
  const wanted = match[1] === "p" ? "preview" : "terminal";
  if (!target || target.t !== wanted) return new Response("This link has expired. Ask for a new one.", { status: 403 });
  const headers = new Headers(request.headers);
  headers.set("x-oryxa-signed", JSON.stringify(target));
  headers.set("x-oryxa-rest", match[3] ?? "/");
  if (wanted === "terminal") {
    headers.set("x-oryxa-cols", url.searchParams.get("cols") ?? "120");
    headers.set("x-oryxa-rows", url.searchParams.get("rows") ?? "32");
  }
  const stub = env.SANDBOX.getByName(sandboxDOName(target.org, target.room, target.name));
  return stub.fetch(new Request(request, { headers }));
}
