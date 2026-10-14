// RoomDO: one per room, named org/{org}/room/{kind}/{id} (contract §1). It is
// the room's control plane: its identity and members, the replay check for
// every token used on it, the room log (§8) kept hot in SQLite and flushed to
// R2 (§7), live subscribers, and (children.ts) the sandboxes and browsers it
// runs.
//
// It's reached only through this Worker: router.ts for Go (an edge token,
// verified there, arrives as CLAIMS_HEADER) and web.ts for web-next (a
// session web-next verified, as WEB_HEADER). Nothing outside the Worker can
// set either header.
import { DurableObject } from "cloudflare:workers";
import {
  AppendEventRequest,
  type Event,
  type Room,
  type Scope,
  type TokenClaims,
  ROOM_RUN,
  runPrefix,
} from "../contracts";
import type { Env } from "../env";
import { EdgeError, discardBody, json, readJson, toResponse } from "../http";
import { Children } from "./children";

export const CLAIMS_HEADER = "x-oryxa-edge-claims";
export const WEB_HEADER = "x-oryxa-edge-web";
export const ROOM_HEADER = "x-oryxa-edge-room";
export const REST_HEADER = "x-oryxa-edge-rest";

// WebIdentity is who web-next says is asking, after checking their session.
export interface WebIdentity {
  org: string;
  member: string;
}

// Caller is whoever a request comes from, already authenticated.
export type Caller = { kind: "agent"; claims: TokenClaims } | { kind: "web"; identity: WebIdentity };

// What an event is written with, before the room numbers and stamps it.
export interface NewEvent {
  actor: Event["actor"];
  run: string | null;
  type: string;
  summary: string;
  data?: Record<string, unknown>;
  durationMs?: number;
  cost?: Event["cost"];
  private?: boolean;
}

const LOG_PAGE = 500;
// HOT_EVENTS is how many flushed events the room keeps in SQLite for fast
// reads; R2 holds all of them.
const HOT_EVENTS = 10_000;
const FLUSH_DELAY_MS = 2_000;
const FLUSH_BATCH = 1_000;
// SUMMARY_LIMIT and DATA_LIMIT keep one event small (§8: size-capped).
const SUMMARY_LIMIT = 512;
const DATA_LIMIT = 16 << 10;

export class RoomDO extends DurableObject<Env> {
  private readonly sql: SqlStorage;
  readonly children: Children;

  constructor(ctx: DurableObjectState, env: Env) {
    super(ctx, env);
    this.sql = ctx.storage.sql;
    ctx.blockConcurrencyWhile(async () => this.migrate());
    this.children = new Children(this, ctx, env);
  }

  private migrate(): void {
    this.sql.exec(`
      CREATE TABLE IF NOT EXISTS meta (key TEXT PRIMARY KEY, value TEXT NOT NULL);
      CREATE TABLE IF NOT EXISTS members (member TEXT PRIMARY KEY, added_at TEXT NOT NULL);
      CREATE TABLE IF NOT EXISTS seen_tokens (jti TEXT PRIMARY KEY, exp INTEGER NOT NULL);
      CREATE TABLE IF NOT EXISTS events (
        seq INTEGER PRIMARY KEY AUTOINCREMENT,
        run TEXT,
        private INTEGER NOT NULL,
        on_behalf_of TEXT,
        body TEXT NOT NULL,
        flushed INTEGER NOT NULL DEFAULT 0
      );
      CREATE INDEX IF NOT EXISTS events_unflushed ON events (flushed, seq);
      CREATE TABLE IF NOT EXISTS runs (run TEXT PRIMARY KEY, manifest TEXT NOT NULL);
    `);
  }

  // ---- identity ----

  private meta(key: string): string | undefined {
    const row = this.sql.exec<{ value: string }>("SELECT value FROM meta WHERE key = ?", key).toArray()[0];
    return row?.value;
  }

  // identity is the room's org and room, fixed by its first request.
  identity(): { org: string; room: Room; team: string } | undefined {
    const org = this.meta("org");
    const kind = this.meta("kind");
    const id = this.meta("id");
    if (!org || !kind || !id) return undefined;
    return { org, room: { kind: kind as Room["kind"], id }, team: this.meta("team") ?? "" };
  }

  // bind fixes the room's identity on first use and refuses any request for
  // another: the DO name came from the same org and room, so a mismatch is a
  // bug, never a caller's choice.
  private bind(org: string, room: Room, team: string): void {
    const current = this.identity();
    if (!current) {
      for (const [key, value] of Object.entries({ org, kind: room.kind, id: room.id, team })) {
        this.sql.exec("INSERT INTO meta (key, value) VALUES (?, ?)", key, value);
      }
      this.append({
        actor: { type: "system", id: "edge" },
        run: null,
        type: "room.created",
        summary: "Room created",
      });
      return;
    }
    if (current.org !== org || current.room.kind !== room.kind || current.room.id !== room.id) {
      throw new EdgeError("forbidden", "this room belongs to another organization or room");
    }
    if (team && !current.team) this.sql.exec("INSERT OR REPLACE INTO meta (key, value) VALUES ('team', ?)", team);
  }

