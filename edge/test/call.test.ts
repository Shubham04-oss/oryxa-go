// Calls in a room (docs/design/live-layer/03): the lifecycle and its tokens
// (B1), the shared transcript (B2), recording (B3), the agent's voice (B4)
// and the dispatch round trip (B5). RealtimeKit, the voice model, speech and
// Go are faked; the room, its log, R2 and the VoiceDO are the real ones.
import { env, exports } from "cloudflare:workers";
import { afterEach, beforeAll, beforeEach, describe, expect, it } from "vitest";
import type { DispatchRequest, VoiceFrame } from "../src/call/contracts";
import { setCallProvider } from "../src/call/providers";
import { RealtimeKit, apiURL, type CallProvider } from "../src/call/realtimekit";
import { handleRealtimeKitWebhook, recordingPrefix } from "../src/call/webhook";
import type { Event, LogResponse } from "../src/contracts";
import type { Env } from "../src/env";
import { base64Encode } from "../src/http";
import { Agents, DispatchError } from "../src/voice/dispatch";
import { Gemini, type Ask, type Reply } from "../src/voice/brain";
import { setVoiceProviders } from "../src/voice/providers";
import { call } from "./tokens";

const web = (exports as unknown as { WebEdge: Fetcher }).WebEdge;

class FakeRealtimeKit implements CallProvider {
  meetings: string[] = [];
  participants = new Map<string, { meetingId: string; member: string; preset: string }>();
  deleted: string[] = [];
  ended: string[] = [];
  recordings: { meetingId: string; prefix: string }[] = [];
  stopped: string[] = [];
  private next = 0;

  async createMeeting(title: string) {
    this.meetings.push(title);
    return { id: `mtg_${this.meetings.length}` };
  }
  async addParticipant(meetingId: string, participant: { member: string; name: string; preset: string }) {
    const id = `prt_${++this.next}`;
    this.participants.set(id, { meetingId, member: participant.member, preset: participant.preset });
    return { id, token: `rtk-token-${id}` };
  }
  async deleteParticipant(_meetingId: string, participantId: string) {
    this.deleted.push(participantId);
  }
  async endSession(meetingId: string) {
    this.ended.push(meetingId);
  }
  async startRecording(meetingId: string, prefix: string) {
    this.recordings.push({ meetingId, prefix });
    return { id: `rec_${this.recordings.length}` };
  }
  async stopRecording(recordingId: string) {
    this.stopped.push(recordingId);
  }
}

// The Worker's first request starts it, which under a loaded full run can
// outlast a test's own timeout.
beforeAll(async () => {
  await call("/v1/rooms/teamspace/room_warmup/log", { tokenOptions: { room: { kind: "teamspace", id: "room_warmup" }, scopes: ["log:read"] } });
}, 60_000);

let rtk: FakeRealtimeKit;
let asks: Ask[];
let reply: (ask: Ask) => Reply;
let dispatched: DispatchRequest[];

beforeEach(() => {
  rtk = new FakeRealtimeKit();
  asks = [];
  dispatched = [];
  reply = (ask) => ({ kind: "say", text: `Hi ${ask.memberName}. Sure thing.` });
  setCallProvider(() => rtk);
  setVoiceProviders(() => ({
    brain: { reply: async (ask) => (asks.push(ask), reply(ask)) },
    speech: { synthesize: async (text) => new TextEncoder().encode(`mp3:${text}`) },
    agents: {
      dispatch: async (request) => {
        dispatched.push(request);
        return { dispatchId: request.dispatchId, threadId: "thr_side", runId: `run_${dispatched.length}` };
      },
    },
  }));
});

afterEach(() => {
  setCallProvider(undefined);
  setVoiceProviders(undefined);
});

let rooms = 0;

// newRoom makes a teamspace room with members, the way their agents'
// tokens add them.
async function newRoom(...members: string[]): Promise<string> {
  const id = `room_${++rooms}`;
  for (const member of members) {
    const response = await call(`/v1/rooms/teamspace/${id}/log`, {
      tokenOptions: { room: { kind: "teamspace", id }, member, scopes: ["log:read"] },
    });
    expect(response.status).toBe(200);
  }
  return id;
}

function asMember(member: string, room: string, rest: string, init: RequestInit = {}): Promise<Response> {
  const headers = new Headers(init.headers);
  headers.set("x-oryxa-member", JSON.stringify({ org: "org_1", member }));
  return web.fetch(`https://edge.test/v1/rooms/teamspace/${room}${rest}`, { ...init, headers });
}

