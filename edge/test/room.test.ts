// The room through the Worker: tokens, scopes, replay, the log, privacy,
// org isolation, live subscribers and the flush to R2.
import { env, exports } from "cloudflare:workers";
import { runDurableObjectAlarm, runInDurableObject } from "cloudflare:test";
import { describe, expect, it } from "vitest";
import type { Event, LogResponse } from "../src/contracts";
import type { RoomDO } from "../src/room/room";
import { call, claims, signClaims, token } from "./tokens";

const ROOM = "/v1/rooms/thread/thr_1";

async function readLog(path = ROOM, options = {}): Promise<LogResponse> {
  const response = await call(`${path}/log`, { tokenOptions: options });
  expect(response.status).toBe(200);
  return response.json();
}

async function appendEvent(path = ROOM, options = {}, type = "run.started"): Promise<Response> {
  return call(`${path}/log`, {
    method: "POST",
    body: JSON.stringify({ type, summary: `${type} summary`, data: { n: 1 } }),
    tokenOptions: options,
  });
}

function roomStub(org = "org_1", kind = "thread", id = "thr_1") {
  return env.ROOM.getByName(`org/${org}/room/${kind}/${id}`);
}

describe("authentication", () => {
  it("refuses a request without a token", async () => {
    const response = await (exports as unknown as { default: Fetcher }).default.fetch(`https://edge.test${ROOM}/log`);
    expect(response.status).toBe(401);
    expect(await response.json()).toMatchObject({ error: { code: "unauthorized" } });
  });

  it("refuses a token signed by a key it doesn't trust", async () => {
    const other = (await crypto.subtle.generateKey({ name: "ECDSA", namedCurve: "P-256" }, true, ["sign"])) as CryptoKeyPair;
    const good = await token();
    const [header, payload] = good.split(".");
    const signature = new Uint8Array(
      await crypto.subtle.sign({ name: "ECDSA", hash: "SHA-256" }, other.privateKey, new TextEncoder().encode(`${header}.${payload}`)),
    );
    const forged = `${header}.${payload}.${btoa(String.fromCharCode(...signature)).replace(/\+/g, "-").replace(/\//g, "_").replace(/=+$/, "")}`;
    expect((await call(`${ROOM}/log`, { token: forged })).status).toBe(401);
  });

  it("refuses an expired token", async () => {
    const now = Math.floor(Date.now() / 1000);
    const response = await call(`${ROOM}/log`, { tokenOptions: { iat: now - 200, exp: now - 100 } });
    expect(response.status).toBe(401);
    expect(((await response.json()) as { error: { message: string } }).error.message).toContain("expired");
  });

  it("refuses a token for another room in the same org", async () => {
    const response = await call(`/v1/rooms/thread/thr_2/log`);
    expect(response.status).toBe(403);
  });

  it("refuses a token meant for agents, not the edge", async () => {
    expect((await call(`${ROOM}/log`, { tokenOptions: { aud: "agents" } })).status).toBe(401);
  });

  it("refuses a replayed token, and logs it", async () => {
    const once = await token({ room: { kind: "thread", id: "thr_replay" } });
    expect((await call(`/v1/rooms/thread/thr_replay/log`, { token: once })).status).toBe(200);
    const again = await call(`/v1/rooms/thread/thr_replay/log`, { token: once });
    expect(again.status).toBe(403);
    expect(await again.json()).toMatchObject({ error: { code: "forbidden", message: "token already used" } });
    const log = await readLog("/v1/rooms/thread/thr_replay", { room: { kind: "thread", id: "thr_replay" } });
    expect(log.events.map((event) => event.type)).toContain("access.denied");
  });

  it("refuses a token without the scope a route needs", async () => {
    expect((await call(`${ROOM}/log`, { tokenOptions: { scopes: ["sandbox:exec"] } })).status).toBe(403);
    expect((await appendEvent(ROOM, { scopes: ["log:read"] })).status).toBe(403);
  });

  it("refuses a guessed path, an unknown kind and an id with a separator", async () => {
    expect((await call(`/v1/rooms/lobby/x/log`)).status).toBe(404);
    expect((await call(`/v1/rooms/thread/a%2Fb/log`)).status).toBe(404);
    expect((await call(`/v1/other`)).status).toBe(404);
  });
});