  isMember(member: string): boolean {
    return this.sql.exec("SELECT 1 FROM members WHERE member = ?", member).toArray().length > 0;
  }

  private addMember(member: string): void {
    if (!member) return;
    this.sql.exec("INSERT OR IGNORE INTO members (member, added_at) VALUES (?, ?)", member, new Date().toISOString());
  }

  // spendToken records a token's jti and refuses one seen before (§2).
  private spendToken(claims: TokenClaims): void {
    const now = Math.floor(Date.now() / 1000);
    this.sql.exec("DELETE FROM seen_tokens WHERE exp < ?", now - 120);
    const inserted = this.sql.exec("INSERT OR IGNORE INTO seen_tokens (jti, exp) VALUES (?, ?)", claims.jti, claims.exp);
    if (inserted.rowsWritten === 0) throw new EdgeError("forbidden", "token already used");
  }

  // ---- requests ----

  override async fetch(request: Request): Promise<Response> {
    let caller: Caller;
    try {
      caller = this.authenticate(request);
    } catch (error) {
      await discardBody(request);
      return toResponse(error);
    }
    const url = new URL(request.url);
    const rest = request.headers.get(REST_HEADER) ?? "";
    try {
      return await this.route(request, url, rest, caller);
    } catch (error) {
      if (error instanceof EdgeError && (error.code === "forbidden" || error.code === "unauthorized")) {
        this.denied(caller, `${request.method} ${rest}`, error.message);
      }
      await discardBody(request);
      return toResponse(error);
    }
  }

  private authenticate(request: Request): Caller {
    const claimsHeader = request.headers.get(CLAIMS_HEADER);
    const webHeader = request.headers.get(WEB_HEADER);
    const roomHeader = request.headers.get(ROOM_HEADER);
    if (!roomHeader) throw new EdgeError("internal", "room not named");
    const room = JSON.parse(roomHeader) as Room;
    if (claimsHeader) {
      const claims = JSON.parse(claimsHeader) as TokenClaims;
      this.bind(claims.org, room, claims.team);
      const caller: Caller = { kind: "agent", claims };
      try {
        this.spendToken(claims);
      } catch (error) {
        this.denied(caller, `${request.method} ${request.headers.get(REST_HEADER) ?? ""}`, "token already used");
        throw error;
      }
      this.addMember(claims.member);
      return caller;
    }
    if (webHeader) {
      const identity = JSON.parse(webHeader) as WebIdentity;
      const current = this.identity();
      if (!current) throw new EdgeError("not_found", "no such room");
      if (current.org !== identity.org) throw new EdgeError("forbidden", "this room belongs to another organization");
      if (!this.isMember(identity.member)) throw new EdgeError("forbidden", "not a member of this room");
      return { kind: "web", identity };
    }
    throw new EdgeError("unauthorized", "no caller");
  }

  private async route(request: Request, url: URL, rest: string, caller: Caller): Promise<Response> {
    if (rest === "/log" && request.method === "GET") {
      requireScope(caller, "log:read");
      return json(this.readLog(caller, url.searchParams));
    }
    if (rest === "/log" && request.method === "POST") {
      if (caller.kind !== "agent") throw new EdgeError("forbidden", "only agents write the log");
      requireScope(caller, "log:write");
      const body = await readJson(request, AppendEventRequest, 64 << 10);
      const seq = this.append({
        actor: { type: "agent", id: caller.claims.agent, onBehalfOf: caller.claims.member },
        run: caller.claims.run || null,
        type: body.type,
        summary: body.summary,
        data: body.data,
        durationMs: body.durationMs,
        cost: body.cost,
        private: caller.claims.private,
      });
      return json({ seq }, 201);
    }
    if (rest === "/live" && request.headers.get("upgrade")?.toLowerCase() === "websocket") {
      requireScope(caller, "log:read");
      return this.subscribe(caller);
    }
    return this.children.route(request, url, rest, caller);
  }

  // ---- the log ----