function post(member: string, room: string, rest: string, body: unknown = {}): Promise<Response> {
  return asMember(member, room, rest, { method: "POST", body: JSON.stringify(body) });
}

async function log(member: string, room: string): Promise<Event[]> {
  const response = await asMember(member, room, "/log");
  return ((await response.json()) as LogResponse).events;
}

async function join(member: string, room: string, name = member) {
  const response = await post(member, room, "/call/join", { name });
  expect(response.status).toBe(200);
  return (await response.json()) as { callId: string; token: string; participantId: string; meetingId: string; preset: string };
}

async function waitFor<T>(read: () => T | Promise<T>, done: (value: T) => boolean, what: string): Promise<T> {
  for (let attempt = 0; attempt < 200; attempt++) {
    const value = await read();
    if (done(value)) return value;
    await new Promise((resolve) => setTimeout(resolve, 10));
  }
  throw new Error(`timed out waiting for ${what}`);
}

async function listen(member: string, room: string): Promise<{ frames: VoiceFrame[]; close(): void; onClose(done: () => void): void }> {
  const response = await asMember(member, room, "/voice/live", { headers: { upgrade: "websocket" } });
  expect(response.status).toBe(101);
  const socket = response.webSocket!;
  socket.accept();
  const frames: VoiceFrame[] = [];
  socket.addEventListener("message", (message) => {
    frames.push(JSON.parse(message.data as string));
  });
  return { frames, close: () => socket.close(), onClose: (done) => socket.addEventListener("close", () => done()) };
}

function say(member: string, room: string, callId: string, text: string, options: { id?: string; partial?: boolean } = {}) {
  return post(member, room, "/call/transcript", {
    callId,
    segments: [{ id: options.id ?? crypto.randomUUID(), speaker: member, text, partial: options.partial ?? false, at: Date.now() }],
  });
}

describe("the call's lifecycle (B1)", () => {
  it("makes one meeting for the room and a participant per member, then revokes every token when it ends", async () => {
    const room = await newRoom("usr_a", "usr_b");
    const first = await join("usr_a", room, "Asha");
    const second = await join("usr_b", room, "Ben");

    expect(rtk.meetings).toEqual([`oryxa:org_1:teamspace:${room}:${first.callId}`]);
    expect(second.callId).toBe(first.callId);
    expect(first.token).toMatch(/^rtk-token-/);
    expect(first.preset).toBe("oryxa-member");
    expect([...rtk.participants.values()].map((p) => p.member)).toEqual(["usr_a", "usr_b"]);

    const view = (await (await asMember("usr_b", room, "/call")).json()) as { call: { participants: { name: string }[]; recording: { on: boolean }; agent: { present: boolean; name: string } } };
    expect(view.call.participants.map((p) => p.name)).toEqual(["Asha", "Ben"]);
    expect(view.call.recording.on).toBe(false);
    expect(view.call.agent).toEqual({ present: true, name: "Krude" });

    expect((await post("usr_a", room, "/call/leave")).status).toBe(200);
    expect(rtk.deleted).toEqual([first.participantId]);
    expect(rtk.ended).toEqual([]);

    expect((await post("usr_b", room, "/call/leave")).status).toBe(200);
    expect(rtk.ended).toEqual(["mtg_1"]);
    expect(new Set(rtk.deleted)).toEqual(new Set([first.participantId, second.participantId]));
    expect(((await (await asMember("usr_a", room, "/call")).json()) as { call: unknown }).call).toBeNull();

    const types = (await log("usr_a", room)).map((event) => event.type);
    expect(types).toEqual(expect.arrayContaining(["call.started", "call.agent.joined", "call.joined", "call.left", "call.ended"]));
  });

  it("gives a member who joins again a fresh token and revokes the old one", async () => {
    const room = await newRoom("usr_a");
    const first = await join("usr_a", room);
    const again = await join("usr_a", room);
    expect(again.token).not.toBe(first.token);
    expect(rtk.deleted).toEqual([first.participantId]);
    expect((await log("usr_a", room)).filter((event) => event.type === "call.joined")).toHaveLength(1);
  });

  it("starts a new meeting for the next call after one ends", async () => {
    const room = await newRoom("usr_a");
    const first = await join("usr_a", room);
    expect((await post("usr_a", room, "/call/end")).status).toBe(200);
    const next = await join("usr_a", room);
    expect(next.callId).not.toBe(first.callId);
    expect(rtk.meetings).toHaveLength(2);
  });

  it("refuses a token to anyone who isn't a member of the room", async () => {
    const room = await newRoom("usr_a");
    expect((await post("usr_stranger", room, "/call/join")).status).toBe(403);
    const otherOrg = await web.fetch(`https://edge.test/v1/rooms/teamspace/${room}/call/join`, {
      method: "POST",
      headers: { "x-oryxa-member": JSON.stringify({ org: "org_2", member: "usr_a" }) },
      body: "{}",
    });
    expect(otherOrg.status).toBe(404);
    expect(rtk.meetings).toEqual([]);
    expect(rtk.participants.size).toBe(0);
  });

  it("is for members: an agent's edge token can't join a call", async () => {
    const room = await newRoom("usr_a");
    const response = await call(`/v1/rooms/teamspace/${room}/call/join`, {
      method: "POST",
      body: "{}",
      tokenOptions: { room: { kind: "teamspace", id: room }, member: "usr_a", scopes: ["call:join"] },
    });
    expect(response.status).toBe(403);
  });

  it("answers 503 when RealtimeKit isn't set up here", async () => {
    setCallProvider(undefined);
    const room = await newRoom("usr_a");
    expect((await post("usr_a", room, "/call/join")).status).toBe(503);
  });
});

