// Children (A3): the sandboxes and browsers a room runs. The room is the
// source of truth for which exist (contract §1): it creates them on first
// use, enforces who may reach each one and how many may run, forwards each
// operation to the child's DO, writes what the child reports to the log,
// and stops children left idle.
import {
  ArtifactRequest,
  ClickRequest,
  type Child,
  ExecCodeRequest,
  ExecCommandRequest,
  FillRequest,
  HandoffRequest,
  LiveRequest,
  NavigateRequest,
  PreviewRequest,
  ReadFilesRequest,
  RemoveFilesRequest,
  ResumeRequest,
  type Scope,
  TerminalRequest,
  WriteFilesRequest,
  browserDOName,
  sandboxDOName,
  validId,
  validSandboxName,
} from "../contracts";
import type { Env } from "../env";
import { EdgeError, discardBody, json, readJson } from "../http";
import { BROWSER_IDLE_MS, type BrowserOp } from "../browser/browser";
import { SANDBOX_IDLE_MS, type SandboxOp } from "../sandbox/sandbox";
import type { ChildContext, ChildReply } from "./child";
import { type Caller, type RoomDO, requireScope } from "./room";
import type { z } from "zod";

export const MAX_RUNNING_SANDBOXES = 4;
export const MAX_RUNNING_BROWSERS = 3;

type Kind = "sandbox" | "browser";

interface Operation<O extends string> {
  op: O;
  method: "GET" | "POST";
  scope: Scope;
  // web lets a member use it through web-next (watching, not driving).
  web: boolean;
  schema?: z.ZodType;
  // agentOnly is false for what only a member may do.
  agent?: boolean;
}

const SANDBOX_OPS: Record<string, Operation<SandboxOp>> = {
  "POST exec/code": { op: "exec/code", method: "POST", scope: "sandbox:exec", web: false, schema: ExecCodeRequest },
  "POST exec/command": { op: "exec/command", method: "POST", scope: "sandbox:exec", web: false, schema: ExecCommandRequest },
  "GET files": { op: "files/list", method: "GET", scope: "sandbox:files", web: true },
  "POST files/read": { op: "files/read", method: "POST", scope: "sandbox:files", web: true, schema: ReadFilesRequest },
  "POST files/write": { op: "files/write", method: "POST", scope: "sandbox:files", web: false, schema: WriteFilesRequest },
  "POST files/remove": { op: "files/remove", method: "POST", scope: "sandbox:files", web: false, schema: RemoveFilesRequest },
  "POST reset": { op: "reset", method: "POST", scope: "sandbox:exec", web: false },
  "POST artifact": { op: "artifact", method: "POST", scope: "sandbox:files", web: false, schema: ArtifactRequest },
  "POST preview": { op: "preview", method: "POST", scope: "sandbox:exec", web: true, schema: PreviewRequest },
  "POST terminal": { op: "terminal", method: "POST", scope: "sandbox:exec", web: true, schema: TerminalRequest },
  "POST snapshot": { op: "snapshot", method: "POST", scope: "sandbox:exec", web: false },
};

const BROWSER_OPS: Record<string, Operation<BrowserOp>> = {
  "POST navigate": { op: "navigate", method: "POST", scope: "browser:drive", web: false, schema: NavigateRequest },
  "POST snapshot": { op: "snapshot", method: "POST", scope: "browser:drive", web: false },
  "POST click": { op: "click", method: "POST", scope: "browser:drive", web: false, schema: ClickRequest },
  "POST fill": { op: "fill", method: "POST", scope: "browser:drive", web: false, schema: FillRequest },
  "POST screenshot": { op: "screenshot", method: "POST", scope: "browser:drive", web: false },
  "POST reset": { op: "reset", method: "POST", scope: "browser:drive", web: false },
  "POST live": { op: "live", method: "POST", scope: "browser:drive", web: true, schema: LiveRequest },
  "POST handoff": { op: "handoff", method: "POST", scope: "browser:drive", web: false, schema: HandoffRequest },
  "GET handoff": { op: "handoff/state", method: "GET", scope: "browser:drive", web: true },
  "POST resume": { op: "resume", method: "POST", scope: "browser:drive", web: true, agent: false, schema: ResumeRequest },
};

