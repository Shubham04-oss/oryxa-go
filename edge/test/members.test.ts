// Room membership pushed from Go (A18): replace and change, who may set it,
// fail-closed checks, conversation rooms, and what a removal ends.
import { exports } from "cloudflare:workers";
import { afterEach, beforeAll, beforeEach, describe, expect, it } from "vitest";
import { directRoomId, type Event, type LogResponse, type Room } from "../src/contracts";
import { setCallProvider } from "../src/call/providers";
import type { CallProvider } from "../src/call/realtimekit";
import { setDriverFactory } from "../src/browser/browser";
import { FakeDriver } from "./fakes";
import { type TokenOptions, call } from "./tokens";

const web = () => (exports as unknown as { WebEdge: Fetcher }).WebEdge;
const as = (member: string, org = "org_1") => ({ "x-oryxa-member": JSON.stringify({ org, member }) });

class FakeRealtimeKit implements CallProvider {
  deleted: string[] = [];
  private next = 0;
  async createMeeting() {
    return { id: "mtg_1" };
  }
  async addParticipant() {
    const id = `prt_${++this.next}`;
    return { id, token: `tok_${id}` };
  }
  async deleteParticipant(_meeting: string, participant: string) {
    this.deleted.push(participant);
  }
  async endSession() {}
  async startRecording() {
    return { id: "rec_1" };
  }
  async stopRecording() {}
}

let rooms = 0;
let room: Room;
let base: string;

function push(body: unknown, options: TokenOptions = {}) {
  return call(`${base}/members`, {
    method: "PUT",
    body: JSON.stringify(body),
    tokenOptions: { room, member: "", agent: "edgemembers", run: "", scopes: ["room:members"], ...options },
  });
}

async function members(): Promise<string[]> {
  const response = await call(`${base}/members`, { tokenOptions: { room, member: "", scopes: ["room:members"] } });
  return ((await response.json()) as { members: string[] }).members;
}

function readAs(member: string) {
  return web().fetch(`https://edge.test${base}/log`, { headers: as(member) });
}

beforeAll(async () => {
  await call("/v1/rooms/teamspace/members_warmup/log", { tokenOptions: { room: { kind: "teamspace", id: "members_warmup" }, scopes: ["log:read"] } });
}, 60_000);

beforeEach(() => {
  room = { kind: "teamspace", id: `tsp_members_${++rooms}` };
  base = `/v1/rooms/${room.kind}/${room.id}`;
});

describe("setting members", () => {
  it("replaces the list, and only listed members get in", async () => {
    const response = await push({ members: ["usr_b", "usr_a"] });
    expect(response.status).toBe(200);
    expect(await response.json()).toEqual({ members: ["usr_a", "usr_b"] });
    expect((await readAs("usr_a")).status).toBe(200);
    expect((await readAs("usr_c")).status).toBe(403);

    expect(await (await push({ members: ["usr_b", "usr_c"] })).json()).toEqual({ members: ["usr_b", "usr_c"] });
    expect((await readAs("usr_a")).status).toBe(403);
    expect((await readAs("usr_c")).status).toBe(200);
  });

  it("empties the room on a replace with nobody", async () => {
    await push({ members: ["usr_a"] });
    expect(await (await push({ members: [] })).json()).toEqual({ members: [] });
    expect((await readAs("usr_a")).status).toBe(403);
  });

  it("adds and removes, and logs the change", async () => {
    await push({ members: ["usr_a"] });
    expect(await (await push({ add: ["usr_b"], remove: ["usr_a"] })).json()).toEqual({ members: ["usr_b"] });
    const log = (await (await readAs("usr_b")).json()) as LogResponse;
    const changes = log.events.filter((event: Event) => event.type === "room.members.changed");
    expect(changes.at(-1)?.data).toEqual({ added: ["usr_b"], removed: ["usr_a"] });
  });

  it("keeps a removed member out even when their agent's token names them, until Go adds them again", async () => {
    await call(`${base}/log`, { tokenOptions: { room, member: "usr_a" } });
    await push({ remove: ["usr_a"] });
    const refused = await call(`${base}/log`, { tokenOptions: { room, member: "usr_a" } });
    expect(refused.status).toBe(403);
    expect(await refused.text()).toContain("removed from this room");
    expect(await members()).not.toContain("usr_a");

    await push({ add: ["usr_a"] });
    expect((await call(`${base}/log`, { tokenOptions: { room, member: "usr_a" } })).status).toBe(200);
  });

  it("keeps out someone removed before they ever arrived", async () => {
    await push({ remove: ["usr_never"] });
    expect((await call(`${base}/log`, { tokenOptions: { room, member: "usr_never" } })).status).toBe(403);
  });

  it("refuses a token without room:members, members themselves, and a malformed body", async () => {
    expect((await push({ members: ["usr_a"] }, { scopes: ["log:write"] })).status).toBe(403);
    await push({ members: ["usr_a"] });
    const fromWeb = await web().fetch(`https://edge.test${base}/members`, {
      method: "PUT",
      headers: as("usr_a"),
      body: JSON.stringify({ add: ["usr_mallory"] }),
    });
    expect(fromWeb.status).toBe(403);
    expect((await push({ members: ["usr_a"], add: ["usr_b"] })).status).toBe(400);
    expect((await push({})).status).toBe(400);
  });

  it("is per organization", async () => {
    await push({ members: ["usr_a"] });
    expect((await web().fetch(`https://edge.test${base}/log`, { headers: as("usr_a", "org_2") })).status).toBe(404);
  });
});

