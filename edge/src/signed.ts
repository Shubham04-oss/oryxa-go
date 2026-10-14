// Short-lived signed URLs (contract §3): a sandbox's preview or terminal, for
// a browser that can't carry an edge token. HMAC-SHA256 with
// EDGE_URL_SIGNING_KEY over the target and its expiry; the URL is the grant,
// so it lives minutes, and names one member's view of one child.
import { base64UrlDecode, base64UrlEncode } from "./http";
import type { Room } from "./contracts";

export interface SignedTarget {
  t: "preview" | "terminal";
  org: string;
  room: Room;
  name: string;
  member: string;
  port?: number;
  exp: number;
}

export const SIGNED_URL_SECONDS = 15 * 60;

const keys = new Map<string, Promise<CryptoKey>>();

function key(secret: string): Promise<CryptoKey> {
  if (!secret || secret.length < 32) throw new Error("EDGE_URL_SIGNING_KEY must be at least 32 characters");
  let found = keys.get(secret);
  if (!found) {
    found = crypto.subtle.importKey("raw", new TextEncoder().encode(secret), { name: "HMAC", hash: "SHA-256" }, false, [
      "sign",
      "verify",
    ]);
    keys.set(secret, found);
  }
  return found;
}

export async function signTarget(secret: string, target: SignedTarget): Promise<string> {
  const payload = base64UrlEncode(new TextEncoder().encode(JSON.stringify(target)));
  const signature = await crypto.subtle.sign("HMAC", await key(secret), new TextEncoder().encode(payload));
  return `${payload}.${base64UrlEncode(new Uint8Array(signature))}`;
}

export async function verifyTarget(secret: string, token: string, nowSeconds = Math.floor(Date.now() / 1000)): Promise<SignedTarget | null> {
  const [payload, signature, extra] = token.split(".");
  if (!payload || !signature || extra !== undefined) return null;
  let valid = false;
  try {
    valid = await crypto.subtle.verify("HMAC", await key(secret), base64UrlDecode(signature), new TextEncoder().encode(payload));
  } catch {
    return null;
  }
  if (!valid) return null;
  try {
    const target = JSON.parse(new TextDecoder().decode(base64UrlDecode(payload))) as SignedTarget;
    return target.exp > nowSeconds ? target : null;
  } catch {
    return null;
  }
}
