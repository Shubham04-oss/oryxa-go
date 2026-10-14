// VoiceDO: the agent in a room's call (docs/design/live-layer/03, B4), one
// per room, named org/{org}/room/{kind}/{id}/voice (contract §1).
//
// It is a virtual participant (the [decision] on discussion #217): it never
// joins the RealtimeKit meeting. The room tells it what each member says
// (the transcript segments their clients forward), and it speaks through
// voice.* frames on /voice/live, which every client in the call plays.
//
// It answers only when addressed. Small talk it answers itself with the fast
// model; real work it hands to the member's agent on AWS (B5), says so, and
// speaks the answer when it comes back. When a member talks over it, it
// stops (barge-in).
//
// Only the room reaches it: through its namespace, by RPC, and with
// /live sockets the room opened for one of its members.
import { DurableObject } from "cloudflare:workers";
import { CALL_EVENTS, type DispatchResult, MAX_CONTEXT_LINE_CHARS, type VoiceFrame } from "../call/contracts";
import { agentName, VOICE_MEMBER_HEADER } from "../call/index";
import type { Heard } from "../call/transcript";
import { type Room, roomDOName } from "../contracts";
import type { Env } from "../env";
import { base64Encode } from "../http";
import { addressedTo, FOLLOW_UP_MS } from "./address";
import type { Line } from "./brain";
import { DispatchError } from "./dispatch";
import { voiceProviders } from "./providers";
import { sentences, spoken } from "./speech";

export interface CallInfo {
  org: string;
  room: Room;
  team: string;
  callId: string;
  present: boolean;
}

// RECENT_LINES is how much of the call the model and the work agent read.
const RECENT_LINES = 12;
const KEPT_LINES = 40;
// MIN_BARGE_IN_CHARS keeps a cough or an "mm" from stopping the agent.
const MIN_BARGE_IN_CHARS = 4;
// DISPATCH_RETRIES is how often a dispatch a deploy interrupted is sent again.
const DISPATCH_RETRIES = 1;

type DispatchRow = {
  id: string;
  member: string;
  name: string;
  request: string;
  context: string;
  attempts: number;
  status: string;
};

export class VoiceDO extends DurableObject<Env> {
  private readonly sql: SqlStorage;
  // generation changes whenever what the agent is saying should stop: a
  // barge-in, a dismissal, the call ending. Speech made for an older
  // generation is dropped.
  private generation = 0;
  private speaking: { utteranceId: string; generation: number } | undefined;
  private lastSpokeAt = 0;
  private lastAnswered: string | undefined;

  constructor(ctx: DurableObjectState, env: Env) {
    super(ctx, env);
    this.sql = ctx.storage.sql;
    this.sql.exec(`
      CREATE TABLE IF NOT EXISTS voice_state (key TEXT PRIMARY KEY, value TEXT NOT NULL);
      CREATE TABLE IF NOT EXISTS voice_lines (at INTEGER NOT NULL, id TEXT PRIMARY KEY, speaker TEXT NOT NULL, name TEXT NOT NULL, text TEXT NOT NULL);
      CREATE TABLE IF NOT EXISTS voice_dispatches (
        id TEXT PRIMARY KEY, call_id TEXT NOT NULL, member TEXT NOT NULL, name TEXT NOT NULL, request TEXT NOT NULL,
        context TEXT NOT NULL, attempts INTEGER NOT NULL, status TEXT NOT NULL, at INTEGER NOT NULL
      );
    `);
  }

  // ---- state ----

  private call(): CallInfo | undefined {
    const row = this.sql.exec<{ value: string }>("SELECT value FROM voice_state WHERE key = 'call'").toArray()[0];
    return row ? (JSON.parse(row.value) as CallInfo) : undefined;
  }

  private saveCall(call: CallInfo | undefined): void {
    if (call) this.sql.exec("INSERT OR REPLACE INTO voice_state (key, value) VALUES ('call', ?)", JSON.stringify(call));
    else this.sql.exec("DELETE FROM voice_state WHERE key = 'call'");
  }