type ChildRow = {
  kind: Kind;
  name: string;
  status: string;
  private: number;
  member: string;
  created_at: string;
  last_used_at: number;
};

export class Children {
  private readonly sql: SqlStorage;

  constructor(
    readonly room: RoomDO,
    readonly ctx: DurableObjectState,
    readonly env: Env,
  ) {
    this.sql = ctx.storage.sql;
    this.sql.exec(`
      CREATE TABLE IF NOT EXISTS children (
        kind TEXT NOT NULL,
        name TEXT NOT NULL,
        status TEXT NOT NULL,
        private INTEGER NOT NULL,
        member TEXT NOT NULL,
        created_at TEXT NOT NULL,
        last_used_at INTEGER NOT NULL,
        PRIMARY KEY (kind, name)
      );
    `);
  }

  async route(request: Request, url: URL, rest: string, caller: Caller): Promise<Response> {
    if (rest === "/children" && request.method === "GET") return json({ children: this.list(caller) });

    const match = /^\/(sandbox|browser)\/([^/]+)(?:\/(.*))?$/.exec(rest);
    if (!match) throw new EdgeError("not_found", "no such route");
    const kind = match[1] as Kind;
    const name = decodeURIComponent(match[2]!);
    const tail = match[3] ?? "";
    if (kind === "sandbox" ? !validSandboxName(name) : !validId(name)) throw new EdgeError("not_found", `no such ${kind}`);

    if (request.method === "DELETE" && tail === "") {
      requireScope(caller, kind === "sandbox" ? "sandbox:exec" : "browser:drive");
      const existing = this.get(kind, name);
      if (!existing) throw new EdgeError("not_found", `no such ${kind}`);
      this.authorize(caller, kind, name, existing);
      await this.stopChild(existing, "stopped by request", caller);
      return json({ stopped: true });
    }

    const table: Record<string, Operation<string>> = kind === "sandbox" ? SANDBOX_OPS : BROWSER_OPS;
    const operation = table[`${request.method} ${tail}`];
    if (!operation) throw new EdgeError("not_found", "no such route");
    if (caller.kind === "web" && !operation.web) throw new EdgeError("forbidden", "members can't do that here");
    if (caller.kind === "agent") {
      if (operation.agent === false) throw new EdgeError("forbidden", "only a member can do that");
      requireScope(caller, operation.scope);
    }

    const existing = this.get(kind, name);
    this.authorize(caller, kind, name, existing);
    if (!existing) {
      if (caller.kind === "web") throw new EdgeError("not_found", `no such ${kind}`);
      this.checkLimit(kind);
    } else if (existing.status !== "running") {
      this.checkLimit(kind);
    }

    let body: unknown;
    if (operation.schema) body = await readJson(request, operation.schema);
    else await discardBody(request);
    const query = Object.fromEntries(url.searchParams);
    const context = this.context(caller, kind, name, existing);
    const reply = await this.call(kind, name, context, operation.op, body, query);

    this.touch(kind, name, context, reply.status < 300 && !(kind === "sandbox" && operation.op === "reset") && !(kind === "browser" && operation.op === "reset"));
    if (operation.op === "reset") this.setStatus(kind, name, "stopped");
    this.log(context, reply);
    await this.scheduleSweep();
    return json(reply.body, reply.status);
  }

  // ---- access ----

