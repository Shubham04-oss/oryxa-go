// The room's call (docs/design/live-layer/03): B1's lifecycle, plus the
// routes for the transcript (B2), recording (B3), the agent's voice (B4) and
// the dispatched answers coming back (B5). RoomDO hands every /call… and
// /voice… request here, already authenticated: a member through WebEdge,
// whose membership the room checked, or Go with an edge token.
//
// One RealtimeKit meeting per call, made when the first member joins. Every
// member who joins gets their own RealtimeKit participant, deleted when they
// leave and when the call ends, which revokes its token.
import { roomDOName, type Room, validId, voiceDOName } from "../contracts";
import type { Env } from "../env";
import { EdgeError, json, readJson } from "../http";
import type { Caller, NewEvent, RoomDO } from "../room/room";
import {
  AgentRequest,
  CALL_EVENTS,
  type CallView,
  DispatchResult,
  JoinRequest,
  type JoinResponse,
  PRESET_MEMBER,
  RecordingRequest,
  TranscriptRequest,
} from "./contracts";
import { callProvider } from "./providers";
import { saveRecording, switchRecording } from "./recording";
import { type CallRow, CallStore } from "./store";
import { acceptTranscript, recordAgentSegment, saveFinalTranscript, saveLiveTranscript } from "./transcript";
import type { RealtimeKitEvent } from "./webhook";

// CallRoom is what the call needs of the RoomDO that holds it.
export interface CallRoom {
  room: Pick<RoomDO, "append" | "identity" | "isMember">;
  sql: SqlStorage;
  env: Env;
  waitUntil(promise: Promise<unknown>): void;
}

export interface RoomIdentity {
  org: string;
  room: Room;
  team: string;
}

// The agent's name, as the call shows it and as members address it.
export function agentName(env: Env): string {
  return env.VOICE_AGENT_NAME?.trim() || "Krude";
}

// meetingTitle names a call's meeting so a RealtimeKit webhook finds its way
// back to the room: every part is a contract id, which holds no colon.
export function meetingTitle(identity: RoomIdentity, callId: string): string {
  return `oryxa:${identity.org}:${identity.room.kind}:${identity.room.id}:${callId}`;
}

// callPrefix is where a call's record lives in R2 (contract §7).
export function callPrefix(identity: RoomIdentity, callId: string): string {
  return `calls/${identity.org}/${identity.room.kind}/${identity.room.id}/${callId}/`;
}

// One change to the call at a time per room: a RealtimeKit call in between
// awaits, and two members joining at once must not make two meetings.
const chains = new WeakMap<object, Promise<unknown>>();

function serialized<T>(key: object, work: () => Promise<T>): Promise<T> {
  const previous = chains.get(key) ?? Promise.resolve();
  const next = previous.then(work, work);
  chains.set(
    key,
    next.catch(() => undefined),
  );
  return next;
}

// removeMember takes a member out of the room's call as if they had left,
// revoking their participant and closing their voice sockets: the room
// removed them (A18).
export async function removeMember(ctx: CallRoom, member: string): Promise<void> {
  const identity = ctx.room.identity();
  if (!identity) return;
  await serialized(ctx.room, () => leave(ctx, new CallStore(ctx.sql), identity, member));
}

export function isCallPath(rest: string): boolean {
  return rest === "/call" || rest.startsWith("/call/") || rest.startsWith("/voice/");
}