  // append writes an event and returns its seq. Data and summary are capped,
  // and a flush to R2 is scheduled.
  append(event: NewEvent): number {
    const identity = this.identity();
    if (!identity) throw new Error("append before the room has an identity");
    let data = event.data ?? {};
    let encoded = JSON.stringify(data);
    if (encoded.length > DATA_LIMIT) {
      data = { truncated: true, size: encoded.length };
      encoded = JSON.stringify(data);
    }
    const at = new Date().toISOString();
    const row = this.sql
      .exec<{ seq: number }>(
        "INSERT INTO events (run, private, on_behalf_of, body) VALUES (?, ?, ?, '') RETURNING seq",
        event.run,
        event.private ? 1 : 0,
        event.actor.onBehalfOf ?? null,
      )
      .one();
    const full: Event = {
      seq: row.seq,
      at,
      org: identity.org,
      room: identity.room,
      actor: event.actor,
      run: event.run,
      type: event.type,
      summary: event.summary.slice(0, SUMMARY_LIMIT),
      data,
      durationMs: Math.max(0, Math.round(event.durationMs ?? 0)),
      ...(event.cost ? { cost: event.cost } : {}),
      private: Boolean(event.private),
    };
    this.sql.exec("UPDATE events SET body = ? WHERE seq = ?", JSON.stringify(full), row.seq);
    this.broadcast(full);
    this.ctx.waitUntil(this.scheduleFlush());
    return row.seq;
  }

  private async scheduleFlush(): Promise<void> {
    const current = await this.ctx.storage.getAlarm();
    const due = Date.now() + FLUSH_DELAY_MS;
    if (current === null || current > due) await this.ctx.storage.setAlarm(due);
  }

  // visible reports whether caller may see an event: a private one only by
  // the member it was for.
  private visible(caller: Caller, event: { private: number; on_behalf_of: string | null }): boolean {
    if (!event.private) return true;
    const member = caller.kind === "agent" ? caller.claims.member : caller.identity.member;
    return event.on_behalf_of === member;
  }

  readLog(caller: Caller, params: URLSearchParams): { events: Event[]; next: number; more: boolean } {
    const after = Number(params.get("after") ?? "0") || 0;
    const limit = Math.min(Math.max(Number(params.get("limit") ?? LOG_PAGE) || LOG_PAGE, 1), LOG_PAGE);
    const run = params.get("run");
    const rows = this.sql
      .exec<{ seq: number; private: number; on_behalf_of: string | null; body: string }>(
        run
          ? "SELECT seq, private, on_behalf_of, body FROM events WHERE seq > ? AND run = ? ORDER BY seq LIMIT ?"
          : "SELECT seq, private, on_behalf_of, body FROM events WHERE seq > ? ORDER BY seq LIMIT ?",
        ...(run ? [after, run, limit + 1] : [after, limit + 1]),
      )
      .toArray();
    const more = rows.length > limit;
    const page = rows.slice(0, limit);
    return {
      events: page.filter((row) => this.visible(caller, row)).map((row) => JSON.parse(row.body) as Event),
      next: page.length ? page[page.length - 1]!.seq : after,
      more,
    };
  }

  // denied logs a refused request in the room it was aimed at (§05:
  // isolation attacks are refused, and logged).
  private denied(caller: Caller, what: string, reason: string): void {
    if (!this.identity()) return;
    const actor: Event["actor"] =
      caller.kind === "agent"
        ? { type: "agent", id: caller.claims.agent, onBehalfOf: caller.claims.member }
        : { type: "member", id: caller.identity.member };
    this.append({
      actor,
      run: caller.kind === "agent" ? caller.claims.run || null : null,
      type: "access.denied",
      summary: `Refused ${what}: ${reason}`.slice(0, SUMMARY_LIMIT),
      data: { request: what, reason },
    });
  }

  // ---- live subscribers ----

  private subscribe(caller: Caller): Response {
    const pair = new WebSocketPair();
    const member = caller.kind === "agent" ? caller.claims.member : caller.identity.member;
    this.ctx.acceptWebSocket(pair[1], [member]);
    pair[1].serializeAttachment({ member });
    return new Response(null, { status: 101, webSocket: pair[0] });
  }

  private broadcast(event: Event): void {
    const message = JSON.stringify({ type: "event", event });
    for (const socket of this.ctx.getWebSockets()) {
      const attachment = socket.deserializeAttachment() as { member?: string } | null;
      if (event.private && attachment?.member !== event.actor.onBehalfOf) continue;
      try {
        socket.send(message);
      } catch {
        // A socket closing mid-send is dropped by the runtime.
      }
    }
  }

  override async webSocketMessage(socket: WebSocket, message: string | ArrayBuffer): Promise<void> {
    if (message === "ping") socket.send("pong");
  }

  override async webSocketClose(socket: WebSocket, code: number): Promise<void> {
    try {
      socket.close(code === 1005 ? 1000 : code, "closed");
    } catch {
      // already closed
    }
  }

  // ---- R2 ----

  override async alarm(): Promise<void> {
    let failed = false;
    try {
      await this.flush();
    } catch (error) {
      failed = true;
      console.error(JSON.stringify({ msg: "room log flush failed", error: String(error) }));
    }
    await this.children.sweep();
    const pending = this.sql.exec("SELECT 1 FROM events WHERE flushed = 0 LIMIT 1").toArray().length > 0;
    const next = this.children.nextSweep();
    const due = [pending ? Date.now() + (failed ? 30_000 : FLUSH_DELAY_MS) : null, next].filter(
      (value): value is number => value !== null,
    );
    if (due.length) await this.ctx.storage.setAlarm(Math.min(...due));
  }

