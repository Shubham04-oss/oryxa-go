// SandboxDO (A1): one Sandbox SDK 1.0 container per DO, named
// org/{org}/room/{kind}/{id}/sandbox/{name}. It implements the sandbox API
// of contract §5 for the room that owns it; the room has already checked the
// caller, so every method here trusts its ChildContext.
//
// Files outlive the container: when the room stops an idle sandbox, its
// /workspace is backed up to R2 (DirectoryBackup) and restored on the next
// start. Processes don't survive, which a result reports as
// sessionRecreated.
import { DurableObject } from "cloudflare:workers";
import { DirectoryBackup, type DirectoryBackupRecord } from "@cloudflare/sandbox";
import {
  ArtifactRequest,
  ExecCodeRequest,
  ExecCommandRequest,
  type ExecOutput,
  type ExecResult,
  type FileContent,
  type ListFilesResponse,
  PreviewRequest,
  ReadFilesRequest,
  RemoveFilesRequest,
  ROOM_RUN,
  type SignedURL,
  TerminalRequest,
  WriteFilesRequest,
  runPrefix,
} from "../contracts";
import type { Env } from "../env";
import { EdgeError, base64Decode, base64Encode } from "../http";
import { type ChildContext, type ChildEvent, type ChildReply, childError } from "../room/child";
import { SIGNED_URL_SECONDS, type SignedTarget, signTarget } from "../signed";
import { ContainerWorkspace, FileError, WORKSPACE_DIR, type Workspace } from "./workspace";

export const SANDBOX_IDLE_MS = 15 * 60 * 1000;
const DEFAULT_EXEC_MS = 120_000;
const MAX_EXEC_MS = 15 * 60 * 1000;
const STDOUT_LIMIT = 192 << 10;
const STDERR_LIMIT = 64 << 10;
const MAX_CODE_BYTES = 1 << 20;
const MAX_COMMAND_BYTES = 64 << 10;
const MAX_PATH_BYTES = 4 << 10;
const MAX_PATHS = 100;
const MAX_WRITE_FILES = 20;
const MAX_WRITE_BYTES = 5 << 20;
// READ_BUDGET bounds what one files/read returns, as AgentCore's toolset
// bounds one tool result: it goes back into the model's context.
const READ_BUDGET = 256 << 10;
const MAX_LIST_ENTRIES = 1_000;
const MAX_ARTIFACT_BYTES = 64 << 20;
const MAX_RESULT_IMAGE_BYTES = 512 << 10;
const MAX_RESULT_IMAGES_BYTES = 1 << 20;
const ARTIFACT_NAME = /^[A-Za-z0-9_-][A-Za-z0-9._-]{0,127}$/;

export type SandboxOp =
  | "exec/code"
  | "exec/command"
  | "files/list"
  | "files/read"
  | "files/write"
  | "files/remove"
  | "reset"
  | "artifact"
  | "preview"
  | "terminal"
  | "snapshot";

// WorkspaceFactory builds a DO's workspace. Tests replace it, since the real
// container can't run under workerd here.
export type WorkspaceFactory = (sandbox: SandboxDO, env: Env, identity: SandboxIdentity) => Workspace;

let workspaceFactory: WorkspaceFactory | null = null;

export function setWorkspaceFactory(factory: WorkspaceFactory | null): void {
  workspaceFactory = factory;
}

export interface SandboxIdentity {
  org: string;
  room: ChildContext["room"];
  name: string;
}

export class SandboxDO extends DurableObject<Env> {
  private workspaceInstance: Workspace | null = null;
  private queue: Promise<unknown> = Promise.resolve();

  // ---- RPC from the room ----

  async op(context: ChildContext, op: SandboxOp, body: unknown, query: Record<string, string> = {}): Promise<ChildReply> {
    const events: ChildEvent[] = [];
    try {
      await this.bind(context);
      const result = await this.serialize(() => this.run(context, op, body, query, events));
      return { status: 200, body: result, events };
    } catch (error) {
      return childError(error, events);
    }
  }