export async function handleCall(ctx: CallRoom, request: Request, rest: string, caller: Caller): Promise<Response> {
  const identity = ctx.room.identity();
  if (!identity) throw new EdgeError("not_found", "no such room");
  const store = new CallStore(ctx.sql);

  // Go's dispatched answers come back here with an edge token.
  if (rest === "/voice/result" && request.method === "POST") {
    if (caller.kind !== "agent") throw new EdgeError("forbidden", "only agents post results");
    if (!caller.claims.scopes.includes("call:speak")) throw new EdgeError("forbidden", "token lacks call:speak");
    const result = await readJson(request, DispatchResult, 64 << 10);
    if (result.org !== identity.org || result.room.kind !== identity.room.kind || result.room.id !== identity.room.id || result.member !== caller.claims.member) {
      throw new EdgeError("forbidden", "the result is for another room or member than its token");
    }
    const accepted = await voice(ctx, identity).result(result);
    return json({ accepted }, 202);
  }

  // Everything else is a member's own.
  if (caller.kind !== "web") throw new EdgeError("forbidden", "calls are for members");
  const member = caller.identity.member;

  if (rest === "/voice/live" && request.headers.get("upgrade")?.toLowerCase() === "websocket") {
    // The agent's voice is for the people in the call, not the whole room.
    requirePresent(store, requireActive(store), member);
    const headers = new Headers({ upgrade: "websocket", [VOICE_MEMBER_HEADER]: member });
    return voiceStub(ctx, identity).fetch(new Request("https://voice.internal/live", { headers }));
  }
  if (rest === "/call" && request.method === "GET") {
    return json({ call: view(ctx, store, store.active()) });
  }
  if (request.method !== "POST") throw new EdgeError("not_found", "no such route");

  switch (rest) {
    case "/call/join": {
      const body = await readJson(request, JoinRequest, 4 << 10);
      return json(await serialized(ctx.room, () => join(ctx, store, identity, member, body.name ?? member)));
    }
    case "/call/leave":
      await serialized(ctx.room, () => leave(ctx, store, identity, member));
      return json({ left: true });
    case "/call/end": {
      await serialized(ctx.room, async () => {
        const call = store.active();
        if (call) await endCall(ctx, store, identity, call, { type: "member", id: member }, "ended by a member");
      });
      return json({ ended: true });
    }
    case "/call/transcript": {
      const body = await readJson(request, TranscriptRequest, 256 << 10);
      const accepted = acceptTranscript(ctx, store, identity, member, body, (segments) =>
        ctx.waitUntil(voice(ctx, identity).hear(segments)),
      );
      return json({ accepted }, 202);
    }
    case "/call/recording": {
      const body = await readJson(request, RecordingRequest, 1 << 10);
      const call = requireActive(store);
      requirePresent(store, call, member);
      const state = await serialized(ctx.room, () => switchRecording(ctx, store, identity, call.id, member, body.on));
      return json({ recording: state });
    }
    case "/call/agent": {
      const body = await readJson(request, AgentRequest, 1 << 10);
      const call = requireActive(store);
      requirePresent(store, call, member);
      if (Boolean(call.agent_present) !== body.present) {
        store.setAgentPresent(call.id, body.present);
        append(ctx, {
          actor: { type: "member", id: member },
          type: body.present ? CALL_EVENTS.agentJoined : CALL_EVENTS.agentLeft,
          summary: body.present ? `${member} asked ${agentName(ctx.env)} into the call` : `${member} dismissed ${agentName(ctx.env)} from the call`,
          data: { callId: call.id },
        });
        await voice(ctx, identity).setPresent(body.present);
      }
      return json({ call: view(ctx, store, store.active()) });
    }
  }
  throw new EdgeError("not_found", "no such route");
}

// VOICE_MEMBER_HEADER names the member a /voice/live socket is for. Only the
// room sets it: VoiceDO is reachable through its namespace alone.
export const VOICE_MEMBER_HEADER = "x-oryxa-voice-member";

function voiceStub(ctx: CallRoom, identity: RoomIdentity) {
  return ctx.env.VOICE.getByName(voiceDOName(identity.org, identity.room));
}

function voice(ctx: CallRoom, identity: RoomIdentity) {
  return voiceStub(ctx, identity);
}

function append(ctx: CallRoom, event: Omit<NewEvent, "run">): void {
  ctx.room.append({ ...event, run: null });
}

function requireActive(store: CallStore): CallRow {
  const call = store.active();
  if (!call) throw new EdgeError("conflict", "no call in this room");
  return call;
}

function requirePresent(store: CallStore, call: CallRow, member: string): void {
  const participant = store.participant(call.id, member);
  if (!participant || participant.left_at !== null) throw new EdgeError("forbidden", "join the call first");
}

