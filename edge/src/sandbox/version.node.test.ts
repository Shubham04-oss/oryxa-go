// sandbox-shim in the image and @cloudflare/sandbox in the Worker speak one
// protocol version: they must be the same release (A14).
import { readFileSync } from "node:fs";
import { join } from "node:path";
import { expect, it } from "vitest";

const edge = join(__dirname, "../..");

it("pins one Sandbox SDK version in the image, package.json and the lockfile", () => {
  const image = /FROM docker\.io\/cloudflare\/sandbox:(\S+) AS shim/.exec(readFileSync(join(edge, "containers/sandbox/Dockerfile"), "utf8"))?.[1];
  const declared = (JSON.parse(readFileSync(join(edge, "package.json"), "utf8")) as { dependencies: Record<string, string> }).dependencies["@cloudflare/sandbox"];
  const locked = [...readFileSync(join(edge, "pnpm-lock.yaml"), "utf8").matchAll(/'@cloudflare\/sandbox@([^']+)':/g)].map((match) => match[1]);
  expect(image).toMatch(/^\d+\.\d+\.\d+$/);
  expect(declared).toBe(image);
  expect(new Set(locked)).toEqual(new Set([image]));
});