  private recent(limit = RECENT_LINES): Line[] {
    return this.sql
      .exec<{ name: string; text: string }>("SELECT name, text FROM (SELECT * FROM voice_lines ORDER BY at DESC, id DESC LIMIT ?) ORDER BY at, id", limit)
      .toArray()
      .map((row) => ({ name: row.name, text: row.text }));
  }

  private remember(id: string, speaker: string, name: string, text: string, at: number): void {
    this.sql.exec("INSERT OR IGNORE INTO voice_lines (at, id, speaker, name, text) VALUES (?, ?, ?, ?, ?)", at, id, speaker, name, text);
    this.sql.exec("DELETE FROM voice_lines WHERE id NOT IN (SELECT id FROM voice_lines ORDER BY at DESC, id DESC LIMIT ?)", KEPT_LINES);
  }

  private room(call: CallInfo) {
    return this.env.ROOM.getByName(roomDOName(call.org, call.room));
  }

  // ---- RPC from the room ----

  async start(call: CallInfo): Promise<void> {
    this.sql.exec("DELETE FROM voice_lines");
    this.saveCall(call);
    this.broadcast({ type: "voice.state", present: call.present, name: agentName(this.env) });
  }

  async end(callId: string): Promise<void> {
    const call = this.call();
    if (!call || call.callId !== callId) return;
    this.stop("call_ended");
    this.saveCall(undefined);
    this.sql.exec("DELETE FROM voice_lines");
    this.sql.exec("UPDATE voice_dispatches SET status = 'abandoned' WHERE call_id = ? AND status = 'pending'", callId);
    this.broadcast({ type: "voice.state", present: false, name: agentName(this.env) });
  }

  async setPresent(present: boolean): Promise<void> {
    const call = this.call();
    if (!call) return;
    this.saveCall({ ...call, present });
    if (!present) this.stop("dismissed");
    this.broadcast({ type: "voice.state", present, name: agentName(this.env) });
  }

  // hear takes what members said. It answers at once; replying goes on
  // after.
  async hear(segments: Heard[]): Promise<void> {
    const call = this.call();
    if (!call) return;
    for (const segment of segments) {
      // Someone talking over the agent stops it, before the words are final.
      if (this.speaking && segment.text.length >= MIN_BARGE_IN_CHARS) this.stop("barge_in");
      if (segment.partial) continue;
      this.remember(segment.id, segment.speaker, segment.name, segment.text, segment.at);
      if (!call.present) continue;
      const followUp = this.lastAnswered === segment.speaker && Date.now() - this.lastSpokeAt < FOLLOW_UP_MS;
      const address = addressedTo(segment.text, [agentName(this.env)], followUp);
      if (!address.addressed) continue;
      this.ctx.waitUntil(this.respond(call, segment, address.request));
    }
  }

