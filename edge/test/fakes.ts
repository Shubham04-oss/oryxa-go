// Fakes for what workerd can't run here: the linux/amd64 sandbox container
// and a Browser Run session. They implement the same interfaces SandboxDO and
// BrowserDO use, so every room, access, logging and R2 path is the real one.
import type { DirectoryBackupRecord } from "@cloudflare/sandbox";
import type { BrowserDriver, HandoffInfo } from "../src/browser/driver";
import { SNAPSHOT_EXPRESSION } from "../src/browser/scripts";
import { type DirEntry, type ExecOptions, type ExecOutcome, FileError, type Workspace } from "../src/sandbox/workspace";

export type ExecHandler = (argv: string[], options: ExecOptions, workspace: FakeWorkspace) => ExecOutcome | Promise<ExecOutcome>;

// One registry per test file: tests reach the fake a DO built by its name.
export const workspaces = new Map<string, FakeWorkspace>();
export const drivers = new Map<string, FakeDriver>();

export class FakeWorkspace implements Workspace {
  files = new Map<string, Uint8Array>();
  isRunning = false;
  starts = 0;
  stops = 0;
  backups: DirectoryBackupRecord[] = [];
  restored: DirectoryBackupRecord[] = [];
  changedSinceMark = new Set<string>();
  handler: ExecHandler | null = null;
  ports = new Map<number, (request: Request) => Response>();
  // The code kernels (A12): each records its cells and resets, and its pid
  // changes when a test says it restarted. kernelRun answers a cell.
  // wedged is a kernel stuck in a cell: it answers nothing until pkill
  // kills it, and start.sh brings a fresh one (a new pid).
  kernels = new Map<number, { pid: number; cells: { code: string; language: string }[]; resets: number; wedged: boolean; kills: number }>([
    [8899, { pid: 100, cells: [], resets: 0, wedged: false, kills: 0 }],
    [8898, { pid: 200, cells: [], resets: 0, wedged: false, kills: 0 }],
  ]);
  kernelRun: ((body: { code: string; language: string; timeoutMs: number }, workspace: FakeWorkspace) => ExecOutcome) | null = null;

  running(): boolean {
    return this.isRunning;
  }

  async start(): Promise<boolean> {
    if (this.isRunning) return false;
    this.isRunning = true;
    this.starts++;
    return true;
  }

  async stop(): Promise<void> {
    if (!this.isRunning) return;
    this.isRunning = false;
    this.stops++;
    this.files.clear();
  }

  async exec(argv: string[], options: ExecOptions): Promise<ExecOutcome> {
    if (!this.isRunning) throw new Error("container not running");
    if (argv[0] === "pkill") {
      const port = argv[3]?.endsWith("kernel.py") ? 8899 : 8898;
      const kernel = this.kernels.get(port)!;
      kernel.kills++;
      kernel.wedged = false;
      kernel.pid++;
      return { stdout: "", stderr: "", exitCode: 0, truncated: false, timedOut: false };
    }
    if (argv[0] === "sh" && argv[2]?.includes("touch /tmp/oryxa/mark")) {
      this.changedSinceMark.clear();
      const command = argv.slice(4);
      if (this.handler) return this.handler(command, options, this);
      if (command[0] === "python3" || command[0] === "node" || command[0] === "tsx") {
        const code = new TextDecoder().decode(this.files.get(command[1]!) ?? new Uint8Array());
        return { stdout: `${command[0]}: ${code}\n`, stderr: "", exitCode: 0, truncated: false, timedOut: false };
      }
      return { stdout: `bash: ${command.slice(2).join(" ")}\n`, stderr: "", exitCode: 0, truncated: false, timedOut: false };
    }
    if (argv[0] === "sh" && argv[2]?.startsWith("find ")) {
      return { stdout: [...this.changedSinceMark].join("\n"), stderr: "", exitCode: 0, truncated: false, timedOut: false };
    }
    return { stdout: "", stderr: "", exitCode: 0, truncated: false, timedOut: false };
  }

  // write is how a fake process changes a file.
  write(path: string, content: string | Uint8Array): void {
    this.files.set(path, typeof content === "string" ? new TextEncoder().encode(content) : content);
    this.changedSinceMark.add(path);
  }

