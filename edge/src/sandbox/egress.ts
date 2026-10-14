// SandboxEgress receives every plain-HTTP request a sandbox makes
// (interceptAllOutboundHttp) and refuses the ones an isolation attack would
// make: cloud metadata, loopback and private addresses, and Oryxa's own
// internal API. Everything else is fetched as asked. HTTPS goes straight
// out; there is no metadata service or private network behind it on
// Cloudflare, and Oryxa's internal routes are refused at its edge.
import { WorkerEntrypoint } from "cloudflare:workers";
import type { Env } from "../env";

const BLOCKED_HOSTS = [/^localhost$/i, /\.internal$/i, /\.local$/i, /^metadata(\.google\.internal)?$/i];

export function blockedTarget(url: URL): string | null {
  const host = url.hostname.replace(/^\[|\]$/g, "").toLowerCase();
  if (BLOCKED_HOSTS.some((pattern) => pattern.test(host))) return "internal host";
  const v4 = /^(\d{1,3})\.(\d{1,3})\.(\d{1,3})\.(\d{1,3})$/.exec(host);
  if (v4) {
    const [a, b] = [Number(v4[1]), Number(v4[2])];
    if (a === 10 || a === 127 || a === 0) return "private address";
    if (a === 169 && b === 254) return "link-local or metadata address";
    if (a === 172 && b >= 16 && b <= 31) return "private address";
    if (a === 192 && b === 168) return "private address";
    if (a === 100 && b >= 64 && b <= 127) return "carrier-grade NAT address";
  }
  if (host.includes(":")) {
    if (host === "::1" || host === "::" || host.startsWith("fe80:") || host.startsWith("fc") || host.startsWith("fd")) {
      return "private address";
    }
    // An IPv4-mapped address, which the URL parser writes in hex.
    const mapped = /^::ffff:(?:(\d+\.\d+\.\d+\.\d+)|([0-9a-f]{1,4}):([0-9a-f]{1,4}))$/.exec(host);
    if (mapped) {
      const v4 = mapped[1] ?? [parseInt(mapped[2]!, 16) >> 8, parseInt(mapped[2]!, 16) & 255, parseInt(mapped[3]!, 16) >> 8, parseInt(mapped[3]!, 16) & 255].join(".");
      return blockedTarget(new URL(`http://${v4}/`)) ?? "IPv4-mapped address";
    }
  }
  if (/(^|\.)oryxa\.in$/i.test(host) && url.pathname.startsWith("/v1/internal")) return "Oryxa internal API";
  return null;
}

export class SandboxEgress extends WorkerEntrypoint<Env> {
  override async fetch(request: Request): Promise<Response> {
    const url = new URL(request.url);
    const reason = blockedTarget(url);
    if (reason) {
      console.warn(JSON.stringify({ msg: "sandbox egress refused", host: url.hostname, reason }));
      return new Response(`Oryxa sandbox: requests to ${reason}es are blocked.\n`, { status: 403 });
    }
    return fetch(request);
  }
}