  // result takes a dispatched run's answer from Go, speaks it, and posts it
  // in the room. It reports whether the dispatch was one this call made.
  async result(result: DispatchResult): Promise<boolean> {
    const row = this.sql
      .exec<DispatchRow>("SELECT * FROM voice_dispatches WHERE id = ? AND status = 'pending'", result.dispatchId)
      .toArray()[0];
    const call = this.call();
    if (!row || row.member !== result.member || !call || call.callId !== result.call) return false;

    if (result.status === "interrupted" && row.attempts <= DISPATCH_RETRIES) {
      // A deploy stopped the run: the next process runs it.
      this.ctx.waitUntil(this.send(call, row, row.attempts + 1));
      return true;
    }
    this.sql.exec("UPDATE voice_dispatches SET status = ? WHERE id = ?", result.status, row.id);
    const generation = this.generation;
    if (result.status === "completed" && result.text) {
      // The work agent used the member's own access. Until the room-audience
      // guard (Layer C) checks an answer against everyone listening, it is
      // spoken only when the member who asked is alone in the call; otherwise
      // it goes to them privately, and the room hears only that it did.
      const present = ((await this.room(call).callOp({ type: "present", callId: call.callId })) as string[] | undefined) ?? [];
      const alone = present.every((member) => member === row.member);
      this.room(call).append({
        actor: { type: "agent", id: "voice", onBehalfOf: row.member },
        run: null,
        type: CALL_EVENTS.result,
        summary: `${agentName(this.env)} answered ${row.name}: ${result.text}`,
        data: { callId: call.callId, dispatchId: row.id, threadId: result.threadId, runId: result.runId, text: result.text, spoken: alone },
        private: !alone,
      });
      if (alone) {
        const answer = spoken(result.text);
        this.ctx.waitUntil(this.speak(call, answer.cut ? `${answer.text} The rest is in the room.` : answer.text, row.member, generation));
      } else {
        this.sendTo(row.member, { type: "voice.private", dispatchId: row.id, text: result.text });
        this.ctx.waitUntil(this.speak(call, `${row.name}, I've sent you that answer privately.`, row.member, generation));
      }
    } else {
      this.room(call).append({
        actor: { type: "agent", id: "voice", onBehalfOf: row.member },
        run: null,
        type: CALL_EVENTS.result,
        summary: `${agentName(this.env)} could not finish ${row.name}'s request`,
        data: { callId: call.callId, dispatchId: row.id, status: result.status, error: result.error ?? null },
      });
      this.ctx.waitUntil(this.speak(call, `Sorry ${row.name}, I couldn't finish that one.`, row.member, generation));
    }
    return true;
  }

  // ---- replying ----

  private async respond(call: CallInfo, segment: Heard, request: string): Promise<void> {
    const providers = voiceProviders(this.env);
    const generation = ++this.generation;
    if (!providers.brain) {
      console.warn(JSON.stringify({ msg: "voice model not configured" }));
      return;
    }
    const recent = this.recent();
    let reply;
    try {
      reply = await providers.brain.reply({ agentName: agentName(this.env), memberName: segment.name, request, recent });
    } catch (error) {
      console.error(JSON.stringify({ msg: "voice model failed", error: String(error) }));
      await this.speak(call, "Sorry, I didn't catch that. Could you say it again?", segment.speaker, generation);
      return;
    }
    if (reply.kind === "say") {
      await this.speak(call, spoken(reply.text).text, segment.speaker, generation);
      return;
    }
    const row: DispatchRow = {
      id: `dsp_${crypto.randomUUID().replaceAll("-", "")}`,
      member: segment.speaker,
      name: segment.name,
      request: reply.request.slice(0, 4_000),
      context: JSON.stringify(recent.map((line) => `${line.name}: ${line.text}`.slice(0, MAX_CONTEXT_LINE_CHARS))),
      attempts: 1,
      status: "pending",
    };
    this.sql.exec(
      "INSERT INTO voice_dispatches (id, call_id, member, name, request, context, attempts, status, at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)",
      row.id,
      call.callId,
      row.member,
      row.name,
      row.request,
      row.context,
      row.attempts,
      row.status,
      Date.now(),
    );
    const sent = await this.send(call, row, 1);
    await this.speak(call, sent ? reply.acknowledgement : "I can't reach my tools right now. Try me again in a minute.", segment.speaker, generation);
  }

