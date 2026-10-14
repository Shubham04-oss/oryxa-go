// The edge's half of the token contract: tokens Go's serviceauth signed
// (contracts/edge/v1/testdata/tokens, re-signed by `go test
// ./internal/serviceauth -run TestEdgeTokenFixture -update`) are accepted or
// refused exactly as the fixture says.
import { describe, expect, it } from "vitest";
import fixture from "../../../contracts/edge/v1/testdata/tokens/edge_tokens.json";
import { importKeys, verifyEdgeToken } from "./token";

describe("tokens Go signed", () => {
  for (const test of fixture.cases) {
    it(`${test.name}: ${test.want}`, async () => {
      const keys = await importKeys(JSON.stringify([fixture.publicKey]));
      const result = await verifyEdgeToken(test.token, keys, { nowSeconds: test.now });
      expect(result.ok ? "ok" : result.reason).toBe(test.want);
    });
  }

  it("refuses every token when no key is configured", async () => {
    const valid = fixture.cases.find((test) => test.want === "ok")!;
    const result = await verifyEdgeToken(valid.token, await importKeys("[]"), { nowSeconds: valid.now });
    expect(result).toEqual({ ok: false, reason: "no_keys" });
  });

  it("accepts a token signed by any configured key, for rotation", async () => {
    const valid = fixture.cases.find((test) => test.want === "ok")!;
    const other = (await crypto.subtle.generateKey({ name: "ECDSA", namedCurve: "P-256" }, true, ["sign", "verify"])) as CryptoKeyPair;
    const otherSpki = new Uint8Array((await crypto.subtle.exportKey("spki", other.publicKey)) as ArrayBuffer);
    const keys = await importKeys(JSON.stringify([btoa(String.fromCharCode(...otherSpki)), fixture.publicKey]));
    expect((await verifyEdgeToken(valid.token, keys, { nowSeconds: valid.now })).ok).toBe(true);
  });
});