  // authorize refuses a caller who may not reach this child: a private
  // sandbox is its member's alone (and only through a private token); a
  // run's sandbox is that run's; a browser is the member's who started it.
  private authorize(caller: Caller, kind: Kind, name: string, existing: ChildRow | undefined): void {
    const member = caller.kind === "agent" ? caller.claims.member : caller.identity.member;
    if (kind === "sandbox") {
      if (name.startsWith("private-")) {
        const owner = name.slice("private-".length);
        if (member !== owner) throw new EdgeError("forbidden", "this private sandbox belongs to another member");
        if (caller.kind === "agent" && !caller.claims.private) throw new EdgeError("forbidden", "a private sandbox needs a private token");
        return;
      }
      if (name !== "shared") {
        if (caller.kind === "agent" && caller.claims.run !== name) throw new EdgeError("forbidden", "this sandbox belongs to another run");
        if (caller.kind === "web" && existing && existing.member !== member) throw new EdgeError("forbidden", "this sandbox belongs to another member's run");
        return;
      }
      if (caller.kind === "agent" && caller.claims.private) throw new EdgeError("forbidden", "a private token reaches only its member's private sandbox");
      return;
    }
    // Browsers hold their member's cookies and logins.
    if (caller.kind === "agent") {
      if (existing && existing.member !== member) throw new EdgeError("forbidden", "this browser belongs to another member");
      return;
    }
    if (existing && existing.private && existing.member !== member) throw new EdgeError("forbidden", "this browser is private to another member");
  }

  private checkLimit(kind: Kind): void {
    const running = this.sql.exec<{ count: number }>("SELECT COUNT(*) AS count FROM children WHERE kind = ? AND status = 'running'", kind).one().count;
    const limit = kind === "sandbox" ? MAX_RUNNING_SANDBOXES : MAX_RUNNING_BROWSERS;
    if (running >= limit) throw new EdgeError("limit", `this room already runs ${limit} ${kind === "sandbox" ? "sandboxes" : "browsers"}; stop one first`);
  }

  private context(caller: Caller, kind: Kind, name: string, existing: ChildRow | undefined): ChildContext {
    const identity = this.room.identity()!;
    if (caller.kind === "agent") {
      const isPrivate = kind === "sandbox" ? name.startsWith("private-") : caller.claims.private;
      return {
        org: identity.org,
        room: identity.room,
        name,
        run: caller.claims.run || null,
        member: caller.claims.member,
        private: isPrivate || Boolean(existing?.private),
        actor: { type: "agent", id: caller.claims.agent, onBehalfOf: caller.claims.member },
      };
    }
    return {
      org: identity.org,
      room: identity.room,
      name,
      run: null,
      member: caller.identity.member,
      private: Boolean(existing?.private),
      actor: { type: "member", id: caller.identity.member, onBehalfOf: caller.identity.member },
    };
  }

  // ---- calls ----

  private async call(kind: Kind, name: string, context: ChildContext, op: string, body: unknown, query: Record<string, string>): Promise<ChildReply> {
    const identity = this.room.identity()!;
    try {
      if (kind === "sandbox") {
        const stub = this.env.SANDBOX.getByName(sandboxDOName(identity.org, identity.room, name));
        return await stub.op(context, op as SandboxOp, body, query);
      }
      const stub = this.env.BROWSER_SESSION.getByName(browserDOName(identity.org, identity.room, name));
      return await stub.op(context, op as BrowserOp, body);
    } catch (error) {
      // The child's DO itself failed (evicted or restarted mid-call).
      console.error(JSON.stringify({ msg: "child call failed", kind, error: String(error) }));
      throw new EdgeError("unavailable", `the ${kind} did not answer; try again`, true);
    }
  }

  private log(context: ChildContext, reply: ChildReply): void {
    for (const event of reply.events) {
      this.room.append({
        actor: context.actor,
        run: context.run,
        type: event.type,
        summary: event.summary,
        data: { ...event.data, child: context.name },
        durationMs: event.durationMs,
        private: context.private,
      });
    }
  }

  // ---- bookkeeping ----

