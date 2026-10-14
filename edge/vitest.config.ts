// Two projects: the contract test reads the shared fixtures from disk in
// Node; everything else runs inside workerd with this Worker's bindings.
// The workers project gets a fresh P-256 key pair: the public half is the
// Worker's trusted agents key, and tests sign tokens with the private half
// (test/tokens.ts), the way agents' KMS key would.
import { cloudflareTest } from "@cloudflare/vitest-plugin";
import { generateKeyPairSync } from "node:crypto";
import { defineConfig } from "vitest/config";

const { publicKey, privateKey } = generateKeyPairSync("ec", { namedCurve: "P-256" });
const spki = publicKey.export({ type: "spki", format: "der" }).toString("base64");
const jwk = JSON.stringify(privateKey.export({ format: "jwk" }));

export default defineConfig({
  test: {
    projects: [
      {
        test: { name: "contracts", include: ["src/**/*.node.test.ts", "src/contracts/**/*.test.ts"], environment: "node" },
      },
      {
        plugins: [
          cloudflareTest({
            wrangler: { configPath: "./wrangler.jsonc" },
            // Workers AI (the call agent's voice) is remote; tests fake it.
            remoteBindings: false,
            miniflare: {
              bindings: {
                AGENTS_TOKEN_PUBLIC_KEYS: JSON.stringify([spki]),
                TEST_SIGNING_KEY: jwk,
                ENVIRONMENT: "test",
                EDGE_URL_SIGNING_KEY: "test-signing-key-test-signing-key-0123456789",
                EDGE_PUBLIC_URL: "https://edge.test",
              },
            },
          }),
        ],
        test: { name: "workers", include: ["src/**/*.worker.test.ts", "test/**/*.test.ts"] },
      },
    ],
  },
});