  async readDirectory(path: string): Promise<DirEntry[]> {
    const base = path.replace(/\/$/, "");
    const entries = new Map<string, DirEntry>();
    for (const [file, bytes] of this.files) {
      if (!file.startsWith(`${base}/`)) continue;
      const rest = file.slice(base.length + 1);
      const [first, ...more] = rest.split("/");
      entries.set(first!, more.length ? { name: first!, type: "directory" } : { name: first!, type: "file", size: bytes.length });
    }
    if (entries.size === 0 && !this.isDirectory(base)) throw new FileError("ENOENT", `${path}: no such file or directory`);
    return [...entries.values()];
  }

  private isDirectory(path: string): boolean {
    return path === "/workspace" || [...this.files.keys()].some((file) => file.startsWith(`${path}/`));
  }

  async readFile(path: string, limit: number): Promise<{ bytes: Uint8Array; size: number; truncated: boolean }> {
    const bytes = this.files.get(path);
    if (!bytes) {
      if (this.isDirectory(path)) throw new FileError("EISDIR", `${path} is a directory`);
      throw new FileError("ENOENT", `${path}: no such file or directory`);
    }
    return { bytes: bytes.subarray(0, limit), size: bytes.length, truncated: bytes.length > limit };
  }

  async openFile(path: string): Promise<{ body: ReadableStream<Uint8Array>; size: number }> {
    const bytes = this.files.get(path);
    if (!bytes) throw new FileError("ENOENT", `${path}: no such file or directory`);
    return { body: new Blob([bytes]).stream(), size: bytes.length };
  }

  async writeFile(path: string, content: Uint8Array): Promise<void> {
    this.files.set(path, content);
  }

  async remove(path: string): Promise<void> {
    let removed = this.files.delete(path);
    for (const file of [...this.files.keys()]) {
      if (file.startsWith(`${path}/`)) removed = this.files.delete(file) || removed;
    }
    if (!removed) throw new FileError("ENOENT", `${path}: no such file or directory`);
  }

  async backup(dir: string): Promise<DirectoryBackupRecord> {
    const record = {
      id: crypto.randomUUID(),
      dir,
      size: [...this.files.values()].reduce((sum, bytes) => sum + bytes.length, 0),
      sha256: "0".repeat(64),
      format: "tar+zstd/1",
      snapshot: [...this.files.entries()].map(([path, bytes]) => [path, [...bytes]]),
    } as unknown as DirectoryBackupRecord;
    this.backups.push(record);
    return record;
  }

  async restore(record: DirectoryBackupRecord): Promise<void> {
    this.restored.push(record);
    for (const [path, bytes] of (record as unknown as { snapshot: [string, number[]][] }).snapshot) {
      this.files.set(path, new Uint8Array(bytes));
    }
  }

  port(port: number): Fetcher {
    const kernel = this.kernels.get(port);
    if (kernel && !this.ports.has(port)) {
      return {
        fetch: async (input: RequestInfo | URL, init?: RequestInit) => {
          const request = new Request(input, init);
          const path = new URL(request.url).pathname;
          const stuck = () =>
            new Promise<Response>((_, reject) => {
              const signal = init?.signal;
              if (signal?.aborted) return reject(signal.reason);
              signal?.addEventListener("abort", () => reject(signal.reason));
            });
          if (kernel.wedged) return stuck();
          if (path === "/health") return Response.json({ ok: true, pid: kernel.pid });
          if (path === "/reset") {
            kernel.resets++;
            return Response.json({ reset: true });
          }
          const body = (await request.json()) as { code: string; language: string; timeoutMs: number };
          kernel.cells.push({ code: body.code, language: body.language });
          if (body.code.includes("await null; while (true)")) {
            kernel.wedged = true;
            return stuck();
          }
          const outcome = this.kernelRun?.(body, this) ?? {
            stdout: `${body.language}: ${body.code}\n`,
            stderr: "",
            exitCode: 0,
            truncated: false,
            timedOut: false,
          };
          return Response.json(outcome);
        },
      } as unknown as Fetcher;
    }
    const handler = this.ports.get(port);
    return {
      fetch: async (input: RequestInfo | URL, init?: RequestInit) => {
        const request = new Request(input, init);
        return handler ? handler(request) : new Response("nothing listening", { status: 502 });
      },
    } as unknown as Fetcher;
  }

  async shell(): Promise<ExecProcess> {
    throw new Error("no terminal in the fake");
  }
}

export interface FakePage {
  title: string;
  text: string;
  elements: { selector: string; tag: string; text?: string; href?: string; type?: string }[];
}

// Browser Run's own list of running sessions: it outlives any one driver,
// as the real sessions outlive a DO that is evicted.
export const browserRun = new Set<string>();