  // send hands a dispatch to Go, and reports whether Go took it.
  private async send(call: CallInfo, row: DispatchRow, attempt: number): Promise<boolean> {
    const agents = voiceProviders(this.env).agents;
    const fail = (reason: string) => {
      this.sql.exec("UPDATE voice_dispatches SET status = 'failed' WHERE id = ?", row.id);
      console.error(JSON.stringify({ msg: "dispatch to agents failed", dispatchId: row.id, reason }));
      return false;
    };
    if (!agents) return fail("agents not configured");
    this.sql.exec("UPDATE voice_dispatches SET attempts = ? WHERE id = ?", attempt, row.id);
    try {
      const accepted = await agents.dispatch({
        dispatchId: row.id,
        org: call.org,
        ...(call.team ? { team: call.team } : {}),
        room: call.room,
        call: call.callId,
        member: row.member,
        request: row.request,
        transcriptContext: JSON.parse(row.context) as string[],
      });
      this.room(call).append({
        actor: { type: "agent", id: "voice", onBehalfOf: row.member },
        run: null,
        type: CALL_EVENTS.dispatched,
        summary: `${agentName(this.env)} handed ${row.name}'s request to their agent`,
        data: { callId: call.callId, dispatchId: row.id, request: row.request, threadId: accepted.threadId, runId: accepted.runId, attempt },
      });
      return true;
    } catch (error) {
      return fail(error instanceof DispatchError ? error.message : String(error));
    }
  }

  // speak says text in the call, a sentence at a time, unless something
  // stops it first, and keeps it in the call's transcript.
  private async speak(call: CallInfo, text: string, answering: string, generation: number): Promise<void> {
    const speech = voiceProviders(this.env).speech;
    if (!text || generation !== this.generation || !this.call()?.present) return;
    const utteranceId = `utt_${crypto.randomUUID().replaceAll("-", "")}`;
    this.speaking = { utteranceId, generation };
    const at = Date.now();
    this.remember(utteranceId, "agent", agentName(this.env), text, at);
    this.ctx.waitUntil(
      this.room(call).callOp({ type: "agentSegment", callId: call.callId, segmentId: utteranceId, text, at, name: agentName(this.env) }),
    );
    this.broadcast({ type: "voice.speaking", utteranceId, text });
    try {
      if (!speech) throw new Error("speech not configured");
      const pieces = sentences(text);
      for (let seq = 0; seq < pieces.length; seq++) {
        const audio = await speech.synthesize(pieces[seq]!);
        if (generation !== this.generation) return;
        this.broadcast({ type: "voice.audio", utteranceId, seq, format: "mp3", data: base64Encode(audio), last: seq === pieces.length - 1 });
      }
    } catch (error) {
      console.error(JSON.stringify({ msg: "speak in a call", error: String(error) }));
    } finally {
      if (this.speaking?.utteranceId === utteranceId) this.speaking = undefined;
      this.lastSpokeAt = Date.now();
      this.lastAnswered = answering;
    }
  }

  private stop(reason: "barge_in" | "dismissed" | "call_ended"): void {
    this.generation++;
    if (!this.speaking) return;
    this.broadcast({ type: "voice.stop", utteranceId: this.speaking.utteranceId, reason });
    this.speaking = undefined;
  }

  // ---- the members' sockets ----

  override async fetch(request: Request): Promise<Response> {
    const member = request.headers.get(VOICE_MEMBER_HEADER);
    if (!member || request.headers.get("upgrade")?.toLowerCase() !== "websocket") {
      return new Response("not found", { status: 404 });
    }
    const pair = new WebSocketPair();
    this.ctx.acceptWebSocket(pair[1], [member]);
    const call = this.call();
    pair[1].send(JSON.stringify({ type: "voice.state", present: Boolean(call?.present), name: agentName(this.env) } satisfies VoiceFrame));
    return new Response(null, { status: 101, webSocket: pair[0] });
  }

  // sendTo sends a frame to one member's sockets only.
  private sendTo(member: string, frame: VoiceFrame): void {
    const message = JSON.stringify(frame);
    for (const socket of this.ctx.getWebSockets(member)) {
      try {
        socket.send(message);
      } catch {
        // A socket closing mid-send is dropped by the runtime.
      }
    }
  }

  private broadcast(frame: VoiceFrame): void {
    const message = JSON.stringify(frame);
    for (const socket of this.ctx.getWebSockets()) {
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
}