  // stop backs up the workspace and stops the container: the room calls it
  // when the sandbox has been idle, or when the room stops it.
  async stop(reason: string): Promise<ChildReply> {
    const events: ChildEvent[] = [];
    try {
      await this.serialize(async () => {
        const workspace = await this.workspace();
        if (!workspace.running()) return;
        const started = Date.now();
        try {
          const record = await workspace.backup(WORKSPACE_DIR);
          await this.saveBackup(record);
        } catch (error) {
          console.error(JSON.stringify({ msg: "sandbox backup before stop failed", error: String(error) }));
        }
        await workspace.stop();
        events.push({ type: "sandbox.stopped", summary: `Sandbox stopped (${reason})`, data: { reason }, durationMs: Date.now() - started });
      });
      return { status: 200, body: { stopped: true }, events };
    } catch (error) {
      return childError(error, events);
    }
  }

  // ---- preview and terminal, through signed URLs ----

  override async fetch(request: Request): Promise<Response> {
    const header = request.headers.get("x-oryxa-signed");
    if (!header) return new Response("not found", { status: 404 });
    const target = JSON.parse(header) as SignedTarget;
    const workspace = await this.workspace();
    if (target.t === "preview") {
      if (!workspace.running()) return new Response("This sandbox is asleep. Ask the agent to start it again.", { status: 503 });
      const incoming = new URL(request.url);
      const rest = request.headers.get("x-oryxa-rest") ?? "/";
      const upstream = new URL(`http://localhost:${target.port}${rest}${incoming.search}`);
      const headers = new Headers(request.headers);
      for (const name of ["x-oryxa-signed", "x-oryxa-rest", "cookie", "authorization"]) headers.delete(name);
      return workspace.port(target.port!).fetch(new Request(upstream, { method: request.method, headers, body: request.body, redirect: "manual" }));
    }
    if (target.t === "terminal") {
      if (request.headers.get("upgrade")?.toLowerCase() !== "websocket") return new Response("expected a WebSocket", { status: 426 });
      await this.serialize(() => workspace.start());
      const cols = Number(request.headers.get("x-oryxa-cols") ?? 120) || 120;
      const rows = Number(request.headers.get("x-oryxa-rows") ?? 32) || 32;
      const process = await workspace.shell(cols, rows);
      return bridgeTerminal(process);
    }
    return new Response("not found", { status: 404 });
  }

  // ---- internals ----

  private serialize<T>(work: () => Promise<T>): Promise<T> {
    const next = this.queue.then(work, work);
    this.queue = next.catch(() => undefined);
    return next;
  }

  private async bind(context: ChildContext): Promise<void> {
    const stored = await this.ctx.storage.get<SandboxIdentity>("identity");
    if (!stored) {
      await this.ctx.storage.put("identity", { org: context.org, room: context.room, name: context.name });
      return;
    }
    if (stored.org !== context.org || stored.room.kind !== context.room.kind || stored.room.id !== context.room.id || stored.name !== context.name) {
      throw new EdgeError("forbidden", "this sandbox belongs to another room");
    }
  }

  private async identity(): Promise<SandboxIdentity> {
    const identity = await this.ctx.storage.get<SandboxIdentity>("identity");
    if (!identity) throw new EdgeError("not_found", "no such sandbox");
    return identity;
  }

  private async workspace(): Promise<Workspace> {
    if (this.workspaceInstance) return this.workspaceInstance;
    const identity = await this.identity();
    if (workspaceFactory) {
      this.workspaceInstance = workspaceFactory(this, this.env, identity);
      return this.workspaceInstance;
    }
    const container = this.ctx.container;
    if (!container) throw new EdgeError("unavailable", "this Worker has no sandbox container configured", true);
    const exports = this.ctx.exports as unknown as {
      DirectoryBackupGateway: ConstructorParameters<typeof DirectoryBackup>[1];
      SandboxEgress: Fetcher;
    };
    const backups = new DirectoryBackup(container, exports.DirectoryBackupGateway, {
      binding: "RUNS",
      prefix: backupPrefix(identity),
    });
    this.workspaceInstance = new ContainerWorkspace(container, {
      image: container.images["sandbox"] ?? Object.values(container.images)[0]!,
      instance: (this.env.SANDBOX_INSTANCE || "standard-1") as ContainerStartupOptions["instance"],
      labels: { org: identity.org, room: `${identity.room.kind}/${identity.room.id}`, sandbox: identity.name },
      egress: exports.SandboxEgress,
      backups,
      idleTimeoutMs: SANDBOX_IDLE_MS + 5 * 60 * 1000,
    });
    return this.workspaceInstance;
  }