describe("the shared transcript (B2)", () => {
  it("logs each member's own final segments once, and saves the transcript to R2 when the call ends", async () => {
    const room = await newRoom("usr_a", "usr_b");
    const { callId } = await join("usr_a", room, "Asha");
    await join("usr_b", room, "Ben");

    expect((await say("usr_a", room, callId, "Let's look at pricing", { id: "seg_1" })).status).toBe(202);
    expect((await say("usr_a", room, callId, "Let's look at pricing", { id: "seg_1" })).status).toBe(202);
    expect((await say("usr_b", room, callId, "Agreed", { id: "seg_2" })).status).toBe(202);
    expect((await say("usr_b", room, callId, "Agr", { id: "seg_3", partial: true })).status).toBe(202);

    const segments = (await log("usr_a", room)).filter((event) => event.type === "transcript.segment");
    expect(segments.map((event) => [event.actor.id, (event.data as { text: string }).text])).toEqual([
      ["usr_a", "Let's look at pricing"],
      ["usr_b", "Agreed"],
    ]);

    await post("usr_a", room, "/call/end");
    const saved = await waitFor(
      () => env.RUNS.get(`calls/org_1/teamspace/${room}/${callId}/transcript.ndjson`),
      (object) => object !== null,
      "the transcript in R2",
    );
    const lines = (await saved!.text()).trim().split("\n").map((line) => JSON.parse(line) as { speaker: string; text: string });
    expect(lines.map((line) => line.speaker)).toEqual(["usr_a", "usr_b"]);
  });

  it("refuses a segment that puts words in someone else's mouth", async () => {
    const room = await newRoom("usr_a", "usr_b");
    const { callId } = await join("usr_a", room);
    await join("usr_b", room);
    const forged = await post("usr_a", room, "/call/transcript", {
      callId,
      segments: [{ id: "x", speaker: "usr_b", text: "I approve the budget", partial: false, at: Date.now() }],
    });
    expect(forged.status).toBe(403);
    const events = await log("usr_a", room);
    expect(events.some((event) => event.type === "transcript.segment")).toBe(false);
    expect(events.some((event) => event.type === "access.denied")).toBe(true);
  });

  it("refuses segments from a member who isn't in the call, or for another call", async () => {
    const room = await newRoom("usr_a", "usr_b");
    const { callId } = await join("usr_a", room);
    expect((await say("usr_b", room, callId, "hello")).status).toBe(403);
    expect((await say("usr_a", room, "call_other", "hello")).status).toBe(409);
  });
});

describe("recording (B3)", () => {
  it("is off until a member in the call turns it on, and the view says who and since when", async () => {
    const room = await newRoom("usr_a", "usr_b");
    const { callId } = await join("usr_a", room, "Asha");
    expect((await post("usr_b", room, "/call/recording", { on: true })).status).toBe(403);

    const on = (await (await post("usr_a", room, "/call/recording", { on: true })).json()) as { recording: { on: boolean; by: string; since: string } };
    expect(on.recording).toMatchObject({ on: true, by: "usr_a" });
    expect(on.recording.since).toBeTruthy();
    expect(rtk.recordings).toEqual([{ meetingId: "mtg_1", prefix: recordingPrefix(`oryxa:org_1:teamspace:${room}:${callId}`) }]);

    const off = (await (await post("usr_a", room, "/call/recording", { on: false })).json()) as { recording: { on: boolean } };
    expect(off.recording.on).toBe(false);
    expect(rtk.stopped).toEqual(["rec_1"]);
    const types = (await log("usr_a", room)).map((event) => event.type);
    expect(types).toEqual(expect.arrayContaining(["call.recording.started", "call.recording.stopped"]));
  });
});