  private get(kind: Kind, name: string): ChildRow | undefined {
    return this.sql.exec<ChildRow>("SELECT * FROM children WHERE kind = ? AND name = ?", kind, name).toArray()[0];
  }

  private touch(kind: Kind, name: string, context: ChildContext, running: boolean): void {
    const now = Date.now();
    this.sql.exec(
      `INSERT INTO children (kind, name, status, private, member, created_at, last_used_at)
       VALUES (?, ?, ?, ?, ?, ?, ?)
       ON CONFLICT (kind, name) DO UPDATE SET last_used_at = excluded.last_used_at,
         status = CASE WHEN ? THEN 'running' ELSE children.status END`,
      kind,
      name,
      running ? "running" : "stopped",
      context.private ? 1 : 0,
      context.member,
      new Date(now).toISOString(),
      now,
      running ? 1 : 0,
    );
  }

  private setStatus(kind: Kind, name: string, status: string): void {
    this.sql.exec("UPDATE children SET status = ? WHERE kind = ? AND name = ?", status, kind, name);
  }

  list(caller: Caller): Child[] {
    const member = caller.kind === "agent" ? caller.claims.member : caller.identity.member;
    return this.sql
      .exec<ChildRow>("SELECT * FROM children ORDER BY created_at, name")
      .toArray()
      .filter((row) => !row.private || row.member === member)
      .map((row) => ({
        kind: row.kind,
        name: row.name,
        status: row.status,
        ...(row.private ? { private: true, member: row.member } : {}),
        createdAt: row.created_at,
        lastUsedAt: new Date(row.last_used_at).toISOString(),
      }));
  }

  private async stopChild(row: ChildRow, reason: string, caller?: Caller): Promise<void> {
    const identity = this.room.identity()!;
    let reply: ChildReply;
    try {
      reply =
        row.kind === "sandbox"
          ? await this.env.SANDBOX.getByName(sandboxDOName(identity.org, identity.room, row.name)).stop(reason)
          : await this.env.BROWSER_SESSION.getByName(browserDOName(identity.org, identity.room, row.name)).stop(reason);
    } catch (error) {
      console.error(JSON.stringify({ msg: "stopping child failed", kind: row.kind, error: String(error) }));
      return;
    }
    this.setStatus(row.kind, row.name, "stopped");
    const actor =
      caller?.kind === "agent"
        ? { type: "agent" as const, id: caller.claims.agent, onBehalfOf: caller.claims.member }
        : caller?.kind === "web"
          ? { type: "member" as const, id: caller.identity.member }
          : { type: "system" as const, id: "edge" };
    this.log(
      { org: identity.org, room: identity.room, name: row.name, run: caller?.kind === "agent" ? caller.claims.run || null : null, member: row.member, private: Boolean(row.private), actor },
      reply,
    );
  }

  // sweep stops children idle past their limit.
  async sweep(): Promise<void> {
    const now = Date.now();
    for (const row of this.sql.exec<ChildRow>("SELECT * FROM children WHERE status = 'running'").toArray()) {
      if (now - row.last_used_at >= idleLimit(row.kind)) await this.stopChild(row, "idle");
    }
  }

  nextSweep(): number | null {
    let next: number | null = null;
    for (const row of this.sql.exec<ChildRow>("SELECT * FROM children WHERE status = 'running'").toArray()) {
      const due = row.last_used_at + idleLimit(row.kind);
      next = next === null ? due : Math.min(next, due);
    }
    return next;
  }

  private async scheduleSweep(): Promise<void> {
    const due = this.nextSweep();
    if (due === null) return;
    const current = await this.ctx.storage.getAlarm();
    if (current === null || current > due) await this.ctx.storage.setAlarm(due);
  }
}

function idleLimit(kind: Kind): number {
  return kind === "sandbox" ? SANDBOX_IDLE_MS : BROWSER_IDLE_MS;
}