  private async saveBackup(record: DirectoryBackupRecord): Promise<void> {
    await this.ctx.storage.put("backup", record);
  }

  // started starts the container if it is asleep, restoring the last backup
  // of its files, and reports whether it had to.
  private async started(events: ChildEvent[]): Promise<{ workspace: Workspace; recreated: boolean }> {
    const workspace = await this.workspace();
    if (workspace.running()) return { workspace, recreated: false };
    const begun = Date.now();
    try {
      await workspace.start();
    } catch (error) {
      throw new EdgeError("unavailable", `the sandbox container did not start: ${String(error)}`, true);
    }
    const backup = await this.ctx.storage.get<DirectoryBackupRecord>("backup");
    let restored = false;
    if (backup) {
      try {
        await workspace.restore(backup);
        restored = true;
      } catch (error) {
        console.error(JSON.stringify({ msg: "sandbox restore failed", error: String(error) }));
      }
    }
    events.push({
      type: "sandbox.started",
      summary: restored ? "Sandbox started, files restored" : "Sandbox started",
      data: { restored },
      durationMs: Date.now() - begun,
    });
    return { workspace, recreated: true };
  }

  private async run(context: ChildContext, op: SandboxOp, body: unknown, query: Record<string, string>, events: ChildEvent[]): Promise<unknown> {
    switch (op) {
      case "exec/code":
        return this.execCode(ExecCodeRequest.parse(body), events);
      case "exec/command":
        return this.execCommand(ExecCommandRequest.parse(body), events);
      case "files/list":
        return this.listFiles(query["path"] ?? "", events);
      case "files/read":
        return this.readFiles(ReadFilesRequest.parse(body).paths, events);
      case "files/write":
        return this.writeFiles(WriteFilesRequest.parse(body), events);
      case "files/remove":
        return this.removeFiles(RemoveFilesRequest.parse(body).paths, events);
      case "reset":
        return this.reset(events);
      case "artifact":
        return this.artifact(context, ArtifactRequest.parse(body), events);
      case "preview":
        return this.preview(context, PreviewRequest.parse(body), events);
      case "terminal":
        return this.terminal(context, TerminalRequest.parse(body), events);
      case "snapshot":
        return this.snapshot(events);
    }
  }

  private async execCode(request: ExecCodeRequest, events: ChildEvent[]): Promise<ExecResult> {
    if (new TextEncoder().encode(request.code).length > MAX_CODE_BYTES) throw new EdgeError("too_large", "code is over 1 MiB");
    const { workspace, recreated } = await this.started(events);
    const extension = { python: "py", javascript: "mjs", typescript: "ts" }[request.language];
    const interpreter = { python: "python3", javascript: "node", typescript: "tsx" }[request.language];
    const file = `${WORKSPACE_DIR}/.oryxa/run-${crypto.randomUUID()}.${extension}`;
    await workspace.writeFile(file, new TextEncoder().encode(request.code));
    try {
      const result = await this.execute(workspace, [interpreter, file], undefined, request.timeoutMs, recreated);
      events.push({
        type: "sandbox.exec",
        summary: `Ran ${request.language} (${result.timedOut ? "timed out" : `exit ${result.exitCode}`}, ${result.durationMs} ms)`,
        data: { language: request.language, exitCode: result.exitCode, timedOut: Boolean(result.timedOut), code: request.code.slice(0, 2_000) },
        durationMs: result.durationMs,
      });
      this.fileEvents(result.changedFiles ?? [], events);
      return result;
    } finally {
      await workspace.remove(file).catch(() => undefined);
    }
  }