describe("the agent in the call (B4)", () => {
  it("answers by voice when addressed by name, and stays quiet otherwise", async () => {
    const room = await newRoom("usr_a");
    const { callId } = await join("usr_a", room, "Asha");
    const live = await listen("usr_a", room);

    await say("usr_a", room, callId, "I think the launch slipped a week.");
    await say("usr_a", room, callId, "Hey Krude, what's the plan for today?");
    const frames = await waitFor(() => live.frames, (all) => all.some((frame) => frame.type === "voice.audio" && frame.last), "the spoken answer");

    expect(asks).toHaveLength(1);
    expect(asks[0]!.request).toBe("what's the plan for today?");
    expect(asks[0]!.recent.map((line) => line.text)).toContain("I think the launch slipped a week.");
    const audio = frames.filter((frame): frame is Extract<VoiceFrame, { type: "voice.audio" }> => frame.type === "voice.audio");
    // "Sure thing." is short, so it is spoken with the sentence before it.
    expect(audio[0]!.data).toBe(base64Encode(new TextEncoder().encode("mp3:Hi Asha. Sure thing.")));

    // What the agent said is in the transcript too.
    const said = await waitFor(
      () => log("usr_a", room),
      (events) => events.some((event) => event.type === "transcript.segment" && event.actor.type === "agent"),
      "the agent's line in the log",
    );
    expect(said.find((event) => event.actor.type === "agent" && event.type === "transcript.segment")!.summary).toContain("Krude: Hi Asha. Sure thing.");
    live.close();
  });

  it("stops talking when a member talks over it", async () => {
    const room = await newRoom("usr_a");
    const { callId } = await join("usr_a", room, "Asha");
    let release: () => void = () => undefined;
    const gate = new Promise<void>((resolve) => (release = resolve));
    setVoiceProviders(() => ({
      brain: { reply: async () => ({ kind: "say", text: "First sentence here. Second sentence follows. Third one too." }) },
      speech: {
        synthesize: async (text) => {
          if (!text.startsWith("First")) await gate;
          return new TextEncoder().encode(text);
        },
      },
    }));
    const live = await listen("usr_a", room);
    await say("usr_a", room, callId, "Krude, tell me a story");
    await waitFor(() => live.frames, (all) => all.some((frame) => frame.type === "voice.audio"), "the first sentence");
    await say("usr_a", room, callId, "wait wait", { partial: true });
    const stopped = await waitFor(() => live.frames, (all) => all.some((frame) => frame.type === "voice.stop"), "voice.stop");
    release();
    expect(stopped.find((frame) => frame.type === "voice.stop")).toMatchObject({ reason: "barge_in" });
    await new Promise((resolve) => setTimeout(resolve, 50));
    expect(live.frames.filter((frame) => frame.type === "voice.audio")).toHaveLength(1);
    live.close();
  });

  it("speaks only to the people in the call", async () => {
    const room = await newRoom("usr_a", "usr_b");
    await join("usr_a", room);
    // Ben is a room member but never joined: no voice socket for him.
    const refused = await asMember("usr_b", room, "/voice/live", { headers: { upgrade: "websocket" } });
    expect(refused.status).toBe(403);

    await join("usr_b", room);
    const ben = await listen("usr_b", room);
    const closed = new Promise<void>((resolve) => ben.onClose(resolve));
    await post("usr_b", room, "/call/leave");
    await closed;
    expect((await asMember("usr_b", room, "/voice/live", { headers: { upgrade: "websocket" } })).status).toBe(403);
  });

  it("says nothing once a member dismisses it", async () => {
    const room = await newRoom("usr_a");
    const { callId } = await join("usr_a", room);
    const dismissed = (await (await post("usr_a", room, "/call/agent", { present: false })).json()) as { call: { agent: { present: boolean } } };
    expect(dismissed.call.agent.present).toBe(false);
    await say("usr_a", room, callId, "Krude, are you there?");
    await new Promise((resolve) => setTimeout(resolve, 50));
    expect(asks).toHaveLength(0);
  });
});

