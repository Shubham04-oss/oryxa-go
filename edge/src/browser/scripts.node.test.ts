// The page scripts must stay the ones agentcorebrowser runs, so a snapshot's
// selectors mean the same thing on Cloudflare as on AgentCore.
import { readFileSync } from "node:fs";
import { join } from "node:path";
import { expect, it } from "vitest";
import { SNAPSHOT_EXPRESSION, clickExpression, fillExpression } from "./scripts";

const cdp = readFileSync(join(__dirname, "../../../agents/primary/internal/agentcorebrowser/cdp.go"), "utf8");

it("snapshots with agentcorebrowser's script", () => {
  const go = cdp.slice(cdp.indexOf("const snapshotExpression = `") + "const snapshotExpression = `".length);
  expect(SNAPSHOT_EXPRESSION).toBe(go.slice(0, go.indexOf("`")));
});

it("clicks and fills with agentcorebrowser's scripts", () => {
  const goClick = /func \(c \*cdpClient\) click[\s\S]*?expression := fmt\.Sprintf\(`([\s\S]*?)`, literal\)/.exec(cdp)![1]!;
  expect(clickExpression("#x")).toBe(goClick.replace("%s", JSON.stringify("#x")));
  const goFill = /func \(c \*cdpClient\) fill[\s\S]*?expression := fmt\.Sprintf\(`([\s\S]*?)`, selectorLiteral/.exec(cdp)![1]!;
  const values = [JSON.stringify("#y"), JSON.stringify("hi"), JSON.stringify("hi"), JSON.stringify("hi"), JSON.stringify("hi")];
  expect(fillExpression("#y", "hi")).toBe(goFill.replace(/%s/g, () => values.shift()!));
});