  private async execCommand(request: ExecCommandRequest, events: ChildEvent[]): Promise<ExecResult> {
    if (new TextEncoder().encode(request.command).length > MAX_COMMAND_BYTES) throw new EdgeError("too_large", "command is over 64 KiB");
    const { workspace, recreated } = await this.started(events);
    const result = await this.execute(workspace, ["bash", "-lc", request.command], request.cwd ? resolvePath(request.cwd) : undefined, request.timeoutMs, recreated);
    events.push({
      type: "sandbox.exec",
      summary: `Ran a command (${result.timedOut ? "timed out" : `exit ${result.exitCode}`}, ${result.durationMs} ms)`,
      data: { command: request.command.slice(0, 2_000), exitCode: result.exitCode, timedOut: Boolean(result.timedOut) },
      durationMs: result.durationMs,
    });
    this.fileEvents(result.changedFiles ?? [], events);
    return result;
  }

  // execute runs argv after marking the time, then lists the files it
  // changed and returns small images among them as rich results.
  private async execute(workspace: Workspace, argv: string[], cwd: string | undefined, timeoutMs: number | undefined, recreated: boolean): Promise<ExecResult> {
    const limit = Math.min(Math.max(timeoutMs ?? DEFAULT_EXEC_MS, 1_000), MAX_EXEC_MS);
    const started = Date.now();
    const outcome = await workspace.exec(["sh", "-c", 'mkdir -p /tmp/oryxa && touch /tmp/oryxa/mark && exec "$@"', "oryxa", ...argv], {
      cwd,
      timeoutMs: limit,
      stdoutLimit: STDOUT_LIMIT,
      stderrLimit: STDERR_LIMIT,
    });
    const durationMs = Date.now() - started;
    const changed = await workspace
      .exec(["sh", "-c", `find ${WORKSPACE_DIR} -newer /tmp/oryxa/mark -type f -not -path '${WORKSPACE_DIR}/.oryxa/*' 2>/dev/null | head -200`], {
        timeoutMs: 15_000,
        stdoutLimit: 64 << 10,
        stderrLimit: 4 << 10,
      })
      .then((listing) => listing.stdout.split("\n").filter(Boolean))
      .catch(() => [] as string[]);
    const results: ExecOutput[] = [];
    let imageBytes = 0;
    for (const path of changed) {
      const mimeType = mimeFor(path);
      if (!mimeType.startsWith("image/")) continue;
      try {
        const file = await workspace.readFile(path, MAX_RESULT_IMAGE_BYTES);
        if (file.truncated || imageBytes + file.bytes.length > MAX_RESULT_IMAGES_BYTES) continue;
        imageBytes += file.bytes.length;
        results.push({ type: "image", mimeType, data: base64Encode(file.bytes), name: path.slice(path.lastIndexOf("/") + 1) });
      } catch {
        // A file that vanished since it was listed isn't a result.
      }
    }
    return {
      stdout: outcome.stdout,
      stderr: outcome.stderr,
      exitCode: outcome.exitCode,
      results,
      durationMs,
      ...(outcome.truncated ? { outputTruncated: true } : {}),
      ...(outcome.timedOut ? { timedOut: true } : {}),
      ...(recreated ? { sessionRecreated: true } : {}),
      ...(changed.length ? { changedFiles: changed } : {}),
    };
  }

  private fileEvents(paths: string[], events: ChildEvent[]): void {
    if (paths.length === 0) return;
    events.push({
      type: "sandbox.file.changed",
      summary: paths.length === 1 ? `Changed ${paths[0]}` : `Changed ${paths.length} files`,
      data: { paths: paths.slice(0, 50) },
    });
  }

  private async listFiles(path: string, events: ChildEvent[]): Promise<ListFilesResponse> {
    const { workspace } = await this.started(events);
    const directory = resolvePath(path || WORKSPACE_DIR);
    const entries = await fileCall(() => workspace.readDirectory(directory));
    const truncated = entries.length > MAX_LIST_ENTRIES;
    const base = directory.replace(/\/$/, "");
    return {
      path: directory,
      entries: entries.slice(0, MAX_LIST_ENTRIES).map((entry) => ({ ...entry, path: `${base}/${entry.name}` })),
      ...(truncated ? { truncated } : {}),
    };
  }

