// Verifies the edge token (contract §2): an ES256 JWT agents signed with its
// serviceauth KMS key. The edge holds that key's public half(s) in
// AGENTS_TOKEN_PUBLIC_KEYS, a JSON array of base64 DER SPKI, as KMS's
// GetPublicKey returns it; more than one lets the key rotate.
//
// The signature is checked before any claim is believed. Whether the token
// covers the room, scope and child it reaches is the router's and the
// room's to check; replay (jti) is the room's.
import {
  AUDIENCE_EDGE,
  ISSUER_AGENTS,
  MAX_TOKEN_LIFETIME_SECONDS,
  TokenClaims,
} from "../contracts";
import { base64Decode, base64UrlDecode } from "../http";

// CLOCK_SKEW_SECONDS tolerates the signer's and the edge's clocks
// disagreeing, as serviceauth's verifier does.
export const CLOCK_SKEW_SECONDS = 30;
const MAX_TOKEN_BYTES = 4096;

export type VerifyFailure =
  | "malformed"
  | "no_keys"
  | "bad_signature"
  | "wrong_issuer"
  | "wrong_audience"
  | "invalid_lifetime"
  | "not_yet_valid"
  | "expired";

export type VerifyResult = { ok: true; claims: TokenClaims } | { ok: false; reason: VerifyFailure };

const keyCache = new Map<string, Promise<CryptoKey[]>>();

// importKeys parses AGENTS_TOKEN_PUBLIC_KEYS once per isolate.
export function importKeys(spec: string | undefined): Promise<CryptoKey[]> {
  const value = spec ?? "";
  let keys = keyCache.get(value);
  if (!keys) {
    keys = (async () => {
      if (value.trim() === "") return [];
      const encoded: unknown = JSON.parse(value);
      if (!Array.isArray(encoded)) throw new Error("AGENTS_TOKEN_PUBLIC_KEYS must be a JSON array");
      return Promise.all(
        encoded.map((key) =>
          crypto.subtle.importKey("spki", base64Decode(String(key)), { name: "ECDSA", namedCurve: "P-256" }, false, [
            "verify",
          ]),
        ),
      );
    })();
    keyCache.set(value, keys);
  }
  return keys;
}

export interface VerifyOptions {
  issuer?: string;
  audience?: string;
  // nowSeconds is the Unix time to check against; the clock by default.
  nowSeconds?: number;
}

export async function verifyEdgeToken(token: string, keys: CryptoKey[], options: VerifyOptions = {}): Promise<VerifyResult> {
  const issuer = options.issuer ?? ISSUER_AGENTS;
  const audience = options.audience ?? AUDIENCE_EDGE;
  const now = options.nowSeconds ?? Math.floor(Date.now() / 1000);

  if (token.length > MAX_TOKEN_BYTES) return { ok: false, reason: "malformed" };
  const parts = token.split(".");
  if (parts.length !== 3 || parts.some((part) => part === "")) return { ok: false, reason: "malformed" };
  let header: { alg?: unknown; crit?: unknown };
  let payload: unknown;
  let signature: Uint8Array;
  try {
    header = JSON.parse(new TextDecoder().decode(base64UrlDecode(parts[0]!)));
    payload = JSON.parse(new TextDecoder().decode(base64UrlDecode(parts[1]!)));
    signature = base64UrlDecode(parts[2]!);
  } catch {
    return { ok: false, reason: "malformed" };
  }
  if (header.alg !== "ES256" || header.crit !== undefined || signature.length !== 64) {
    return { ok: false, reason: "malformed" };
  }
  if (keys.length === 0) return { ok: false, reason: "no_keys" };

  const signed = new TextEncoder().encode(`${parts[0]}.${parts[1]}`);
  let verified = false;
  for (const key of keys) {
    if (await crypto.subtle.verify({ name: "ECDSA", hash: "SHA-256" }, key, signature, signed)) {
      verified = true;
      break;
    }
  }
  if (!verified) return { ok: false, reason: "bad_signature" };

  const parsed = TokenClaims.safeParse(payload);
  if (!parsed.success) return { ok: false, reason: "malformed" };
  const claims = parsed.data;
  if (claims.iss !== issuer) return { ok: false, reason: "wrong_issuer" };
  if (claims.aud !== audience) return { ok: false, reason: "wrong_audience" };
  const lifetime = claims.exp - claims.iat;
  if (lifetime <= 0 || lifetime > MAX_TOKEN_LIFETIME_SECONDS) return { ok: false, reason: "invalid_lifetime" };
  if (claims.iat > now + CLOCK_SKEW_SECONDS) return { ok: false, reason: "not_yet_valid" };
  if (claims.exp <= now - CLOCK_SKEW_SECONDS) return { ok: false, reason: "expired" };
  return { ok: true, claims };
}