function view(ctx: CallRoom, store: CallStore, call: CallRow | undefined): CallView | null {
  if (!call) return null;
  return {
    callId: call.id,
    startedAt: call.started_at,
    startedBy: call.started_by,
    participants: store.present(call.id).map((row) => ({ member: row.member, name: row.name, joinedAt: row.joined_at })),
    recording: store.recording(call),
    agent: { present: Boolean(call.agent_present), name: agentName(ctx.env) },
  };
}

function newCallId(): string {
  return `call_${crypto.randomUUID().replaceAll("-", "")}`;
}

async function join(ctx: CallRoom, store: CallStore, identity: RoomIdentity, member: string, name: string): Promise<JoinResponse> {
  const provider = callProvider(ctx.env);
  let call = store.active();
  if (!call) {
    const callId = newCallId();
    const meeting = await provider.createMeeting(meetingTitle(identity, callId));
    const at = new Date().toISOString();
    store.start(callId, meeting.id, member, at);
    call = store.byId(callId)!;
    append(ctx, {
      actor: { type: "member", id: member },
      type: CALL_EVENTS.started,
      summary: `${name} started a call`,
      data: { callId, meetingId: meeting.id },
    });
    await voice(ctx, identity).start({
      org: identity.org,
      room: identity.room,
      team: identity.team,
      callId,
      present: true,
    });
    append(ctx, {
      actor: { type: "agent", id: "voice" },
      type: CALL_EVENTS.agentJoined,
      summary: `${agentName(ctx.env)} joined the call`,
      data: { callId },
    });
  }

  // A member joining again, from another device or after a drop, gets a new
  // participant; the old one is deleted, and its token with it.
  const previous = store.participant(call.id, member);
  if (previous) await revoke(provider, call, previous.participant_id, "rejoin");
  const participant = await provider.addParticipant(call.meeting_id, { member, name, preset: PRESET_MEMBER });
  store.join(call.id, member, name, participant.id, new Date().toISOString());
  await voice(ctx, identity).joined(member);
  if (!previous || previous.left_at !== null) {
    append(ctx, {
      actor: { type: "member", id: member },
      type: CALL_EVENTS.joined,
      summary: `${name} joined the call`,
      data: { callId: call.id },
    });
  }
  return { callId: call.id, meetingId: call.meeting_id, participantId: participant.id, token: participant.token, preset: PRESET_MEMBER };
}

async function leave(ctx: CallRoom, store: CallStore, identity: RoomIdentity, member: string): Promise<void> {
  const call = store.active();
  if (!call) return;
  const participant = store.participant(call.id, member);
  if (!participant || !store.leave(call.id, member, new Date().toISOString())) return;
  await revoke(callProvider(ctx.env), call, participant.participant_id, "leave");
  await voice(ctx, identity).left(member);
  append(ctx, {
    actor: { type: "member", id: member },
    type: CALL_EVENTS.left,
    summary: `${participant.name} left the call`,
    data: { callId: call.id },
  });
  if (store.present(call.id).length === 0) {
    await endCall(ctx, store, identity, call, { type: "system", id: "edge" }, "everyone left");
  }
}

// endCall ends the session, revokes every token the call issued, stops the
// agent and saves the live transcript. It is done once; RealtimeKit's own
// meeting.ended, arriving after, finds it already ended.
export async function endCall(
  ctx: CallRoom,
  store: CallStore,
  identity: RoomIdentity,
  call: CallRow,
  actor: NewEvent["actor"],
  reason: string,
): Promise<void> {
  if (!store.end(call.id, new Date().toISOString())) return;
  const at = new Date().toISOString();
  for (const participant of store.present(call.id)) store.leave(call.id, participant.member, at);
  const provider = callProvider(ctx.env);
  try {
    await provider.endSession(call.meeting_id);
  } catch (error) {
    console.error(JSON.stringify({ msg: "end a call's session", callId: call.id, error: String(error) }));
  }
  for (const participant of store.everyone(call.id)) await revoke(provider, call, participant.participant_id, "end");
  if (call.recording_id) store.setRecording(call.id, null, null, null);
  await voice(ctx, identity).end(call.id);
  append(ctx, {
    actor,
    type: CALL_EVENTS.ended,
    summary: `The call ended: ${reason}`,
    data: { callId: call.id, startedAt: call.started_at },
  });
  ctx.waitUntil(saveLiveTranscript(ctx, store, identity, call.id));
}

