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
  MAX_ROOM_MEMBERS,
  MembersRequest,
  conversationMembers,
  type Event,
  type Room,
  type Scope,
  type TokenClaims,
  ROOM_RUN,
  runPrefix,
} from "../contracts";
import type { Env } from "../env";
import { EdgeError, discardBody, json, readJson, toResponse } from "../http";
import { type CallOp, type CallRoom, handleCall, handleCallOp, isCallPath, removeMember as removeFromCall } from "../call/index";
import { notesDue, runNotes } from "../call/notes";
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
// MAX_ARCHIVE_LISTS bounds how many R2 listings one read of old events makes.
const MAX_ARCHIVE_LISTS = 20;

type LogRow = {
  seq: number;
  private: number;
  on_behalf_of: string | null;
  body: string;
};
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
      CREATE TABLE IF NOT EXISTS removed_members (member TEXT PRIMARY KEY, removed_at TEXT NOT NULL);
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

  // members lists the room's members, sorted.
  members(): string[] {
    return this.sql
      .exec<{ member: string }>("SELECT member FROM members ORDER BY member")
      .toArray()
      .map((row) => row.member);
  }

  private wasRemoved(member: string): boolean {
    return this.sql.exec("SELECT 1 FROM removed_members WHERE member = ?", member).toArray().length > 0;
  }

  // mayJoin is whether member can be one of this room's members at all: a
  // direct message or side conversation has exactly the two its id names.
  private mayJoin(room: Room, member: string): boolean {
    if (room.kind !== "dm" && room.kind !== "side") return true;
    return conversationMembers(room)?.includes(member) ?? false;
  }

  // admitByToken lets an agent's token make its member a room member, the
  // way rooms learned members before Go pushed them (A18) and still do for a
  // thread's owner. It refuses a member Go removed, until Go adds them again,
  // and anyone a conversation room's id doesn't name.
  private admitByToken(room: Room, member: string): void {
    if (!member) return;
    if (!this.mayJoin(room, member)) throw new EdgeError("forbidden", "not a member of this conversation");
    if (this.wasRemoved(member)) throw new EdgeError("forbidden", "removed from this room");
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
      if (!claims.scopes.includes("room:members")) {
        try {
          this.admitByToken(room, claims.member);
        } catch (error) {
          this.denied(caller, `${request.method} ${request.headers.get(REST_HEADER) ?? ""}`, (error as Error).message);
          throw error;
        }
      }
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
      return json(await this.readLog(caller, url.searchParams));
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
    if (rest === "/members") {
      if (caller.kind !== "agent") throw new EdgeError("forbidden", "only agents set a room's members");
      requireScope(caller, "room:members");
      if (request.method === "GET") return json({ members: this.members() });
      if (request.method !== "PUT") throw new EdgeError("not_found", "no such route");
      const body = await readJson(request, MembersRequest, 256 << 10);
      return json({ members: await this.setMembers(body) });
    }
    if (rest === "/live" && request.headers.get("upgrade")?.toLowerCase() === "websocket") {
      requireScope(caller, "log:read");
      return this.subscribe(caller);
    }
    // The call and the agent's voice (edge/src/call, integration-2).
    if (isCallPath(rest)) return handleCall(this.callRoom(), request, rest, caller);
    return this.children.route(request, url, rest, caller);
  }

  // ---- membership (A18) ----

  // setMembers applies Go's membership: a full replace or a change. A
  // member removed is out of the call, their live sockets are closed, their
  // browsers and private sandbox are stopped, and their agent's token no
  // longer brings them back.
  private async setMembers(request: MembersRequest): Promise<string[]> {
    const identity = this.identity()!;
    const current = new Set(this.members());
    let add: string[];
    let remove: string[];
    if (request.members) {
      const wanted = new Set(request.members);
      add = [...wanted].filter((member) => !current.has(member));
      remove = [...current].filter((member) => !wanted.has(member));
    } else {
      add = [...new Set(request.add ?? [])].filter((member) => !current.has(member));
      remove = [...new Set(request.remove ?? [])].filter((member) => current.has(member) || !this.wasRemoved(member));
    }
    const refused = add.filter((member) => !this.mayJoin(identity.room, member));
    if (refused.length) throw new EdgeError("invalid_request", `not a member this conversation can have: ${refused.join(", ")}`);
    if (current.size + add.length - remove.filter((member) => current.has(member)).length > MAX_ROOM_MEMBERS) {
      throw new EdgeError("too_large", `a room has at most ${MAX_ROOM_MEMBERS} members`);
    }

    const at = new Date().toISOString();
    for (const member of add) {
      this.sql.exec("INSERT OR IGNORE INTO members (member, added_at) VALUES (?, ?)", member, at);
      this.sql.exec("DELETE FROM removed_members WHERE member = ?", member);
    }
    for (const member of remove) {
      this.sql.exec("DELETE FROM members WHERE member = ?", member);
      this.sql.exec("INSERT OR REPLACE INTO removed_members (member, removed_at) VALUES (?, ?)", member, at);
    }
    if (add.length || remove.length) {
      this.append({
        actor: { type: "system", id: "agents" },
        run: null,
        type: "room.members.changed",
        summary: `Members changed: ${add.length} added, ${remove.length} removed`,
        data: { added: add.slice(0, 100), removed: remove.slice(0, 100) },
      });
    }
    for (const member of remove) await this.memberRemoved(member);
    return this.members();
  }

  private async memberRemoved(member: string): Promise<void> {
    for (const socket of this.ctx.getWebSockets(member)) {
      try {
        socket.close(4403, "removed from this room");
      } catch {
        // already closed
      }
    }
    try {
      await removeFromCall(this.callRoom(), member);
    } catch (error) {
      console.error(JSON.stringify({ msg: "removing a member from the call failed", error: String(error) }));
    }
    await this.children.memberRemoved(member);
  }

  // ---- the call (edge/src/call) ----

  private callRoom(): CallRoom {
    return {
      room: this,
      sql: this.sql,
      env: this.env,
      waitUntil: (promise) => this.ctx.waitUntil(promise),
      wake: (at) => this.ctx.waitUntil(this.wake(at)),
    };
  }

  // wake sets the room's alarm for at, unless it is already due sooner.
  private async wake(at: number): Promise<void> {
    const current = await this.ctx.storage.getAlarm();
    if (current === null || current > at) await this.ctx.storage.setAlarm(at);
  }

  // callOp is the call's RPC entry, for the Worker's RealtimeKit webhook and
  // the room's VoiceDO; nothing outside this Worker reaches it.
  async callOp(op: CallOp): Promise<unknown> {
    return handleCallOp(this.callRoom(), op);
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

  // hotEvents is how many flushed events stay in SQLite; a field so a test
  // can make the R2 fallback below reachable without 10,000 events.
  hotEvents = HOT_EVENTS;

  // readLog pages through the log after a seq, optionally for one run
  // (ROOM_RUN for the room's own events, which belong to no run). The newest
  // events are in SQLite; older ones the flush pruned are read back from R2.
  async readLog(caller: Caller, params: URLSearchParams): Promise<{ events: Event[]; next: number; more: boolean }> {
    const after = Number(params.get("after") ?? "0") || 0;
    const limit = Math.min(Math.max(Number(params.get("limit") ?? LOG_PAGE) || LOG_PAGE, 1), LOG_PAGE);
    const run = params.get("run");
    const runFilter = run === null ? "" : run === ROOM_RUN ? " AND run IS NULL" : " AND run = ?";
    const runArgs = run === null || run === ROOM_RUN ? [] : [run];

    let older: LogRow[] = [];
    const oldest = this.sql.exec<{ seq: number | null }>("SELECT MIN(seq) AS seq FROM events").one().seq;
    if (oldest !== null && after + 1 < oldest) older = await this.archived(after, oldest, run, limit + 1);

    const hot = this.sql
      .exec<LogRow>(
        `SELECT seq, private, on_behalf_of, body FROM events WHERE seq > ?${runFilter} ORDER BY seq LIMIT ?`,
        after,
        ...runArgs,
        limit + 1,
      )
      .toArray();
    const rows = [...older, ...hot].slice(0, limit + 1);
    const more = rows.length > limit;
    const page = rows.slice(0, limit);
    return {
      events: page.filter((row) => this.visible(caller, row)).map((row) => JSON.parse(row.body) as Event),
      next: page.length ? page[page.length - 1]!.seq : after,
      more,
    };
  }

  // archived reads events with after < seq < before from R2, where the flush
  // wrote them in chunks per run (events/{firstSeq}.ndjson), in seq order,
  // stopping once it has wanted of them and no earlier chunk can add one.
  private async archived(after: number, before: number, run: string | null, wanted: number): Promise<LogRow[]> {
    const identity = this.identity();
    if (!identity) return [];
    const base = run === null ? `runs/${identity.org}/${identity.room.kind}/${identity.room.id}/` : runPrefix(identity.org, identity.room, run);
    const chunks: { key: string; first: number }[] = [];
    let cursor: string | undefined;
    for (let page = 0; page < MAX_ARCHIVE_LISTS; page++) {
      const listed = await this.env.RUNS.list({ prefix: base, cursor });
      for (const object of listed.objects) {
        const match = /\/events\/(\d{8,})\.ndjson$/.exec(object.key);
        if (match) chunks.push({ key: object.key, first: Number(match[1]) });
      }
      if (!listed.truncated) break;
      cursor = listed.cursor;
    }
    chunks.sort((a, b) => a.first - b.first);
    // A chunk starting at or before after can still hold later events of its
    // run, so the last such chunk per run is read too.
    const lastBefore = new Map<string, number>();
    chunks.forEach((chunk, index) => {
      if (chunk.first <= after + 1) lastBefore.set(chunk.key.slice(0, chunk.key.lastIndexOf("/events/")), index);
    });
    const firstIndex = lastBefore.size ? Math.min(...lastBefore.values()) : 0;

    const rows: LogRow[] = [];
    for (let index = firstIndex; index < chunks.length; index++) {
      const chunk = chunks[index]!;
      if (chunk.first >= before) break;
      if (rows.length >= wanted && chunk.first > rows[wanted - 1]!.seq) break;
      const object = await this.env.RUNS.get(chunk.key);
      if (!object) continue;
      for (const line of (await object.text()).split("\n")) {
        if (!line) continue;
        const event = JSON.parse(line) as Event;
        if (event.seq <= after || event.seq >= before) continue;
        rows.push({ seq: event.seq, private: event.private ? 1 : 0, on_behalf_of: event.actor.onBehalfOf ?? null, body: line });
      }
      rows.sort((a, b) => a.seq - b.seq);
    }
    return rows.slice(0, wanted);
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
    try {
      await runNotes(this.callRoom());
    } catch (error) {
      console.error(JSON.stringify({ msg: "a call's notes failed", error: String(error) }));
    }
    const pending = this.sql.exec("SELECT 1 FROM events WHERE flushed = 0 LIMIT 1").toArray().length > 0;
    const next = this.children.nextSweep();
    const notes = notesDue(this.sql);
    const due = [pending ? Date.now() + (failed ? 30_000 : FLUSH_DELAY_MS) : null, next, notes === null ? null : Math.max(notes, Date.now() + 1_000)].filter(
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
      this.hotEvents,
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