describe("the log", () => {
  it("starts with room.created and numbers what agents append", async () => {
    const room = { kind: "thread" as const, id: "thr_log" };
    const path = "/v1/rooms/thread/thr_log";
    const appended = await appendEvent(path, { room });
    expect(appended.status).toBe(201);
    const log = await readLog(path, { room });
    expect(log.events.map((event) => event.type)).toEqual(["room.created", "run.started"]);
    const event = log.events[1]!;
    expect(event).toMatchObject({
      org: "org_1",
      room,
      actor: { type: "agent", id: "agt_1", onBehalfOf: "usr_1" },
      run: "run_1",
      summary: "run.started summary",
      data: { n: 1 },
      private: false,
    });
    expect(event.seq).toBeGreaterThan(log.events[0]!.seq);
    expect(log.next).toBe(event.seq);
    const after = await call(`${path}/log?after=${event.seq}`, { tokenOptions: { room } });
    expect(((await after.json()) as LogResponse).events).toEqual([]);
  });

  it("refuses an event type that isn't a dotted name", async () => {
    expect((await appendEvent(ROOM, {}, "Not A Type")).status).toBe(400);
  });

  it("shows a private event only to its member", async () => {
    const room = { kind: "teamspace" as const, id: "rm_private" };
    const path = "/v1/rooms/teamspace/rm_private";
    await appendEvent(path, { room, member: "usr_a", private: true }, "sidekick.note");
    const theirs = await readLog(path, { room, member: "usr_a" });
    const others = await readLog(path, { room, member: "usr_b" });
    expect(theirs.events.map((event) => event.type)).toContain("sidekick.note");
    expect(others.events.map((event) => event.type)).not.toContain("sidekick.note");
  });

  it("keeps organizations apart even for the same room id", async () => {
    await appendEvent(ROOM, { org: "org_iso_a" }, "secret.thing");
    const other = await readLog(ROOM, { org: "org_iso_b" });
    expect(other.events.map((event) => event.type)).not.toContain("secret.thing");
    expect(other.events.every((event) => event.org === "org_iso_b")).toBe(true);
  });
});

describe("R2", () => {
  it("flushes events per run and writes each run's manifest", async () => {
    const room = { kind: "teamspace" as const, id: "dm_flush" };
    const path = "/v1/rooms/teamspace/dm_flush";
    await appendEvent(path, { room, run: "run_a" }, "run.started");
    await appendEvent(path, { room, run: "run_a" }, "run.completed");
    await appendEvent(path, { room, run: "run_b" }, "run.started");
    expect(await runDurableObjectAlarm(roomStub("org_1", "teamspace", "dm_flush"))).toBe(true);

    const runA = await env.RUNS.list({ prefix: "runs/org_1/teamspace/dm_flush/run_a/" });
    const keys = runA.objects.map((object) => object.key);
    expect(keys).toContain("runs/org_1/teamspace/dm_flush/run_a/manifest.json");
    const eventsKey = keys.find((key) => key.includes("/events/"))!;
    const lines = (await (await env.RUNS.get(eventsKey))!.text()).trim().split("\n");
    expect(lines.map((line) => (JSON.parse(line) as Event).type)).toEqual(["run.started", "run.completed"]);
    const manifest = await (await env.RUNS.get("runs/org_1/teamspace/dm_flush/run_a/manifest.json"))!.json<Record<string, unknown>>();
    expect(manifest).toMatchObject({ org: "org_1", run: "run_a", eventCount: 2, status: "completed" });
    expect((await env.RUNS.list({ prefix: "runs/org_1/teamspace/dm_flush/_room/" })).objects.length).toBeGreaterThan(0);
    expect((await env.RUNS.list({ prefix: "runs/org_1/teamspace/dm_flush/run_b/" })).objects.length).toBe(2);
  });

  it("keeps events it couldn't write, and writes them next time", async () => {
    const room = { kind: "side" as const, id: "side_fail" };
    await appendEvent("/v1/rooms/side/side_fail", { room, run: "run_f" });
    const stub = roomStub("org_1", "side", "side_fail");
    await runInDurableObject(stub, async (instance: RoomDO) => {
      const target = instance as unknown as { env: Cloudflare.Env };
      const real = target.env;
      target.env = { ...real, RUNS: { put: () => Promise.reject(new Error("R2 down")) } as unknown as R2Bucket };
      await expect(instance.flush()).rejects.toThrow("R2 down");
      target.env = real;
    });
    await runDurableObjectAlarm(stub);
    const written = await env.RUNS.list({ prefix: "runs/org_1/side/side_fail/run_f/events/" });
    expect(written.objects.length).toBe(1);
  });
});