  private async readFiles(paths: string[], events: ChildEvent[]): Promise<{ files: FileContent[] }> {
    if (paths.length > MAX_PATHS) throw new EdgeError("invalid_request", `at most ${MAX_PATHS} paths`);
    const { workspace } = await this.started(events);
    let budget = READ_BUDGET;
    const files: FileContent[] = [];
    for (const raw of paths) {
      const path = resolvePath(raw);
      try {
        const file = await workspace.readFile(path, Math.max(budget, 0));
        budget -= file.bytes.length;
        const text = utf8(file.bytes);
        files.push({
          path,
          ...(text !== null ? { encoding: "utf8" as const, content: text } : { encoding: "base64" as const, content: base64Encode(file.bytes) }),
          mimeType: mimeFor(path, text !== null),
          size: file.size,
          ...(file.truncated ? { truncated: true } : {}),
        });
      } catch (error) {
        if (!(error instanceof FileError)) throw error;
        files.push({ path, size: 0, error: `${error.errno}: ${error.message}` });
      }
    }
    return { files };
  }

  private async writeFiles(request: WriteFilesRequest, events: ChildEvent[]): Promise<{ written: { path: string; size: number }[] }> {
    if (request.files.length > MAX_WRITE_FILES) throw new EdgeError("invalid_request", `at most ${MAX_WRITE_FILES} files`);
    const decoded = request.files.map((file) => {
      const bytes = file.encoding === "base64" ? base64Decode(file.content) : new TextEncoder().encode(file.content);
      if (bytes.length > MAX_WRITE_BYTES) throw new EdgeError("too_large", `${file.path} is over 5 MiB`);
      return { path: resolvePath(file.path), bytes };
    });
    const { workspace } = await this.started(events);
    const written: { path: string; size: number }[] = [];
    for (const file of decoded) {
      await fileCall(() => workspace.writeFile(file.path, file.bytes));
      written.push({ path: file.path, size: file.bytes.length });
    }
    this.fileEvents(written.map((file) => file.path), events);
    return { written };
  }

  private async removeFiles(paths: string[], events: ChildEvent[]): Promise<{ removed: string[] }> {
    if (paths.length > MAX_PATHS) throw new EdgeError("invalid_request", `at most ${MAX_PATHS} paths`);
    const resolved = paths.map(resolvePath);
    if (resolved.some((path) => path === "/" || path === WORKSPACE_DIR)) {
      throw new EdgeError("invalid_request", "refusing to remove the root or the whole workspace; use reset");
    }
    const { workspace } = await this.started(events);
    for (const path of resolved) await fileCall(() => workspace.remove(path));
    events.push({ type: "sandbox.file.changed", summary: `Removed ${resolved.length === 1 ? resolved[0] : `${resolved.length} files`}`, data: { removed: resolved } });
    return { removed: resolved };
  }

  private async reset(events: ChildEvent[]): Promise<{ reset: boolean }> {
    const workspace = await this.workspace();
    await workspace.stop();
    await this.ctx.storage.delete("backup");
    const identity = await this.identity();
    const prefix = backupPrefix(identity);
    const listed = await this.env.RUNS.list({ prefix });
    if (listed.objects.length) await this.env.RUNS.delete(listed.objects.map((object) => object.key));
    events.push({ type: "sandbox.reset", summary: "Sandbox reset: processes and files cleared" });
    return { reset: true };
  }

