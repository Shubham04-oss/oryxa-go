// Workspace is what SandboxDO needs from a running Linux sandbox: processes
// and files. ContainerWorkspace is the real one, a Cloudflare Container with
// @cloudflare/sandbox 1.0's Files and DirectoryBackup; tests use a fake (it
// can't run here: the image and its shim are linux/amd64).
import { DirectoryBackup, type DirectoryBackupRecord, Files, SandboxFileError } from "@cloudflare/sandbox";

// AGENT_USER is the unprivileged user the image creates; every process and
// file operation runs as it.
export const AGENT_USER = "1000:1000";
export const WORKSPACE_DIR = "/workspace";

export interface ExecOptions {
  cwd?: string;
  timeoutMs: number;
  stdoutLimit: number;
  stderrLimit: number;
  env?: Record<string, string>;
}

export interface ExecOutcome {
  stdout: string;
  stderr: string;
  exitCode: number;
  truncated: boolean;
  timedOut: boolean;
}

export interface DirEntry {
  name: string;
  type: "file" | "directory" | "symlink" | "other";
  size?: number;
}

// FileError is a filesystem failure in the sandbox, such as ENOENT.
export class FileError extends Error {
  constructor(
    readonly errno: string,
    message: string,
  ) {
    super(message);
  }
}

export interface Workspace {
  // start starts the sandbox if it isn't running and reports whether it did.
  start(): Promise<boolean>;
  running(): boolean;
  stop(): Promise<void>;
  exec(argv: string[], options: ExecOptions): Promise<ExecOutcome>;
  readDirectory(path: string): Promise<DirEntry[]>;
  // readFile returns at most limit bytes, and whether there was more.
  readFile(path: string, limit: number): Promise<{ bytes: Uint8Array; size: number; truncated: boolean }>;
  // openFile streams a whole file, for copying it to R2.
  openFile(path: string): Promise<{ body: ReadableStream<Uint8Array>; size: number }>;
  writeFile(path: string, content: Uint8Array): Promise<void>;
  remove(path: string): Promise<void>;
  backup(dir: string): Promise<DirectoryBackupRecord>;
  restore(record: DirectoryBackupRecord): Promise<void>;
  // port is a fetcher for a TCP port inside the sandbox: previews.
  port(port: number): Fetcher;
  // shell starts an interactive login shell on a PTY: the terminal.
  shell(cols: number, rows: number): Promise<ExecProcess>;
}

export interface ContainerWorkspaceOptions {
  image: string;
  instance: ContainerStartupOptions["instance"];
  labels: Record<string, string>;
  // egress receives every plain-HTTP request the sandbox makes.
  egress: Fetcher;
  backups: DirectoryBackup;
  idleTimeoutMs: number;
}

const START_TIMEOUT_MS = 60_000;

export class ContainerWorkspace implements Workspace {
  private readonly files: Files;

  constructor(
    private readonly container: Container,
    private readonly options: ContainerWorkspaceOptions,
  ) {
    this.files = new Files(container);
  }

  running(): boolean {
    return this.container.running;
  }

  async start(): Promise<boolean> {
    if (this.container.running) return false;
    this.container.start({
      image: this.options.image,
      enableInternet: true,
      instance: this.options.instance,
      labels: this.options.labels,
      env: { HOME: "/home/agent", LANG: "C.UTF-8" },
    });
    // Backups take their own routes first; every other plain-HTTP request
    // goes through egress, which refuses metadata and private addresses.
    await this.options.backups.intercept();
    await this.container.interceptAllOutboundHttp(this.options.egress);
    await this.container.setInactivityTimeout(this.options.idleTimeoutMs);
    const deadline = Date.now() + START_TIMEOUT_MS;
    for (let delay = 100; ; delay = Math.min(delay * 2, 2_000)) {
      try {
        const probe = await this.container.exec(["true"], { stdout: "ignore", stderr: "ignore" });
        if ((await probe.exitCode) === 0) return true;
      } catch (error) {
        if (Date.now() > deadline) throw error;
      }
      await new Promise((resolve) => setTimeout(resolve, delay));
    }
  }

  async stop(): Promise<void> {
    if (this.container.running) await this.container.destroy();
  }

  async exec(argv: string[], options: ExecOptions): Promise<ExecOutcome> {
    const abort = new AbortController();
    let timedOut = false;
    const timer = setTimeout(() => {
      timedOut = true;
      abort.abort();
    }, options.timeoutMs);
    try {
      const process = await this.container.exec(argv, {
        cwd: options.cwd ?? WORKSPACE_DIR,
        user: AGENT_USER,
        env: options.env,
        signal: abort.signal,
        stdout: "pipe",
        stderr: "pipe",
      });
      const [stdout, stderr] = await Promise.all([
        readCapped(process.stdout, options.stdoutLimit),
        readCapped(process.stderr, options.stderrLimit),
      ]);
      let exitCode = -1;
      try {
        exitCode = await process.exitCode;
      } catch {
        // Killed at its timeout: there is no exit code to report.
      }
      return {
        stdout: stdout.text,
        stderr: stderr.text,
        exitCode: timedOut ? -1 : exitCode,
        truncated: stdout.truncated || stderr.truncated,
        timedOut,
      };
    } finally {
      clearTimeout(timer);
    }
  }