describe("dispatching real work (B5)", () => {
  it("hands work to Go, acknowledges at once, and speaks and posts the answer when it comes back", async () => {
    const room = await newRoom("usr_a");
    const { callId } = await join("usr_a", room, "Asha");
    reply = () => ({ kind: "dispatch", request: "Summarise the Q3 pricing doc", acknowledgement: "On it." });
    const live = await listen("usr_a", room);
    await say("usr_a", room, callId, "We were talking about Q3.");
    await say("usr_a", room, callId, "Krude, summarise the Q3 pricing doc");
    await waitFor(() => dispatched, (all) => all.length === 1, "the dispatch");

    const sent = dispatched[0]!;
    expect(sent).toMatchObject({ org: "org_1", room: { kind: "teamspace", id: room }, call: callId, member: "usr_a", request: "Summarise the Q3 pricing doc" });
    expect(sent.transcriptContext).toContain("Asha: We were talking about Q3.");
    await waitFor(() => live.frames, (all) => all.some((frame) => frame.type === "voice.speaking" && frame.text === "On it."), "the acknowledgement");

    const result = {
      dispatchId: sent.dispatchId, org: "org_1", room: { kind: "teamspace", id: room }, call: callId, member: "usr_a",
      threadId: "thr_side", runId: "run_1", status: "completed", text: "**Pricing** moves to per seat. Annual plans keep a discount.",
    };
    const fromGo = await call(`/v1/rooms/teamspace/${room}/voice/result`, {
      method: "POST",
      body: JSON.stringify(result),
      tokenOptions: { room: { kind: "teamspace", id: room }, member: "usr_a", scopes: ["call:speak"] },
    });
    expect(fromGo.status).toBe(202);
    expect(await fromGo.json()).toEqual({ accepted: true });
    await waitFor(
      () => live.frames,
      (all) => all.some((frame) => frame.type === "voice.speaking" && frame.text.startsWith("Pricing moves to per seat.")),
      "the spoken answer, without markdown",
    );
    const events = await log("usr_a", room);
    expect(events.some((event) => event.type === "voice.dispatch")).toBe(true);
    expect(events.find((event) => event.type === "voice.result")!.data).toMatchObject({ text: result.text, runId: "run_1" });

    // The same result again is not a second answer.
    const again = await call(`/v1/rooms/teamspace/${room}/voice/result`, {
      method: "POST",
      body: JSON.stringify(result),
      tokenOptions: { room: { kind: "teamspace", id: room }, member: "usr_a", scopes: ["call:speak"] },
    });
    expect(await again.json()).toEqual({ accepted: false });
    live.close();
  });

  it("keeps an answer found with a member's own access private when others are in the call", async () => {
    const room = await newRoom("usr_a", "usr_b");
    const { callId } = await join("usr_a", room, "Asha");
    await join("usr_b", room, "Ben");
    reply = () => ({ kind: "dispatch", request: "What's in my inbox?", acknowledgement: "Checking." });
    const asha = await listen("usr_a", room);
    const ben = await listen("usr_b", room);
    await say("usr_a", room, callId, "Krude, what's in my inbox?");
    await waitFor(() => dispatched, (all) => all.length === 1, "the dispatch");

    const result = {
      dispatchId: dispatched[0]!.dispatchId, org: "org_1", room: { kind: "teamspace", id: room }, call: callId, member: "usr_a",
      threadId: "thr_side", runId: "run_1", status: "completed", text: "Your landlord wrote about the lease.",
    };
    const fromGo = await call(`/v1/rooms/teamspace/${room}/voice/result`, {
      method: "POST",
      body: JSON.stringify(result),
      tokenOptions: { room: { kind: "teamspace", id: room }, member: "usr_a", scopes: ["call:speak"] },
    });
    expect(fromGo.status).toBe(202);
    await waitFor(() => asha.frames, (all) => all.some((frame) => frame.type === "voice.private"), "Asha's private answer");
    await waitFor(
      () => ben.frames,
      (all) => all.some((frame) => frame.type === "voice.speaking" && frame.text === "Asha, I've sent you that answer privately."),
      "the spoken pointer",
    );
    const everythingBenGot = JSON.stringify(ben.frames);
    expect(everythingBenGot).not.toContain("landlord");
    expect(JSON.stringify(await log("usr_b", room))).not.toContain("landlord");
    expect((await log("usr_a", room)).find((event) => event.type === "voice.result")).toMatchObject({ private: true });
    asha.close();
    ben.close();
  });

  it("keeps an answer private to its member even when they are alone in the call", async () => {
    const room = await newRoom("usr_a", "usr_b");
    const { callId } = await join("usr_a", room, "Asha");
    reply = () => ({ kind: "dispatch", request: "What's in my inbox from legal?", acknowledgement: "Checking." });
    const asha = await listen("usr_a", room);
    await say("usr_a", room, callId, "Krude, anything from legal in my inbox?");
    await waitFor(() => dispatched, (all) => all.length === 1, "the dispatch");
    expect(dispatched[0]!.said).toBe("Krude, anything from legal in my inbox?");
    await call(`/v1/rooms/teamspace/${room}/voice/result`, {
      method: "POST",
      body: JSON.stringify({
        dispatchId: dispatched[0]!.dispatchId, org: "org_1", room: { kind: "teamspace", id: room }, call: callId, member: "usr_a",
        threadId: "thr_side", runId: "run_1", status: "completed", text: "Legal sent the NDA redlines.",
      }),
      tokenOptions: { room: { kind: "teamspace", id: room }, member: "usr_a", scopes: ["call:speak"] },
    });
    await waitFor(() => asha.frames, (all) => all.some((frame) => frame.type === "voice.speaking" && frame.text.includes("NDA")), "the answer, spoken to Asha");
    const ashas = await waitFor(
      () => log("usr_a", room),
      (events) => events.some((event) => event.type === "transcript.segment" && event.summary.includes("NDA")),
      "the agent's private line",
    );
    expect(ashas.filter((event) => event.summary.includes("NDA")).every((event) => event.private)).toBe(true);
    // Ben is in the room but not the call: nothing of the answer reaches him.
    expect(JSON.stringify(await log("usr_b", room))).not.toContain("NDA");

    await post("usr_a", room, "/call/end");
    const saved = await waitFor(
      () => env.RUNS.get(`calls/org_1/teamspace/${room}/${callId}/transcript.ndjson`),
      (object) => object !== null,
      "the transcript in R2",
    );
    const lines = (await saved!.text()).trim().split("\n").map((line) => JSON.parse(line) as { text: string; private?: boolean; for?: string });
    expect(lines.filter((line) => line.text.includes("NDA"))).toEqual([expect.objectContaining({ private: true, for: "usr_a" })]);
  });

  it("sends a dispatch again while agents is restarting", async () => {
    const room = await newRoom("usr_a");
    const { callId } = await join("usr_a", room);
    reply = () => ({ kind: "dispatch", request: "Check my calendar", acknowledgement: "Checking." });
    let refusals = 0;
    setVoiceProviders(() => ({
      brain: { reply: async (ask) => reply(ask) },
      speech: { synthesize: async (text) => new TextEncoder().encode(text) },
      agents: {
        dispatch: async (request) => {
          if (refusals++ === 0) throw new DispatchError("agents answered 503", true);
          dispatched.push(request);
          return { dispatchId: request.dispatchId, threadId: "t", runId: "r" };
        },
      },
    }));
    await say("usr_a", room, callId, "Krude, check my calendar");
    await waitFor(() => dispatched, (all) => all.length === 1, "the dispatch, sent again");
    expect(refusals).toBe(2);
  });

  it("sends a dispatch a deploy interrupted again, once", async () => {
    const room = await newRoom("usr_a");
    const { callId } = await join("usr_a", room);
    reply = () => ({ kind: "dispatch", request: "Check my calendar", acknowledgement: "Checking." });
    await say("usr_a", room, callId, "Krude, check my calendar");
    await waitFor(() => dispatched, (all) => all.length === 1, "the dispatch");
    const interrupted = {
      dispatchId: dispatched[0]!.dispatchId, org: "org_1", room: { kind: "teamspace", id: room }, call: callId, member: "usr_a",
      threadId: "thr_side", runId: "run_1", status: "interrupted",
    };
    const post = () =>
      call(`/v1/rooms/teamspace/${room}/voice/result`, {
        method: "POST",
        body: JSON.stringify(interrupted),
        tokenOptions: { room: { kind: "teamspace", id: room }, member: "usr_a", scopes: ["call:speak"] },
      });
    expect((await post()).status).toBe(202);
    await waitFor(() => dispatched, (all) => all.length === 2, "the dispatch sent again");
    expect(dispatched[1]!.dispatchId).toBe(dispatched[0]!.dispatchId);
    await post();
    await new Promise((resolve) => setTimeout(resolve, 50));
    expect(dispatched).toHaveLength(2);
  });

  it("refuses a result without call:speak, or for another member than its token", async () => {
    const room = await newRoom("usr_a", "usr_b");
    const { callId } = await join("usr_a", room);
    const result = {
      dispatchId: "dsp_x", org: "org_1", room: { kind: "teamspace", id: room }, call: callId, member: "usr_a",
      threadId: "t", runId: "r", status: "completed", text: "hi",
    };
    const noScope = await call(`/v1/rooms/teamspace/${room}/voice/result`, {
      method: "POST",
      body: JSON.stringify(result),
      tokenOptions: { room: { kind: "teamspace", id: room }, member: "usr_a", scopes: ["log:write"] },
    });
    expect(noScope.status).toBe(403);
    const otherMember = await call(`/v1/rooms/teamspace/${room}/voice/result`, {
      method: "POST",
      body: JSON.stringify(result),
      tokenOptions: { room: { kind: "teamspace", id: room }, member: "usr_b", scopes: ["call:speak"] },
    });
    expect(otherMember.status).toBe(403);
    const asMemberToo = await post("usr_a", room, "/voice/result", result);
    expect(asMemberToo.status).toBe(403);
  });
});