  private async artifact(context: ChildContext, request: ArtifactRequest, events: ChildEvent[]) {
    const path = resolvePath(request.path);
    const name = request.name ?? path.slice(path.lastIndexOf("/") + 1);
    if (!ARTIFACT_NAME.test(name)) throw new EdgeError("invalid_request", "artifact name may use letters, digits, '.', '_' and '-'");
    const { workspace } = await this.started(events);
    const file = await fileCall(() => workspace.openFile(path));
    if (file.size > MAX_ARTIFACT_BYTES) {
      await file.body.cancel();
      throw new EdgeError("too_large", "artifacts are capped at 64 MiB");
    }
    const mimeType = request.mimeType ?? mimeFor(path);
    const key = `${runPrefix(context.org, context.room, context.run ?? ROOM_RUN)}artifacts/${name}`;
    // Copy and hash in one pass: workerd can't pipe one tee'd stream into
    // two transform streams.
    const digest = new crypto.DigestStream("SHA-256");
    const fixed = new FixedLengthStream(file.size);
    const stored = this.env.RUNS.put(key, fixed.readable, {
      httpMetadata: { contentType: mimeType },
      customMetadata: { org: context.org, member: context.member, run: context.run ?? "" },
    });
    const toStore = fixed.writable.getWriter();
    const toHash = digest.getWriter();
    const reader = file.body.getReader();
    for (;;) {
      const { done, value } = await reader.read();
      if (done) break;
      await Promise.all([toStore.write(value), toHash.write(value)]);
    }
    await Promise.all([toStore.close(), toHash.close()]);
    await stored;
    const sha256 = [...new Uint8Array(await digest.digest)].map((byte) => byte.toString(16).padStart(2, "0")).join("");
    events.push({ type: "sandbox.artifact", summary: `Saved ${name} as an artifact`, data: { key, size: file.size, mimeType } });
    return { key, name, mimeType, size: file.size, sha256 };
  }

  private async preview(context: ChildContext, request: PreviewRequest, events: ChildEvent[]): Promise<SignedURL> {
    await this.started(events);
    const url = await this.signedURL(context, { t: "preview", port: request.port }, "https");
    events.push({ type: "sandbox.preview", summary: `Preview opened on port ${request.port}`, data: { port: request.port } });
    return url;
  }

  private async terminal(context: ChildContext, request: TerminalRequest, events: ChildEvent[]): Promise<SignedURL> {
    await this.started(events);
    const url = await this.signedURL(context, { t: "terminal" }, "wss", request);
    events.push({ type: "sandbox.terminal", summary: "Terminal opened" });
    return url;
  }

  private async signedURL(
    context: ChildContext,
    what: { t: SignedTarget["t"]; port?: number },
    scheme: "https" | "wss",
    size?: { cols?: number; rows?: number },
  ): Promise<SignedURL> {
    const exp = Math.floor(Date.now() / 1000) + SIGNED_URL_SECONDS;
    const token = await signTarget(this.env.EDGE_URL_SIGNING_KEY, {
      ...what,
      org: context.org,
      room: context.room,
      name: context.name,
      member: context.member,
      exp,
    });
    const base = new URL(this.env.EDGE_PUBLIC_URL || "https://edge.oryxa.in");
    base.protocol = `${scheme}:`;
    const path = what.t === "preview" ? `/p/${token}/` : `/t/${token}`;
    const query = size && (size.cols || size.rows) ? `?cols=${size.cols ?? 120}&rows=${size.rows ?? 32}` : "";
    return { url: `${base.origin}${path}${query}`, expiresAt: new Date(exp * 1000).toISOString() };
  }

  private async snapshot(events: ChildEvent[]) {
    const { workspace } = await this.started(events);
    const record = await workspace.backup(WORKSPACE_DIR);
    await this.saveBackup(record);
    const createdAt = new Date().toISOString();
    events.push({ type: "sandbox.snapshot", summary: "Workspace snapshot saved", data: { id: record.id, size: record.size } });
    return { id: record.id, size: record.size, createdAt };
  }
}

// BACKUPS_PREFIX is where sandbox workspace backups live in R2, apart from
// the runs: a backup is replaced at every idle stop and only needed to
// restore one, so a lifecycle rule on this fixed prefix expires old ones
// (A17; R2 rules match fixed prefixes, not patterns).
export const BACKUPS_PREFIX = "backups/sandbox/";