describe("conversation rooms", () => {
  const a = "0a1d5f0e-2b7a-4c8e-9f3d-1a2b3c4d5e6f";
  const b = "6c1d5f0e-2b7a-4c8e-9f3d-1a2b3c4d5e6f";

  it("hold only the two members their id names", async () => {
    room = { kind: "dm", id: directRoomId(a, b) };
    base = `/v1/rooms/dm/${room.id}`;
    expect((await push({ members: [a, b] })).status).toBe(200);
    expect((await push({ add: ["usr_intruder"] })).status).toBe(400);
    const intruder = await call(`${base}/log`, { tokenOptions: { room, member: "usr_intruder" } });
    expect(intruder.status).toBe(403);
    expect((await readAs(a)).status).toBe(200);
  });
});

describe("what a removal ends", () => {
  let rtk: FakeRealtimeKit;
  let driver: FakeDriver;

  beforeEach(() => {
    rtk = new FakeRealtimeKit();
    setCallProvider(() => rtk);
    driver = new FakeDriver();
    driver.pages.set("https://example.com/", { title: "Example", text: "", elements: [] });
    setDriverFactory(() => driver);
  });

  afterEach(() => {
    setCallProvider(undefined);
    setDriverFactory(null);
  });

  it("closes their live socket", async () => {
    await push({ members: ["usr_a", "usr_b"] });
    const response = await web().fetch(`https://edge.test${base}/live`, { headers: { upgrade: "websocket", ...as("usr_a") } });
    expect(response.status).toBe(101);
    const socket = response.webSocket!;
    socket.accept();
    const closed = new Promise<number>((resolve) => socket.addEventListener("close", (event) => resolve(event.code)));
    await push({ remove: ["usr_a"] });
    expect(await closed).toBe(4403);
  });

  it("takes them out of the call, revoking their participant", async () => {
    await push({ members: ["usr_a", "usr_b"] });
    for (const member of ["usr_a", "usr_b"]) {
      const joined = await web().fetch(`https://edge.test${base}/call/join`, { method: "POST", headers: as(member), body: "{}" });
      expect(joined.status).toBe(200);
    }
    await push({ remove: ["usr_a"] });
    expect(rtk.deleted).toEqual(["prt_1"]);
    const view = (await (await web().fetch(`https://edge.test${base}/call`, { headers: as("usr_b") })).json()) as {
      call: { participants: { member: string }[] } | null;
    };
    expect(view.call?.participants.map((participant) => participant.member)).toEqual(["usr_b"]);
  });

  it("stops their browser, which holds their logins", async () => {
    await push({ members: ["usr_a"] });
    const navigated = await call(`${base}/browser/usr_a/navigate`, {
      method: "POST",
      body: JSON.stringify({ url: "https://example.com/" }),
      tokenOptions: { room, member: "usr_a" },
    });
    expect(navigated.status).toBe(200);
    await push({ remove: ["usr_a"] });
    expect(driver.closed).toBe(1);
  });
});