export class FakeDriver implements BrowserDriver {
  pages = new Map<string, FakePage>();
  url = "about:blank";
  // The session this driver is connected to; null on a cold driver.
  sessionId: string | null = null;
  launched = 0;
  reconnected = 0;
  closed = 0;
  failClose = false;
  failLaunch = false;
  handoffActive = false;
  handoffResult: { success: boolean; reason?: string } | null = null;
  filled = new Map<string, string>();

  // evict forgets the connection, as a new PuppeteerDriver after a DO
  // restart starts with none; the session keeps running in Browser Run.
  evict(): void {
    this.sessionId = null;
  }

  // expire ends the session on Browser Run's side (its keep-alive ran out).
  expire(): void {
    for (const id of this.owned) browserRun.delete(id);
    this.sessionId = null;
  }

  private owned = new Set<string>();

  async open(sessionId: string | undefined): Promise<{ sessionId: string; created: boolean }> {
    if (await this.attach(sessionId)) return { sessionId: this.sessionId!, created: false };
    if (this.failLaunch) throw new Error("concurrent session limit reached");
    this.sessionId = `bs_${crypto.randomUUID().slice(0, 8)}`;
    browserRun.add(this.sessionId);
    this.owned.add(this.sessionId);
    this.launched++;
    this.url = "about:blank";
    return { sessionId: this.sessionId, created: true };
  }

  async attach(sessionId: string | undefined): Promise<boolean> {
    if (this.sessionId && browserRun.has(this.sessionId) && (!sessionId || sessionId === this.sessionId)) return true;
    if (!sessionId || !browserRun.has(sessionId)) {
      this.sessionId = null;
      return false;
    }
    this.sessionId = sessionId;
    this.reconnected++;
    return true;
  }

  async alive(sessionId: string): Promise<boolean> {
    return browserRun.has(sessionId);
  }

  private connected(): void {
    if (!this.sessionId || !browserRun.has(this.sessionId)) throw new Error("browser session is not open");
  }

  async evaluate<T>(expression: string): Promise<T> {
    if (expression === SNAPSHOT_EXPRESSION) {
      const page = this.pages.get(this.url) ?? { title: "", text: "", elements: [] };
      return { title: page.title, url: this.url, text: page.text, elements: page.elements, elements_truncated: false } as T;
    }
    const selector = JSON.parse(/document\.querySelector\(("(?:[^"\\]|\\.)*")\)/.exec(expression)![1]!) as string;
    const element = this.pages.get(this.url)?.elements.find((candidate) => candidate.selector === selector);
    if (!element) throw new Error("No element matches the CSS selector");
    if (expression.includes("element.click()")) {
      if (element.href) this.url = element.href;
      return true as T;
    }
    if (element.type === "password") throw new Error("Browser tools never fill password or file-input fields");
    const value = /setter\.call\(element, ("(?:[^"\\]|\\.)*")\)/.exec(expression)![1]!;
    this.filled.set(selector, JSON.parse(value));
    return true as T;
  }

  async navigate(url: string): Promise<void> {
    if (!this.pages.has(url)) throw new Error("net::ERR_NAME_NOT_RESOLVED");
    this.url = url;
  }

  async waitUntilReady(): Promise<void> {}

  async screenshot(): Promise<Uint8Array> {
    return new Uint8Array([0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a, ...new TextEncoder().encode(this.url)]);
  }

  async liveView(mode: "tab" | "full"): Promise<string> {
    this.connected();
    return `https://live.browser.run/${this.sessionId}?mode=${mode}`;
  }

  async handoff(): Promise<string> {
    this.handoffActive = true;
    return "hnd_fake";
  }

  async handoffState(): Promise<HandoffInfo> {
    this.connected();
    return { active: this.handoffActive, ...(this.handoffActive ? { handoffId: "hnd_fake" } : {}) };
  }

  async handoffComplete(success: boolean, reason: string | undefined): Promise<void> {
    this.connected();
    if (!this.handoffActive) throw new Error("no handoff in progress");
    this.handoffActive = false;
    this.handoffResult = { success, ...(reason ? { reason } : {}) };
  }

  async close(sessionId?: string): Promise<void> {
    const attached = this.sessionId;
    this.sessionId = null;
    for (const id of new Set([attached, sessionId])) {
      if (!id || !browserRun.has(id)) continue;
      if (this.failClose) throw new Error("Browser Run did not answer");
      browserRun.delete(id);
      this.closed++;
    }
    this.url = "about:blank";
  }

}