// backupPrefix is one sandbox's: backups/sandbox/{org}/{kind}/{room}/{name}/.
export function backupPrefix(identity: SandboxIdentity): string {
  return `${BACKUPS_PREFIX}${identity.org}/${identity.room.kind}/${identity.room.id}/${identity.name}/`;
}

// resolvePath makes a sandbox path absolute against /workspace and refuses
// what can't be a path.
export function resolvePath(raw: string): string {
  const path = raw.trim();
  if (path === "") throw new EdgeError("invalid_request", "path is empty");
  if (path.includes("\0") || new TextEncoder().encode(path).length > MAX_PATH_BYTES) throw new EdgeError("invalid_request", "path is not valid");
  const absolute = path.startsWith("/") ? path : `${WORKSPACE_DIR}/${path}`;
  const parts: string[] = [];
  for (const part of absolute.split("/")) {
    if (part === "" || part === ".") continue;
    if (part === "..") parts.pop();
    else parts.push(part);
  }
  return `/${parts.join("/")}`;
}

async function fileCall<T>(operation: () => Promise<T>): Promise<T> {
  try {
    return await operation();
  } catch (error) {
    if (error instanceof FileError) {
      const code = error.errno === "ENOENT" ? "not_found" : "invalid_request";
      throw new EdgeError(code, `${error.errno}: ${error.message}`);
    }
    throw error;
  }
}

function utf8(bytes: Uint8Array): string | null {
  if (bytes.includes(0)) return null;
  try {
    return new TextDecoder("utf-8", { fatal: true, ignoreBOM: false }).decode(bytes);
  } catch {
    return null;
  }
}

const MIME: Record<string, string> = {
  py: "text/x-python", js: "text/javascript", mjs: "text/javascript", ts: "text/typescript", json: "application/json",
  md: "text/markdown", txt: "text/plain", csv: "text/csv", html: "text/html", css: "text/css", xml: "application/xml",
  yaml: "application/yaml", yml: "application/yaml", sh: "text/x-shellscript", svg: "image/svg+xml", png: "image/png",
  jpg: "image/jpeg", jpeg: "image/jpeg", gif: "image/gif", webp: "image/webp", pdf: "application/pdf",
  zip: "application/zip", gz: "application/gzip", tar: "application/x-tar",
  xlsx: "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet",
  docx: "application/vnd.openxmlformats-officedocument.wordprocessingml.document",
  pptx: "application/vnd.openxmlformats-officedocument.presentationml.presentation",
};

export function mimeFor(path: string, isText = false): string {
  const extension = path.slice(path.lastIndexOf(".") + 1).toLowerCase();
  return MIME[extension] ?? (isText ? "text/plain" : "application/octet-stream");
}

// bridgeTerminal joins a member's WebSocket to a PTY shell in the sandbox.
function bridgeTerminal(process: ExecProcess): Response {
  const pair = new WebSocketPair();
  const [client, server] = [pair[0], pair[1]];
  server.accept();
  const writer = process.stdin?.getWriter();
  server.addEventListener("message", (message) => {
    const data = typeof message.data === "string" ? new TextEncoder().encode(message.data) : new Uint8Array(message.data as ArrayBuffer);
    if (data.length > 0 && data[0] === 0x7b) {
      // {"resize":[cols,rows]} from the client resizes the PTY.
      try {
        const control = JSON.parse(new TextDecoder().decode(data)) as { resize?: [number, number] };
        if (control.resize) {
          process.resize(control.resize[0], control.resize[1]);
          return;
        }
      } catch {
        // Not a control message: it's input.
      }
    }
    void writer?.write(data);
  });
  server.addEventListener("close", () => {
    process.kill();
    void writer?.close().catch(() => undefined);
  });
  void (async () => {
    const reader = process.stdout?.getReader();
    if (!reader) return;
    try {
      for (;;) {
        const { done, value } = await reader.read();
        if (done) break;
        server.send(value as Uint8Array);
      }
    } finally {
      try {
        server.close(1000, "shell exited");
      } catch {
        // already closed
      }
    }
  })();
  return new Response(null, { status: 101, webSocket: client });
}
