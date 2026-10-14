// The edge→agents token, across languages: this signs a fixture with the
// edge's own signer (dispatch.ts), and Go's edgedispatch.Verifier must
// accept it (token_test.go reads the same file). UPDATE_FIXTURES=1 re-signs.
import { generateKeyPairSync } from "node:crypto";
import { readFileSync, writeFileSync } from "node:fs";
import { describe, expect, it } from "vitest";
import { verifyEdgeToken } from "../auth/token";
import type { DispatchRequest } from "../call/contracts";
import { AUDIENCE_AGENTS, ISSUER_EDGE } from "../contracts";
import { base64Decode } from "../http";
import { signEdgeToken } from "./dispatch";

const FIXTURE = new URL("../../../agents/primary/internal/edgedispatch/testdata/edge_to_agents.json", import.meta.url);
const ISSUED_AT = 1_800_000_000;

const request: DispatchRequest = {
  dispatchId: "dsp_fixture",
  org: "org_1",
  team: "team_1",
  room: { kind: "teamspace", id: "room_1" },
  call: "call_1",
  member: "usr_1",
  said: "Krude, what's on today?",
  request: "What's on my calendar?",
  transcriptContext: ["Asha: Krude?"],
};

interface Fixture {
  publicKeys: string[];
  issuedAt: number;
  token: string;
  request: DispatchRequest;
}

describe("the edge→agents token", () => {
  it("is signed the way Go verifies it", async () => {
    if (process.env.UPDATE_FIXTURES) {
      const { publicKey, privateKey } = generateKeyPairSync("ec", { namedCurve: "P-256" });
      const fixture: Fixture = {
        publicKeys: [publicKey.export({ type: "spki", format: "der" }).toString("base64")],
        issuedAt: ISSUED_AT,
        token: await signEdgeToken(JSON.stringify(privateKey.export({ format: "jwk" })), request, ISSUED_AT),
        request,
      };
      writeFileSync(FIXTURE, `${JSON.stringify(fixture, null, 2)}\n`);
    }
    const fixture = JSON.parse(readFileSync(FIXTURE, "utf8")) as Fixture;
    const keys = await Promise.all(
      fixture.publicKeys.map((key) =>
        crypto.subtle.importKey("spki", base64Decode(key), { name: "ECDSA", namedCurve: "P-256" }, false, ["verify"]),
      ),
    );
    const result = await verifyEdgeToken(fixture.token, keys, { issuer: ISSUER_EDGE, audience: AUDIENCE_AGENTS, nowSeconds: fixture.issuedAt });
    expect(result.ok).toBe(true);
    if (!result.ok) return;
    expect(result.claims).toMatchObject({ org: "org_1", team: "team_1", room: request.room, member: "usr_1", scopes: ["call:speak"] });
    expect(result.claims.exp - result.claims.iat).toBe(60);
  });
});
