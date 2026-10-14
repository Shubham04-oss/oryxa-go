// Signs edge tokens in tests with the key vitest.config.ts generated, the
// way serviceauth.Signer.EdgeToken does with agents' KMS key.
import { env } from "cloudflare:workers";
import type { Room, Scope, TokenClaims } from "../src/contracts";
import { base64UrlEncode } from "../src/http";

let signingKey: Promise<CryptoKey> | undefined;

function key(): Promise<CryptoKey> {
  signingKey ??= crypto.subtle.importKey(
    "jwk",
    JSON.parse(env.TEST_SIGNING_KEY),
    { name: "ECDSA", namedCurve: "P-256" },
    false,
    ["sign"],
  );
  return signingKey;
}

export interface TokenOptions {
  org?: string;
  room?: Room;
  member?: string;
  agent?: string;
  run?: string;
  scopes?: Scope[];
  private?: boolean;
  team?: string;
  jti?: string;
  iat?: number;
  exp?: number;
  aud?: string;
}

export async function signClaims(claims: TokenClaims): Promise<string> {
  const header = base64UrlEncode(new TextEncoder().encode(JSON.stringify({ alg: "ES256", typ: "JWT" })));
  const payload = base64UrlEncode(new TextEncoder().encode(JSON.stringify(claims)));
  const signature = await crypto.subtle.sign(
    { name: "ECDSA", hash: "SHA-256" },
    await key(),
    new TextEncoder().encode(`${header}.${payload}`),
  );
  return `${header}.${payload}.${base64UrlEncode(new Uint8Array(signature))}`;
}

export function claims(options: TokenOptions = {}): TokenClaims {
  const now = Math.floor(Date.now() / 1000);
  return {
    iss: "agents",
    aud: options.aud ?? "edge",
    org: options.org ?? "org_1",
    team: options.team ?? "",
    room: options.room ?? { kind: "thread", id: "thr_1" },
    member: options.member ?? "usr_1",
    agent: options.agent ?? "agt_1",
    run: options.run ?? "run_1",
    scopes: options.scopes ?? ["sandbox:exec", "sandbox:files", "browser:drive", "log:read", "log:write"],
    private: options.private ?? false,
    iat: options.iat ?? now,
    exp: options.exp ?? now + 60,
    jti: options.jti ?? crypto.randomUUID(),
  };
}

export async function token(options: TokenOptions = {}): Promise<string> {
  return signClaims(claims(options));
}

// call sends one edge API request with a fresh token.
export async function call(
  path: string,
  init: RequestInit & { token?: string; tokenOptions?: TokenOptions } = {},
): Promise<Response> {
  const { exports } = await import("cloudflare:workers");
  const bearer = init.token ?? (await token(init.tokenOptions));
  const headers = new Headers(init.headers);
  headers.set("authorization", `Bearer ${bearer}`);
  if (init.body && !headers.has("content-type")) headers.set("content-type", "application/json");
  return (exports as unknown as { default: Fetcher }).default.fetch(`https://edge.test${path}`, { ...init, headers });
}
