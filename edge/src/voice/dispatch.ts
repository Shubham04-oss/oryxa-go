// The edge's half of B5: VoiceDO hands real work to Go at
// POST {AGENTS_URL}/v1/edge/dispatch, signed with the edge→agents token
// (contract §4): the edge token with iss and aud swapped, ES256 under the
// edge's own key (EDGE_SIGNING_KEY, a P-256 private JWK). Go's
// edgedispatch.Verifier holds its public half and checks the body names the
// token's organization, room and member. The answer comes back later at
// /voice/result.
import { DispatchAccepted, type DispatchRequest } from "../call/contracts";
import { AUDIENCE_AGENTS, ISSUER_EDGE, type TokenClaims } from "../contracts";
import type { Env } from "../env";
import { base64UrlEncode } from "../http";

export interface AgentsClient {
  dispatch(request: DispatchRequest): Promise<DispatchAccepted>;
}

// TOKEN_LIFETIME_SECONDS matches the Go issuer's: a token per request.
const TOKEN_LIFETIME_SECONDS = 60;
const TIMEOUT_MS = 10_000;

export class DispatchError extends Error {
  constructor(
    message: string,
    readonly retryable: boolean,
  ) {
    super(message);
  }
}

const keys = new Map<string, Promise<CryptoKey>>();

function signingKey(jwk: string): Promise<CryptoKey> {
  let key = keys.get(jwk);
  if (!key) {
    key = crypto.subtle.importKey("jwk", JSON.parse(jwk), { name: "ECDSA", namedCurve: "P-256" }, false, ["sign"]);
    keys.set(jwk, key);
  }
  return key;
}

// signEdgeToken signs an edge→agents token for request's organization, room
// and member.
export async function signEdgeToken(jwk: string, request: DispatchRequest, nowSeconds = Math.floor(Date.now() / 1000)): Promise<string> {
  const claims: TokenClaims = {
    iss: ISSUER_EDGE,
    aud: AUDIENCE_AGENTS,
    org: request.org,
    team: request.team ?? "",
    room: request.room,
    member: request.member,
    agent: "",
    run: "",
    scopes: ["call:speak"],
    private: false,
    iat: nowSeconds,
    exp: nowSeconds + TOKEN_LIFETIME_SECONDS,
    jti: crypto.randomUUID(),
  };
  const encode = (value: unknown) => base64UrlEncode(new TextEncoder().encode(JSON.stringify(value)));
  const input = `${encode({ alg: "ES256", typ: "JWT" })}.${encode(claims)}`;
  const signature = await crypto.subtle.sign({ name: "ECDSA", hash: "SHA-256" }, await signingKey(jwk), new TextEncoder().encode(input));
  return `${input}.${base64UrlEncode(new Uint8Array(signature))}`;
}

export class Agents implements AgentsClient {
  constructor(
    private readonly baseURL: string,
    private readonly jwk: string,
    // Wrapped: Workers refuse the global fetch called as another object's method.
    private readonly fetcher: typeof fetch = (input, init) => fetch(input, init),
  ) {}

  static fromEnv(env: Env): Agents | undefined {
    if (!env.AGENTS_URL || !env.EDGE_SIGNING_KEY) return undefined;
    return new Agents(env.AGENTS_URL.replace(/\/+$/, ""), env.EDGE_SIGNING_KEY);
  }

  async dispatch(request: DispatchRequest): Promise<DispatchAccepted> {
    const token = await signEdgeToken(this.jwk, request);
    let response: Response;
    try {
      response = await this.fetcher(`${this.baseURL}/v1/edge/dispatch`, {
        method: "POST",
        headers: { authorization: `Bearer ${token}`, "content-type": "application/json" },
        body: JSON.stringify(request),
        signal: AbortSignal.timeout(TIMEOUT_MS),
        redirect: "manual",
      });
    } catch (error) {
      throw new DispatchError(`agents did not answer: ${String(error)}`, true);
    }
    if (response.status !== 202) {
      throw new DispatchError(`agents answered ${response.status}`, response.status === 503 || response.status === 429 || response.status >= 500);
    }
    const parsed = DispatchAccepted.safeParse(await response.json().catch(() => null));
    if (!parsed.success) throw new DispatchError("agents' acknowledgement could not be read", false);
    return parsed.data;
  }
}