describe("web-next", () => {
  const web = (exports as unknown as { WebEdge: Fetcher }).WebEdge;

  it("lets a member read their room, and refuses everyone else", async () => {
    const room = { kind: "thread" as const, id: "thr_web" };
    await appendEvent("/v1/rooms/thread/thr_web", { room, member: "usr_owner" });
    const asMember = await web.fetch("https://edge.test/v1/rooms/thread/thr_web/log", {
      headers: { "x-oryxa-member": JSON.stringify({ org: "org_1", member: "usr_owner" }) },
    });
    expect(asMember.status).toBe(200);
    const stranger = await web.fetch("https://edge.test/v1/rooms/thread/thr_web/log", {
      headers: { "x-oryxa-member": JSON.stringify({ org: "org_1", member: "usr_stranger" }) },
    });
    expect(stranger.status).toBe(403);
    const otherOrg = await web.fetch("https://edge.test/v1/rooms/thread/thr_web/log", {
      headers: { "x-oryxa-member": JSON.stringify({ org: "org_2", member: "usr_owner" }) },
    });
    expect(otherOrg.status).toBe(404);
    const noIdentity = await web.fetch("https://edge.test/v1/rooms/thread/thr_web/log");
    expect(noIdentity.status).toBe(401);
  });

  it("refuses a member writing the log as if they were an agent", async () => {
    const response = await web.fetch("https://edge.test/v1/rooms/thread/thr_web/log", {
      method: "POST",
      headers: { "x-oryxa-member": JSON.stringify({ org: "org_1", member: "usr_owner" }) },
      body: JSON.stringify({ type: "x.y", summary: "s" }),
    });
    expect(response.status).toBe(403);
  });

  it("streams new events to a subscribed member", async () => {
    const room = { kind: "thread" as const, id: "thr_live" };
    await appendEvent("/v1/rooms/thread/thr_live", { room, member: "usr_live" }, "run.started");
    const response = await web.fetch("https://edge.test/v1/rooms/thread/thr_live/live", {
      headers: { upgrade: "websocket", "x-oryxa-member": JSON.stringify({ org: "org_1", member: "usr_live" }) },
    });
    expect(response.status).toBe(101);
    const socket = response.webSocket!;
    socket.accept();
    const received = new Promise<{ type: string; event: Event }>((resolve) =>
      socket.addEventListener("message", (message) => resolve(JSON.parse(message.data as string))),
    );
    await appendEvent("/v1/rooms/thread/thr_live", { room, member: "usr_live" }, "sandbox.exec");
    expect((await received).event.type).toBe("sandbox.exec");
    socket.close();
  });
});

describe("a token signed for one org", () => {
  it("never reaches another org's room, whatever the path", async () => {
    const forOrgA = await signClaims(claims({ org: "org_a" }));
    // The org comes from the token, so this reaches org_a's thr_1 only.
    const response = await call(ROOM + "/log", { token: forOrgA });
    const log = (await response.json()) as LogResponse;
    expect(log.events.every((event) => event.org === "org_a")).toBe(true);
  });
});

describe("reading the log back (A13)", () => {
  async function pageAll(path: string, options: Record<string, unknown>, query = "") {
    const seen: Event[] = [];
    let after = 0;
    for (let page = 0; page < 50; page++) {
      const response = await call(`${path}/log?after=${after}&limit=7${query}`, { tokenOptions: options });
      const body = (await response.json()) as LogResponse;
      seen.push(...body.events);
      after = body.next;
      if (!body.more) break;
    }
    return seen;
  }

  it("reads the room's own events with run=_room", async () => {
    const room = { kind: "teamspace" as const, id: "rm_room_run" };
    const path = "/v1/rooms/teamspace/rm_room_run";
    await appendEvent(path, { room, run: "run_x" }, "run.started");
    const body = (await (await call(`${path}/log?run=_room`, { tokenOptions: { room } })).json()) as LogResponse;
    expect(body.events.map((event) => event.type)).toEqual(["room.created"]);
    expect(body.events.every((event) => event.run === null)).toBe(true);
  });

  it("pages through events the flush moved to R2 as if they were still here", async () => {
    const room = { kind: "teamspace" as const, id: "rm_archive" };
    const path = "/v1/rooms/teamspace/rm_archive";
    for (let index = 0; index < 24; index++) {
      const run = index % 3 === 0 ? "run_b" : "run_a";
      const isPrivate = index === 5;
      await appendEvent(path, { room, run, member: isPrivate ? "usr_secret" : "usr_1", private: isPrivate }, `step.n${index}`);
    }
    const stub = roomStub("org_1", "teamspace", "rm_archive");
    await runInDurableObject(stub, (instance: RoomDO) => {
      instance.hotEvents = 4;
    });
    await runDurableObjectAlarm(stub);
    await runInDurableObject(stub, (instance: RoomDO, state) => {
      instance.hotEvents = 4;
      expect(state.storage.sql.exec("SELECT COUNT(*) AS n FROM events").one().n).toBeLessThanOrEqual(5);
    });

    const all = await pageAll(path, { room });
    const seqs = all.map((event) => event.seq);
    expect(seqs).toEqual([...seqs].sort((a, b) => a - b));
    expect(new Set(seqs).size).toBe(seqs.length);
    // room.created, then 24 steps less the one private to someone else.
    expect(all.map((event) => event.type)).toEqual(["room.created", ...Array.from({ length: 24 }, (_, i) => `step.n${i}`).filter((type) => type !== "step.n5")]);

    const theirs = await pageAll(path, { room, member: "usr_secret" });
    expect(theirs.map((event) => event.type)).toContain("step.n5");

    const runB = await pageAll(path, { room }, "&run=run_b");
    expect(runB.map((event) => event.type)).toEqual(["step.n0", "step.n3", "step.n6", "step.n9", "step.n12", "step.n15", "step.n18", "step.n21"]);
  });
});
