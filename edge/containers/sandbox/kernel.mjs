// The sandbox's JavaScript and TypeScript kernel (live-layer A12): one
// long-lived vm context, so what one sandbox_execute_code call defines is
// there for the next, as with the Python kernel (kernel.py, which describes
// the HTTP protocol; this one adds "language" to /exec).
//
// TypeScript, and JavaScript with import statements, are compiled to
// CommonJS with the TypeScript compiler first; require resolves from the
// workspace. A cell with top-level await runs in an async function: what it
// assigns to globalThis stays, its own declarations don't. A synchronous
// cell past its timeout is interrupted by vm; an asynchronous one is
// abandoned and answered as timed out.
import { createServer } from "node:http";
import { createRequire } from "node:module";
import { mkdirSync } from "node:fs";
import { Console } from "node:console";
import { Writable } from "node:stream";
import vm from "node:vm";
import { inspect } from "node:util";

const PORT = Number(process.env.ORYXA_JS_KERNEL_PORT ?? 8898);
const WORKSPACE = process.env.ORYXA_WORKSPACE ?? "/workspace";
const TYPESCRIPT = process.env.ORYXA_TYPESCRIPT ?? "/usr/lib/node_modules/typescript/lib/typescript.js";
const MAX_BODY = 2 << 20;

mkdirSync(WORKSPACE, { recursive: true });
process.chdir(WORKSPACE);
const require = createRequire(`${WORKSPACE}/`);
const ts = createRequire(import.meta.url)(TYPESCRIPT);

let context;
let output = { stdout: null, stderr: null };

class Capped extends Writable {
  constructor(limit) {
    super();
    this.limit = limit;
    this.chunks = [];
    this.size = 0;
    this.truncated = false;
  }
  _write(chunk, _encoding, done) {
    const room = this.limit - this.size;
    if (room > 0) {
      const kept = chunk.subarray(0, room);
      this.chunks.push(kept);
      this.size += kept.length;
    }
    if (chunk.length > room) this.truncated = true;
    done();
  }
  text() {
    return Buffer.concat(this.chunks).toString("utf8");
  }
}

function fresh() {
  const module = { exports: {} };
  const sandbox = {
    console: new Console({ stdout: new Writable({ write: (c, e, d) => output.stdout.write(c, e, d) }), stderr: new Writable({ write: (c, e, d) => output.stderr.write(c, e, d) }) }),
    require,
    module,
    exports: module.exports,
    process,
    Buffer,
    setTimeout,
    clearTimeout,
    setInterval,
    clearInterval,
    setImmediate,
    URL,
    URLSearchParams,
    TextEncoder,
    TextDecoder,
    fetch,
    structuredClone,
    __dirname: WORKSPACE,
    __filename: `${WORKSPACE}/cell.js`,
  };
  context = vm.createContext(sandbox);
}
fresh();

function compile(code, language) {
  const needsCompiler = language === "typescript" || /^\s*(import|export)\s/m.test(code);
  if (!needsCompiler) return code;
  return ts.transpileModule(code, {
    compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022, esModuleInterop: true },
  }).outputText;
}

async function run({ code = "", language = "javascript", timeoutMs = 120000, stdoutLimit = 192 << 10, stderrLimit = 64 << 10 }) {
  const started = Date.now();
  output = { stdout: new Capped(stdoutLimit), stderr: new Capped(stderrLimit) };
  let exitCode = 0;
  let timedOut = false;
  try {
    let source = compile(String(code), language);
    const asynchronous = /(^|[^\w.])await\s/.test(source);
    if (asynchronous) source = `(async () => {\n${source}\n})()`;
    let value = new vm.Script(source, { filename: "cell.js" }).runInContext(context, { timeout: Math.max(timeoutMs, 1) });
    if (asynchronous) {
      let timer;
      const limit = new Promise((_, reject) => {
        timer = setTimeout(() => reject(Object.assign(new Error("timed out"), { oryxaTimeout: true })), Math.max(timeoutMs - (Date.now() - started), 1));
      });
      try {
        value = await Promise.race([value, limit]);
      } finally {
        clearTimeout(timer);
      }
    } else if (value !== undefined) {
      output.stdout.write(`${inspect(value, { depth: 4 })}\n`);
    }
  } catch (error) {
    if (error?.oryxaTimeout || error?.code === "ERR_SCRIPT_EXECUTION_TIMEOUT") {
      timedOut = true;
      exitCode = -1;
    } else {
      exitCode = 1;
      output.stderr.write(`${error?.stack ?? String(error)}\n`);
    }
  }
  return {
    stdout: output.stdout.text(),
    stderr: output.stderr.text(),
    exitCode,
    timedOut,
    truncated: output.stdout.truncated || output.stderr.truncated,
    durationMs: Date.now() - started,
  };
}

let queue = Promise.resolve();

createServer((request, response) => {
  const answer = (status, body) => {
    response.writeHead(status, { "content-type": "application/json" });
    response.end(JSON.stringify(body));
  };
  if (request.method === "GET" && request.url === "/health") return answer(200, { ok: true, pid: process.pid });
  if (request.method !== "POST") return answer(404, { error: "not found" });
  const chunks = [];
  let size = 0;
  request.on("data", (chunk) => {
    size += chunk.length;
    if (size <= MAX_BODY) chunks.push(chunk);
  });
  request.on("end", () => {
    if (size > MAX_BODY) return answer(413, { error: "too large" });
    let body;
    try {
      body = JSON.parse(Buffer.concat(chunks).toString("utf8") || "{}");
    } catch {
      return answer(400, { error: "not JSON" });
    }
    if (request.url === "/reset") {
      queue = queue.then(() => fresh());
      return queue.then(() => answer(200, { reset: true }));
    }
    if (request.url !== "/exec") return answer(404, { error: "not found" });
    queue = queue.then(() => run(body)).then((outcome) => answer(200, outcome), (error) => answer(500, { error: String(error) }));
  });
}).listen(PORT, "0.0.0.0");