describe("RealtimeKit's webhooks", () => {
  async function rsaKey(): Promise<{ pem: string; sign(body: string): Promise<string> }> {
    const pair = (await crypto.subtle.generateKey(
      { name: "RSASSA-PKCS1-v1_5", modulusLength: 2048, publicExponent: new Uint8Array([1, 0, 1]), hash: "SHA-256" },
      true,
      ["sign", "verify"],
    )) as CryptoKeyPair;
    const spki = new Uint8Array((await crypto.subtle.exportKey("spki", pair.publicKey)) as ArrayBuffer);
    return {
      pem: `-----BEGIN PUBLIC KEY-----\n${base64Encode(spki)}\n-----END PUBLIC KEY-----`,
      sign: async (body) =>
        base64Encode(new Uint8Array(await crypto.subtle.sign("RSASSA-PKCS1-v1_5", pair.privateKey, new TextEncoder().encode(body)))),
    };
  }

  it("ends the call when RealtimeKit says the meeting ended, once, and refuses a bad signature", async () => {
    const key = await rsaKey();
    const hooked = { ...env, REALTIMEKIT_WEBHOOK_PUBLIC_KEY: key.pem } as Env;
    const room = await newRoom("usr_a");
    const { callId } = await join("usr_a", room);
    const body = JSON.stringify({
      event: "meeting.ended",
      reason: "ALL_PARTICIPANTS_LEFT",
      meeting: { id: "mtg_1", title: `oryxa:org_1:teamspace:${room}:${callId}` },
    });
    const send = async (signature: string, uuid = "dlv_1") =>
      handleRealtimeKitWebhook(
        new Request("https://edge.test/v1/webhooks/realtimekit", { method: "POST", body, headers: { "rtk-signature": signature, "rtk-uuid": uuid } }),
        hooked,
      );

    expect((await send(await key.sign(`${body} `))).status).toBe(401);
    expect((await send("")).status).toBe(401);
    expect(((await (await asMember("usr_a", room, "/call")).json()) as { call: unknown }).call).not.toBeNull();

    expect((await send(await key.sign(body))).status).toBe(200);
    expect(((await (await asMember("usr_a", room, "/call")).json()) as { call: unknown }).call).toBeNull();
    expect((await send(await key.sign(body))).status).toBe(200);
    expect((await log("usr_a", room)).filter((event) => event.type === "call.ended")).toHaveLength(1);
  });

  it("finds a recording's call by its file name, whatever RealtimeKit adds to the prefix", async () => {
    const key = await rsaKey();
    const room = await newRoom("usr_a");
    const { callId } = await join("usr_a", room);
    const file = `${recordingPrefix(`oryxa:org_1:teamspace:${room}:${callId}`)}_1760430000.mp4`;
    const body = JSON.stringify({ event: "recording.statusUpdate", recording: { id: "rec_1", status: "RECORDING", outputFileName: file } });
    const response = await handleRealtimeKitWebhook(
      new Request("https://edge.test/v1/webhooks/realtimekit", {
        method: "POST",
        body,
        headers: { "rtk-signature": await key.sign(body), "rtk-uuid": "dlv_rec" },
      }),
      { ...env, REALTIMEKIT_WEBHOOK_PUBLIC_KEY: key.pem } as Env,
    );
    expect(await response.json()).toEqual({ ok: true });
  });

  it("ignores a webhook for a meeting that isn't an Oryxa call", async () => {
    const key = await rsaKey();
    const body = JSON.stringify({ event: "meeting.ended", meeting: { id: "m", title: "someone else's meeting" } });
    const response = await handleRealtimeKitWebhook(
      new Request("https://edge.test/v1/webhooks/realtimekit", {
        method: "POST",
        body,
        headers: { "rtk-signature": await key.sign(body), "rtk-uuid": "dlv_x" },
      }),
      { ...env, REALTIMEKIT_WEBHOOK_PUBLIC_KEY: key.pem } as Env,
    );
    expect(await response.json()).toEqual({ ignored: true });
  });
});