  // flush writes unflushed events to R2, one ndjson object per run per
  // batch at runs/{org}/{kind}/{room}/{run}/events/{firstSeq}.ndjson, and
  // updates each run's manifest.json. Events are marked flushed only after
  // their object is written, so a failed write loses nothing.
  async flush(): Promise<number> {
    const identity = this.identity();
    if (!identity) return 0;
    let written = 0;
    for (;;) {
      const rows = this.sql
        .exec<{ seq: number; run: string | null; body: string }>(
          "SELECT seq, run, body FROM events WHERE flushed = 0 ORDER BY seq LIMIT ?",
          FLUSH_BATCH,
        )
        .toArray();
      if (rows.length === 0) break;
      const byRun = new Map<string, { seq: number; body: string }[]>();
      for (const row of rows) {
        const run = row.run ?? ROOM_RUN;
        byRun.set(run, [...(byRun.get(run) ?? []), row]);
      }
      for (const [run, events] of byRun) {
        const prefix = runPrefix(identity.org, identity.room, run);
        const key = `${prefix}events/${String(events[0]!.seq).padStart(8, "0")}.ndjson`;
        await this.env.RUNS.put(key, events.map((event) => event.body).join("\n") + "\n", {
          httpMetadata: { contentType: "application/x-ndjson" },
          customMetadata: { org: identity.org },
        });
        await this.updateManifest(identity, run, prefix, events.map((event) => JSON.parse(event.body) as Event));
      }
      const last = rows[rows.length - 1]!.seq;
      this.sql.exec("UPDATE events SET flushed = 1 WHERE flushed = 0 AND seq <= ?", last);
      written += rows.length;
    }
    this.sql.exec(
      "DELETE FROM events WHERE flushed = 1 AND seq <= (SELECT MAX(seq) FROM events) - ?",
      HOT_EVENTS,
    );
    return written;
  }

  private async updateManifest(
    identity: { org: string; room: Room },
    run: string,
    prefix: string,
    events: Event[],
  ): Promise<void> {
    const row = this.sql.exec<{ manifest: string }>("SELECT manifest FROM runs WHERE run = ?", run).toArray()[0];
    const manifest: RunManifest = row
      ? JSON.parse(row.manifest)
      : {
          org: identity.org,
          room: identity.room,
          run,
          startedAt: events[0]!.at,
          lastEventAt: events[0]!.at,
          actors: [],
          eventCount: 0,
          types: {},
          cost: { unit: "usd", amount: 0 },
          status: "active",
          events: `${prefix}events/`,
          artifacts: `${prefix}artifacts/`,
          media: `${prefix}media/`,
        };
    for (const event of events) {
      manifest.lastEventAt = event.at;
      manifest.eventCount++;
      manifest.types[event.type] = (manifest.types[event.type] ?? 0) + 1;
      const actor = `${event.actor.type}:${event.actor.id}${event.actor.onBehalfOf ? `@${event.actor.onBehalfOf}` : ""}`;
      if (!manifest.actors.includes(actor)) manifest.actors.push(actor);
      if (event.cost?.unit === "usd") manifest.cost.amount += event.cost.amount;
      if (event.type === "run.completed") manifest.status = "completed";
      if (event.type === "run.failed") manifest.status = "failed";
    }
    const encoded = JSON.stringify(manifest);
    await this.env.RUNS.put(`${prefix}manifest.json`, encoded, {
      httpMetadata: { contentType: "application/json" },
      customMetadata: { org: identity.org },
    });
    this.sql.exec("INSERT OR REPLACE INTO runs (run, manifest) VALUES (?, ?)", run, encoded);
  }
}

// RunManifest is a run's manifest.json (§7): who, what, when, cost, status
// and where everything else is.
export interface RunManifest {
  org: string;
  room: Room;
  run: string;
  startedAt: string;
  lastEventAt: string;
  actors: string[];
  eventCount: number;
  types: Record<string, number>;
  cost: { unit: "usd"; amount: number };
  status: "active" | "completed" | "failed";
  events: string;
  artifacts: string;
  media: string;
}

// requireScope refuses an agent whose token lacks scope. Members reaching
// the room through web-next may read what they can see; anything else they
// may do is checked where it is done.
export function requireScope(caller: Caller, scope: Scope): void {
  if (caller.kind === "web") {
    if (scope === "log:read") return;
    throw new EdgeError("forbidden", `members can't use ${scope} here`);
  }
  if (!caller.claims.scopes.includes(scope)) throw new EdgeError("forbidden", `token lacks ${scope}`);
}