  async readDirectory(path: string): Promise<DirEntry[]> {
    const entries = await wrapFiles(() => this.files.readDirectory(path, { user: AGENT_USER }));
    const result: DirEntry[] = [];
    for (const entry of entries) {
      const type = entry.type === "file" || entry.type === "directory" || entry.type === "symlink" ? entry.type : "other";
      let size: number | undefined;
      if (type === "file") {
        try {
          size = Number((await this.files.stat(`${path.replace(/\/$/, "")}/${entry.name}`, { user: AGENT_USER })).size);
        } catch {
          size = undefined;
        }
      }
      result.push({ name: entry.name, type, ...(size === undefined ? {} : { size }) });
    }
    return result;
  }

  async readFile(path: string, limit: number): Promise<{ bytes: Uint8Array; size: number; truncated: boolean }> {
    const stat = await wrapFiles(() => this.files.stat(path, { user: AGENT_USER }));
    if (stat.type === "directory") throw new FileError("EISDIR", `${path} is a directory`);
    const response = await wrapFiles(() => this.files.readFile(path, { user: AGENT_USER }));
    const read = await readBytes(response.body, limit);
    return { bytes: read.bytes, size: Number(stat.size), truncated: read.truncated };
  }

  async openFile(path: string): Promise<{ body: ReadableStream<Uint8Array>; size: number }> {
    const stat = await wrapFiles(() => this.files.stat(path, { user: AGENT_USER }));
    if (stat.type !== "file") throw new FileError("EISDIR", `${path} is not a file`);
    const response = await wrapFiles(() => this.files.readFile(path, { user: AGENT_USER }));
    if (!response.body) throw new FileError("EIO", `${path} has no content`);
    return { body: response.body as ReadableStream<Uint8Array>, size: Number(stat.size) };
  }

  async writeFile(path: string, content: Uint8Array): Promise<void> {
    const parent = path.slice(0, path.lastIndexOf("/")) || "/";
    await wrapFiles(() => this.files.mkdir(parent, { recursive: true, user: AGENT_USER }));
    await wrapFiles(() => this.files.writeFile(path, content, { user: AGENT_USER }));
  }

  async remove(path: string): Promise<void> {
    await wrapFiles(() => this.files.remove(path, { recursive: true, user: AGENT_USER }));
  }

  backup(dir: string): Promise<DirectoryBackupRecord> {
    return this.options.backups.backup({ dir, exclude: ["node_modules/", ".venv/", "__pycache__/"] });
  }

  restore(record: DirectoryBackupRecord): Promise<void> {
    return this.options.backups.restore(record);
  }

  port(port: number): Fetcher {
    return this.container.getTcpPort(port);
  }

  shell(cols: number, rows: number): Promise<ExecProcess> {
    return this.container.exec(["bash", "-l"], {
      cwd: WORKSPACE_DIR,
      user: AGENT_USER,
      pty: { cols, rows },
      stdin: "pipe",
      env: { TERM: "xterm-256color" },
    });
  }
}

// wrapFiles turns @cloudflare/sandbox's SandboxFileError into FileError, so
// SandboxDO needn't know the package's error classes.
async function wrapFiles<T>(operation: () => Promise<T>): Promise<T> {
  try {
    return await operation();
  } catch (error) {
    if (SandboxFileError.is(error)) throw new FileError(error.code, error.message);
    throw error;
  }
}

// readCapped reads a process stream as UTF-8, keeping at most limit bytes
// and draining the rest so the process never blocks on a full pipe.
export async function readCapped(
  stream: ReadableStream | null,
  limit: number,
): Promise<{ text: string; truncated: boolean }> {
  const read = await readBytes(stream, limit);
  return { text: new TextDecoder().decode(read.bytes), truncated: read.truncated };
}

export async function readBytes(
  stream: ReadableStream | null,
  limit: number,
): Promise<{ bytes: Uint8Array; truncated: boolean }> {
  if (!stream) return { bytes: new Uint8Array(), truncated: false };
  const reader = stream.getReader();
  const chunks: Uint8Array[] = [];
  let kept = 0;
  let truncated = false;
  for (;;) {
    const { done, value } = await reader.read();
    if (done) break;
    const chunk = value instanceof Uint8Array ? value : new Uint8Array(value as ArrayBuffer);
    if (kept < limit) {
      const take = chunk.subarray(0, limit - kept);
      chunks.push(take);
      kept += take.length;
      if (take.length < chunk.length) truncated = true;
    } else if (chunk.length > 0) {
      truncated = true;
    }
  }
  const bytes = new Uint8Array(kept);
  let offset = 0;
  for (const chunk of chunks) {
    bytes.set(chunk, offset);
    offset += chunk.length;
  }
  // Don't split a UTF-8 sequence at the cap.
  if (truncated) {
    let end = bytes.length;
    while (end > 0 && end > bytes.length - 4 && (bytes[end - 1]! & 0xc0) === 0x80) end--;
    if (end > 0 && end < bytes.length && (bytes[end - 1]! & 0xc0) === 0xc0) end--;
    return { bytes: bytes.subarray(0, end), truncated };
  }
  return { bytes, truncated };
}