describe("the real clients, in workerd", () => {
  // The tests above fake RealtimeKit, Gemini and agents; these check that the
  // real clients' own fetch works where they run, rather than failing every
  // request with "Illegal invocation" (the global fetch called as a method).
  it("reach the network with their default fetch", async () => {
    const clients: { name: string; fetcher: (input: string) => Promise<Response> }[] = [
      { name: "RealtimeKit", ...(new RealtimeKit("acct", "app", "token") as unknown as { fetcher: (input: string) => Promise<Response> }) },
      { name: "Gemini", ...(new Gemini("key") as unknown as { fetcher: (input: string) => Promise<Response> }) },
      { name: "Agents", ...(new Agents("http://127.0.0.1:9", "{}") as unknown as { fetcher: (input: string) => Promise<Response> }) },
    ];
    for (const client of clients) {
      const holder = { fetcher: client.fetcher };
      const error = await holder.fetcher("http://127.0.0.1:9/").then(
        () => null,
        (failure: unknown) => String(failure),
      );
      expect(error ?? "", client.name).not.toMatch(/Illegal invocation/);
    }
  });

  it("create meetings RealtimeKit accepts: no transcription language beside a Whisper transcript", async () => {
    let sent: { transcribe_on_end?: boolean; ai_config?: { transcription?: Record<string, unknown> } } = {};
    const client = new RealtimeKit("acct", "app", "token", async (_input, init) => {
      sent = JSON.parse(String(init?.body));
      return new Response(JSON.stringify({ success: true, data: { id: "mtg_1" } }));
    });
    expect(await client.createMeeting("oryxa:test")).toEqual({ id: "mtg_1" });
    expect(sent.transcribe_on_end).toBe(true);
    // RealtimeKit refuses en-US for the post-meeting transcript.
    expect(sent.ai_config?.transcription).not.toHaveProperty("language");
  });

  it("say why RealtimeKit refused, whatever shape its error takes", async () => {
    for (const [body, reason] of [
      [{ success: false, error: { message: "Preset group_call_host not found" } }, "Preset group_call_host not found"],
      [{ success: false, error: "Invalid meeting id" }, "Invalid meeting id"],
      [{ success: false, errors: [{ message: "Authentication error" }] }, "Authentication error"],
    ] as const) {
      const client = new RealtimeKit("acct", "app", "token", async () => new Response(JSON.stringify(body), { status: 400 }));
      await expect(client.createMeeting("oryxa:test")).rejects.toThrow(reason);
    }
    const plain = new RealtimeKit("acct", "app", "token", async () => new Response("bad gateway", { status: 400 }));
    await expect(plain.createMeeting("oryxa:test")).rejects.toThrow("bad gateway");
  });

  it("send RealtimeKit requests to REALTIMEKIT_API_URL when it is set", async () => {
    const client = RealtimeKit.fromEnv({
      REALTIMEKIT_ACCOUNT_ID: "acct",
      REALTIMEKIT_APP_ID: "app",
      REALTIMEKIT_API_TOKEN: "token",
      REALTIMEKIT_API_URL: "http://127.0.0.1:9/accounts/",
    } as Env)!;
    await expect(client.createMeeting("oryxa:test")).rejects.toThrow(/RealtimeKit did not answer/);
  });

  it("take REALTIMEKIT_API_URL only over https or to this machine", () => {
    const real = "https://api.cloudflare.com/client/v4/accounts";
    expect(apiURL(undefined)).toBe(real);
    expect(apiURL("https://rtk.example.test/accounts")).toBe("https://rtk.example.test/accounts");
    expect(apiURL("http://127.0.0.1:8790/accounts")).toBe("http://127.0.0.1:8790/accounts");
    expect(apiURL("http://localhost:8790")).toBe("http://localhost:8790");
    expect(apiURL("http://rtk.example.test/accounts")).toBe(real);
    expect(apiURL("http://127.0.0.1.example.test")).toBe(real);
    expect(apiURL("not a url")).toBe(real);
  });
});
