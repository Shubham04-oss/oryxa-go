// The real code kernels (containers/sandbox/kernel.py and kernel.mjs), run
// here as the container runs them, minus the container: one cell sets a
// variable and the next reads it (A12).
import { type ChildProcess, spawn } from "node:child_process";
import { mkdtempSync } from "node:fs";
import { createRequire } from "node:module";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { afterAll, beforeAll, describe, expect, it } from "vitest";

const CONTAINER = join(__dirname, "../../containers/sandbox");
const workspace = mkdtempSync(join(tmpdir(), "oryxa-kernel-"));
const typescript = createRequire(import.meta.url).resolve("typescript");

interface Kernel {
  port: number;
  process: ChildProcess;
}

const running: Kernel[] = [];

async function start(command: string, script: string, portVariable: string): Promise<Kernel> {
  const port = 20_000 + Math.floor(Math.random() * 20_000);
  const process = spawn(command, [join(CONTAINER, script)], {
    env: { ...globalThis.process.env, [portVariable]: String(port), ORYXA_WORKSPACE: workspace, ORYXA_TYPESCRIPT: typescript },
    stdio: "ignore",
  });
  const kernel = { port, process };
  running.push(kernel);
  for (let attempt = 0; attempt < 100; attempt++) {
    try {
      if ((await fetch(`http://127.0.0.1:${port}/health`)).ok) return kernel;
    } catch {
      // still starting
    }
    await new Promise((resolve) => setTimeout(resolve, 100));
  }
  throw new Error(`${script} did not start`);
}

async function cell(kernel: Kernel, code: string, extra: Record<string, unknown> = {}) {
  const response = await fetch(`http://127.0.0.1:${kernel.port}/exec`, { method: "POST", body: JSON.stringify({ code, ...extra }) });
  return (await response.json()) as { stdout: string; stderr: string; exitCode: number; timedOut: boolean; truncated: boolean };
}

async function reset(kernel: Kernel) {
  await fetch(`http://127.0.0.1:${kernel.port}/reset`, { method: "POST", body: "{}" });
}

let python: Kernel;
let node: Kernel;

beforeAll(async () => {
  [python, node] = await Promise.all([
    start("python3", "kernel.py", "ORYXA_PY_KERNEL_PORT"),
    start("node", "kernel.mjs", "ORYXA_JS_KERNEL_PORT"),
  ]);
}, 30_000);

afterAll(() => {
  for (const kernel of running) kernel.process.kill();
});

describe("the Python kernel", () => {
  it("keeps a variable from one cell to the next, and echoes a last expression", async () => {
    expect(await cell(python, "import json\nrows = [1, 2, 3]\nprint('set')")).toMatchObject({ stdout: "set\n", exitCode: 0 });
    expect(await cell(python, "sum(rows) + 36")).toMatchObject({ stdout: "42\n", exitCode: 0 });
  });

  it("forgets everything on reset", async () => {
    await cell(python, "kept = 1");
    await reset(python);
    const after = await cell(python, "kept");
    expect(after.exitCode).toBe(1);
    expect(after.stderr).toContain("NameError");
    expect(after.stderr).not.toContain("kernel.py");
  });

  it("captures what a subprocess prints, and caps output", async () => {
    expect((await cell(python, "import subprocess\n_ = subprocess.run(['echo', 'child'])")).stdout).toBe("child\n");
    expect(await cell(python, "print('a' * 100)", { stdoutLimit: 10 })).toMatchObject({ stdout: "aaaaaaaaaa", truncated: true });
  });

  it("answers a run past its timeout, then exits so a fresh kernel takes over", async () => {
    const timed = await cell(python, "import time\ntime.sleep(5)", { timeoutMs: 300 });
    expect(timed).toMatchObject({ timedOut: true, exitCode: -1 });
    await new Promise((resolve) => setTimeout(resolve, 300));
    expect(python.process.exitCode).toBe(3);
    python = await start("python3", "kernel.py", "ORYXA_PY_KERNEL_PORT");
  }, 20_000);
});

describe("the JavaScript and TypeScript kernel", () => {
  it("keeps declarations from one cell to the next, across languages", async () => {
    await cell(node, "const prices = [10, 20]; let total = 0;", { language: "javascript" });
    await cell(node, "total = prices.reduce((a, b) => a + b, 0);", { language: "javascript" });
    expect(await cell(node, "const doubled: number = total * 2; doubled", { language: "typescript" })).toMatchObject({ stdout: "60\n", exitCode: 0 });
  });

  it("compiles imports and runs top-level await", async () => {
    expect((await cell(node, 'import path from "path"; console.log(path.basename("/a/b.txt"))')).stdout).toBe("b.txt\n");
    expect((await cell(node, "globalThis.later = await Promise.resolve(7); console.log(later)")).stdout).toBe("7\n");
  });

  it("interrupts a runaway cell and keeps the context", async () => {
    await cell(node, "const survives = 'yes'");
    expect(await cell(node, "while (true) {}", { timeoutMs: 300 })).toMatchObject({ timedOut: true, exitCode: -1 });
    expect((await cell(node, "survives")).stdout).toBe("'yes'\n");
  });

  it("forgets everything on reset", async () => {
    await cell(node, "const gone = 1");
    await reset(node);
    expect((await cell(node, "typeof gone")).stdout).toBe("'undefined'\n");
  });
});