// revoke deletes a RealtimeKit participant, which revokes its token. A
// failure is retried once, then logged: the token would otherwise outlive
// the call.
async function revoke(provider: ReturnType<typeof callProvider>, call: CallRow, participantId: string, why: string): Promise<void> {
  for (let attempt = 1; attempt <= 2; attempt++) {
    try {
      await provider.deleteParticipant(call.meeting_id, participantId);
      return;
    } catch (error) {
      if (attempt === 2) {
        console.error(JSON.stringify({ msg: "revoke a call participant failed", callId: call.id, participantId, why, error: String(error) }));
      }
    }
  }
}

// CallOp is what reaches the call by RPC on the RoomDO (RoomDO.callOp): a
// verified RealtimeKit webhook from the Worker, or what the agent said from
// the room's VoiceDO. Nothing outside this Worker can send one.
export type CallOp =
  | { type: "webhook"; event: RealtimeKitEvent; deliveryId: string; callId: string }
  | { type: "agentSegment"; callId: string; segmentId: string; text: string; at: number; name: string; privateFor: string | null }
  // present answers who is in the call now, by member id.
  | { type: "present"; callId: string };

export async function handleCallOp(ctx: CallRoom, op: CallOp): Promise<unknown> {
  const identity = ctx.room.identity();
  if (!identity) return undefined;
  const store = new CallStore(ctx.sql);
  if (op.type === "present") {
    const call = store.byId(op.callId);
    return call && call.ended_at === null ? store.present(op.callId).map((row) => row.member) : [];
  }
  if (op.type === "agentSegment") {
    recordAgentSegment(ctx, store, op.callId, op.segmentId, op.text, op.at, op.name, op.privateFor);
    return undefined;
  }
  // A delivery is marked done only once it is applied: RealtimeKit sends a
  // failed one again, and a retry of one that worked is dropped.
  if (store.seenWebhook(op.deliveryId)) return undefined;
  const event = op.event;
  const call = store.byId(op.callId);
  if (!call) {
    console.warn(JSON.stringify({ msg: "realtimekit webhook for an unknown call", event: event.event, callId: op.callId }));
    return undefined;
  }
  await serialized(ctx.room, async () => {
    switch (event.event) {
      case "meeting.ended":
        await endCall(ctx, store, identity, store.byId(call.id)!, { type: "system", id: "realtimekit" }, event.reason ?? "the meeting ended");
        break;
      case "meeting.participantLeft":
        // A dropped connection is not a leave: the member may come back. Their
        // seat is kept until they leave or the call ends.
        console.info(JSON.stringify({ msg: "call participant disconnected", callId: call.id, member: event.participant?.customParticipantId }));
        break;
      case "recording.statusUpdate":
        await saveRecording(ctx, store, identity, call, event);
        break;
      case "meeting.transcript":
        await saveFinalTranscript(ctx, identity, call, event);
        break;
    }
  });
  store.markWebhook(op.deliveryId);
  return undefined;
}

// roomForMeeting reads the room and call back out of a meeting's title.
export function roomForMeeting(title: string | undefined): { org: string; room: Room; callId: string; doName: string } | undefined {
  const parts = (title ?? "").split(":");
  if (parts.length !== 5 || parts[0] !== "oryxa") return undefined;
  const [, org, kind, id, callId] = parts as [string, string, string, string, string];
  if (!validId(callId)) return undefined;
  const room = { kind, id } as Room;
  try {
    return { org, room, callId, doName: roomDOName(org, room) };
  } catch {
    return undefined;
  }
}
