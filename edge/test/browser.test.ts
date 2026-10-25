// The browser through the room (A2 + A3): the browser_* tools' calls, URL
// checks, screenshots to R2, handoff to a member and back, and who may drive
// whose browser.
import { runDurableObjectAlarm, runInDurableObject } from "cloudflare:test";
import { env, exports } from "cloudflare:workers";
import { afterEach, beforeEach, describe, expect, it } from "vitest";
import { type Event, type LogResponse, PageResponse, browserDOName } from "../src/contracts";
import { BROWSER_IDLE_MS, setDriverFactory } from "../src/browser/browser";
import { FakeDriver, browserRun, drivers } from "./fakes";
import { type TokenOptions, call } from "./tokens";

let counter = 0;
let room: { kind: "thread"; id: string };
let base: string;

function driver(session = "usr_1"): FakeDriver {
  return drivers.get(`org/org_1/room/thread/${room.id}/browser/${session}`)!;
}

function post(path: string, body: unknown = {}, options: TokenOptions = {}) {
  return call(`${base}${path}`, { method: "POST", body: JSON.stringify(body), tokenOptions: { room, ...options } });
}

const web = () => (exports as unknown as { WebEdge: Fetcher }).WebEdge;
const asMember = (member: string) => ({ "x-oryxa-member": JSON.stringify({ org: "org_1", member }) });

async function logTypes(): Promise<string[]> {
  return ((await (await call(`${base}/log`, { tokenOptions: { room } })).json()) as LogResponse).events.map((event) => event.type);
}

function browserStub(session = "usr_1") {
  return env.BROWSER_SESSION.getByName(browserDOName("org_1", room, session));
}

function roomStub() {
  return env.ROOM.getByName(`org/org_1/room/thread/${room.id}`);
}

function watch(path: string, init: RequestInit = {}, member = "usr_1") {
  return web().fetch(`https://edge.test${base}${path}`, { ...init, headers: asMember(member) });
}

const LOGIN = "https://bank.example/login";
const HOME = "https://bank.example/home";

beforeEach(() => {
  room = { kind: "thread", id: `thr_br_${++counter}` };
  base = `/v1/rooms/thread/${room.id}`;
  setDriverFactory((_env, name) => {
    let found = drivers.get(name);
    if (!found) {
      found = new FakeDriver();
      found.pages.set(LOGIN, {
        title: "Sign in",
        text: "Sign in to continue",
        elements: [
          { selector: "#email", tag: "input", type: "email" },
          { selector: "#password", tag: "input", type: "password" },
          { selector: "a.home", tag: "a", text: "Home", href: HOME },
        ],
      });
      found.pages.set(HOME, { title: "Accounts", text: "Balance: 100", elements: [] });
      drivers.set(name, found);
    }
    return found;
  });
});

afterEach(() => setDriverFactory(null));

describe("driving", () => {
  it("navigates, returns the snapshot in the contract's shape, and logs it", async () => {
    const response = await post("/browser/usr_1/navigate", { url: LOGIN });
    expect(response.status).toBe(200);
    const body = PageResponse.parse(await response.json());
    expect(body).toEqual({
      page: {
        title: "Sign in",
        url: LOGIN,
        text: "Sign in to continue",
        elements: [
          { selector: "#email", tag: "input", type: "email" },
          { selector: "#password", tag: "input", type: "password" },
          { selector: "a.home", tag: "a", text: "Home", href: HOME },
        ],
      },
      sessionRecreated: true,
    });
    const again = PageResponse.parse(await (await post("/browser/usr_1/snapshot")).json());
    expect(again.sessionRecreated).toBeUndefined();

    const events = ((await (await call(`${base}/log`, { tokenOptions: { room } })).json()) as LogResponse).events;
    expect(events.map((event) => event.type)).toEqual(["room.created", "browser.started", "browser.navigate", "browser.snapshot"]);
  });

  it("clicks and fills by selector, keeping what was typed out of the log", async () => {
    await post("/browser/usr_1/navigate", { url: LOGIN });
    const filled = await post("/browser/usr_1/fill", { selector: "#email", text: "me@example.com" });
    expect(filled.status).toBe(200);
    expect(driver().filled.get("#email")).toBe("me@example.com");
    const refused = await post("/browser/usr_1/fill", { selector: "#password", text: "hunter2" });
    expect(refused.status).toBe(400);
    expect(await refused.text()).toContain("never fill password");
    const clicked = PageResponse.parse(await (await post("/browser/usr_1/click", { selector: "a.home" })).json());
    expect(clicked.page.url).toBe(HOME);
    const events = ((await (await call(`${base}/log`, { tokenOptions: { room } })).json()) as LogResponse).events;
    expect(JSON.stringify(events)).not.toContain("me@example.com");
  });

  it("refuses URLs the tool contract refuses", async () => {
    for (const [url, status] of [
      ["javascript:alert(1)", 400],
      ["file:///etc/passwd", 400],
      ["https://user:pass@example.com/", 400],
      ["/relative", 400],
      ["http://169.254.169.254/latest/meta-data/", 403],
      ["http://localhost:8080/", 403],
    ] as const) {
      expect((await post("/browser/usr_1/navigate", { url })).status, url).toBe(status);
    }
  });

  it("stores screenshots in R2 under the run and returns them", async () => {
    await post("/browser/usr_1/navigate", { url: LOGIN });
    const shot = (await (await post("/browser/usr_1/screenshot")).json()) as { key: string; data: string; width: number };
    expect(shot.key).toMatch(new RegExp(`^runs/org_1/thread/${room.id}/run_1/media/screenshot-[0-9a-f]{16}\\.png$`));
    expect(shot.width).toBe(1440);
    const stored = new Uint8Array(await (await env.RUNS.get(shot.key))!.arrayBuffer());
    expect(btoa(String.fromCharCode(...stored))).toBe(shot.data);
  });

  it("reset closes the session and the next call starts a new one", async () => {
    await post("/browser/usr_1/navigate", { url: LOGIN });
    expect((await post("/browser/usr_1/reset")).status).toBe(200);
    expect(driver().closed).toBe(1);
    const next = PageResponse.parse(await (await post("/browser/usr_1/snapshot")).json());
    expect(next.sessionRecreated).toBe(true);
  });
});

describe("handoff", () => {
  it("hands control to a member, refuses the agent meanwhile, and resumes", async () => {
    await post("/browser/usr_1/navigate", { url: LOGIN });
    const opened = (await (await post("/browser/usr_1/handoff", { reason: "Sign in, then hand back" })).json()) as {
      handoffId: string;
      live: { url: string };
    };
    expect(opened.handoffId).toBe("hnd_fake");
    expect(opened.live.url).toContain("live.browser.run");

    const blocked = await post("/browser/usr_1/click", { selector: "a.home" });
    expect(blocked.status).toBe(409);

    const state = await web().fetch(`https://edge.test${base}/browser/usr_1/handoff`, { headers: asMember("usr_1") });
    expect(await state.json()).toMatchObject({ active: true, handoffId: "hnd_fake", reason: "Sign in, then hand back" });

    // Only a member hands back; the agent can't declare itself done.
    expect((await post("/browser/usr_1/resume", { success: true })).status).toBe(403);
    driver().url = HOME;
    const resumed = await web().fetch(`https://edge.test${base}/browser/usr_1/resume`, {
      method: "POST",
      headers: asMember("usr_1"),
      body: JSON.stringify({ success: true, note: "Signed in" }),
    });
    expect(resumed.status).toBe(200);
    expect(await resumed.json()).toMatchObject({ resumed: true, page: { url: HOME } });
    expect(driver().handoffResult).toEqual({ success: true, reason: "Signed in" });
    expect((await post("/browser/usr_1/snapshot")).status).toBe(200);

    const events = ((await (await call(`${base}/log`, { tokenOptions: { room } })).json()) as LogResponse).events;
    const resume = events.find((event: Event) => event.type === "browser.resume")!;
    expect(resume.actor).toEqual({ type: "member", id: "usr_1", onBehalfOf: "usr_1" });
  });
});

describe("who may drive whose browser", () => {
  it("keeps a browser, and its cookies, to the member who started it", async () => {
    await post("/browser/usr_1/navigate", { url: LOGIN }, { member: "usr_1" });
    expect((await post("/browser/usr_1/snapshot", {}, { member: "usr_2" })).status).toBe(403);
    expect((await post("/browser/usr_2/navigate", { url: LOGIN }, { member: "usr_2" })).status).toBe(200);
  });

  it("needs browser:drive", async () => {
    expect((await post("/browser/usr_1/navigate", { url: LOGIN }, { scopes: ["sandbox:exec"] })).status).toBe(403);
  });

  it("refuses a member driving the browser through web-next", async () => {
    await post("/browser/usr_1/navigate", { url: LOGIN });
    const response = await web().fetch(`https://edge.test${base}/browser/usr_1/navigate`, {
      method: "POST",
      headers: asMember("usr_1"),
      body: JSON.stringify({ url: HOME }),
    });
    expect(response.status).toBe(403);
  });

  it("refuses a fourth browser in one room", async () => {
    for (const member of ["m1", "m2", "m3"]) {
      expect((await post(`/browser/${member}/navigate`, { url: LOGIN }, { member })).status).toBe(200);
    }
    expect((await post(`/browser/m4/navigate`, { url: LOGIN }, { member: "m4" })).status).toBe(429);
  });
});

describe("watching and handing back (A15)", () => {
  it("lets only the browser's own member open its live view or hand it back", async () => {
    await post("/browser/usr_1/navigate", { url: LOGIN }, { member: "usr_1" });
    await post("/browser/usr_1/handoff", { reason: "Sign in" }, { member: "usr_1" });
    // usr_2 is a room member too: their agent acted here.
    await post("/browser/usr_2/navigate", { url: LOGIN }, { member: "usr_2" });

    const theirLive = await web().fetch(`https://edge.test${base}/browser/usr_1/live`, { method: "POST", headers: asMember("usr_2"), body: "{}" });
    expect(theirLive.status).toBe(403);
    const theirResume = await web().fetch(`https://edge.test${base}/browser/usr_1/resume`, {
      method: "POST",
      headers: asMember("usr_2"),
      body: JSON.stringify({ success: true }),
    });
    expect(theirResume.status).toBe(403);
    const theirState = await web().fetch(`https://edge.test${base}/browser/usr_1/handoff`, { headers: asMember("usr_2") });
    expect(theirState.status).toBe(403);
    expect(driver().handoffActive).toBe(true);

    const ownLive = await web().fetch(`https://edge.test${base}/browser/usr_1/live`, { method: "POST", headers: asMember("usr_1"), body: "{}" });
    expect(ownLive.status).toBe(200);
    const ownResume = await web().fetch(`https://edge.test${base}/browser/usr_1/resume`, {
      method: "POST",
      headers: asMember("usr_1"),
      body: JSON.stringify({ success: true }),
    });
    expect(ownResume.status).toBe(200);
  });
});

// B2: after the DO is evicted its driver has nothing attached, and closing
// must still end the Browser Run session it stored.
describe("closing after the DO restarts", () => {
  it("stop by request reconnects to the stored session and ends it", async () => {
    await post("/browser/usr_1/navigate", { url: LOGIN });
    const session = driver().sessionId!;
    driver().evict();
    expect(browserRun.has(session)).toBe(true);

    const stopped = await call(`${base}/browser/usr_1`, { method: "DELETE", tokenOptions: { room } });
    expect(stopped.status).toBe(200);
    expect(browserRun.has(session)).toBe(false);
    expect(driver().closed).toBe(1);
    expect(await logTypes()).toContain("browser.stopped");
  });

  it("the room's idle sweep ends a session the evicted DO stored", async () => {
    await post("/browser/usr_1/navigate", { url: LOGIN });
    const session = driver().sessionId!;
    driver().evict();
    await runInDurableObject(roomStub(), (_instance, state) => {
      state.storage.sql.exec("UPDATE children SET last_used_at = ?", Date.now() - BROWSER_IDLE_MS - 1);
    });
    expect(await runDurableObjectAlarm(roomStub())).toBe(true);
    expect(browserRun.has(session)).toBe(false);
    const children = (await (await call(`${base}/children`, { tokenOptions: { room } })).json()) as { children: { status: string }[] };
    expect(children.children[0]!.status).toBe("stopped");
  });

  it("reset ends the old session, and reports failure while it can't", async () => {
    await post("/browser/usr_1/navigate", { url: LOGIN });
    const session = driver().sessionId!;
    driver().evict();
    driver().failClose = true;
    expect((await post("/browser/usr_1/reset")).status).toBe(503);
    expect(browserRun.has(session)).toBe(true);

    driver().failClose = false;
    expect((await post("/browser/usr_1/reset")).status).toBe(200);
    expect(browserRun.has(session)).toBe(false);
  });

  it("retries a failed close from its own alarm", async () => {
    await post("/browser/usr_1/navigate", { url: LOGIN });
    const session = driver().sessionId!;
    driver().evict();
    driver().failClose = true;
    await call(`${base}/browser/usr_1`, { method: "DELETE", tokenOptions: { room } });
    expect(browserRun.has(session)).toBe(true);

    driver().failClose = false;
    expect(await runDurableObjectAlarm(browserStub())).toBe(true);
    expect(browserRun.has(session)).toBe(false);
  });

  it("closes a session the room never stopped once it is idle past the limit", async () => {
    await post("/browser/usr_1/navigate", { url: LOGIN });
    const session = driver().sessionId!;
    driver().evict();
    // Not idle yet: the alarm leaves it running.
    expect(await runDurableObjectAlarm(browserStub())).toBe(true);
    expect(browserRun.has(session)).toBe(true);

    await runInDurableObject(browserStub(), async (_instance, state) => {
      await state.storage.put("lastUsedAt", Date.now() - BROWSER_IDLE_MS - 3 * 60_000);
    });
    expect(await runDurableObjectAlarm(browserStub())).toBe(true);
    expect(browserRun.has(session)).toBe(false);
  });
});

// B3: watching never starts a browser.
describe("opening the live view", () => {
  it("reconnects to a running session after a restart", async () => {
    await post("/browser/usr_1/navigate", { url: LOGIN });
    driver().evict();
    const live = await watch("/browser/usr_1/live", { method: "POST", body: "{}" });
    expect(live.status).toBe(200);
    expect(driver().launched).toBe(1);
    expect(driver().reconnected).toBe(1);
  });

  it("refuses with conflict, and launches nothing, when the session has ended", async () => {
    await post("/browser/usr_1/navigate", { url: LOGIN });
    driver().expire();
    const live = await watch("/browser/usr_1/live", { method: "POST", body: "{}" });
    expect(live.status).toBe(409);
    expect(await live.text()).toContain("has stopped");
    expect(driver().launched).toBe(1);
    const types = await logTypes();
    expect(types.filter((type) => type === "browser.started")).toHaveLength(1);
    expect(types.at(-1)).toBe("browser.stopped");

    // The agent's next call still starts a fresh one.
    const next = PageResponse.parse(await (await post("/browser/usr_1/snapshot")).json());
    expect(next.sessionRecreated).toBe(true);
  });

  it("gives a control link a short life", async () => {
    await post("/browser/usr_1/navigate", { url: LOGIN });
    const tab = (await (await watch("/browser/usr_1/live", { method: "POST", body: "{}" })).json()) as { expiresAt: string };
    const full = (await (await watch("/browser/usr_1/live", { method: "POST", body: JSON.stringify({ mode: "full" }) })).json()) as { expiresAt: string };
    expect(Date.parse(full.expiresAt)).toBeLessThan(Date.parse(tab.expiresAt));
    expect(Date.parse(full.expiresAt) - Date.now()).toBeLessThanOrEqual(5 * 60_000);
  });
});

// B8: a handoff nobody answered, or on a session that ended, is over.
describe("a handoff that is over", () => {
  it("reports inactive once the session has ended", async () => {
    await post("/browser/usr_1/navigate", { url: LOGIN });
    await post("/browser/usr_1/handoff", { reason: "Sign in" });
    driver().expire();
    const state = await watch("/browser/usr_1/handoff");
    expect(await state.json()).toMatchObject({ active: false });
    expect(driver().launched).toBe(1);
  });

  it("times out, says so in the log, and gives the agent the browser back", async () => {
    await post("/browser/usr_1/navigate", { url: LOGIN });
    await post("/browser/usr_1/handoff", { reason: "Sign in", timeoutMs: 60_000 });
    await runInDurableObject(browserStub(), async (_instance, state) => {
      const handoff = (await state.storage.get<{ startedAt: number }>("handoff"))!;
      await state.storage.put("handoff", { ...handoff, startedAt: Date.now() - 61_000 });
    });
    const state = await watch("/browser/usr_1/handoff");
    expect(await state.json()).toMatchObject({ active: false });
    expect(await logTypes()).toContain("browser.handoff.timeout");
    expect((await post("/browser/usr_1/click", { selector: "a.home" })).status).toBe(200);
  });

  it("hand back succeeds when Browser Run already ended the handoff", async () => {
    await post("/browser/usr_1/navigate", { url: LOGIN });
    await post("/browser/usr_1/handoff", { reason: "Sign in" });
    driver().handoffActive = false;
    driver().evict();
    const resumed = await watch("/browser/usr_1/resume", { method: "POST", body: JSON.stringify({ success: true }) });
    expect(resumed.status).toBe(200);
    expect(driver().launched).toBe(1);
  });

  it("hand back on an ended session launches nothing", async () => {
    await post("/browser/usr_1/navigate", { url: LOGIN });
    await post("/browser/usr_1/handoff", { reason: "Sign in" });
    driver().expire();
    const resumed = await watch("/browser/usr_1/resume", { method: "POST", body: JSON.stringify({ success: true }) });
    expect(resumed.status).toBe(409);
    expect(driver().launched).toBe(1);
  });
});

// B10: a browser that can't start, or a page that can't open, says why.
describe("failures in the log", () => {
  it("logs browser.failed when the browser can't start", async () => {
    await post("/browser/usr_1/navigate", { url: LOGIN });
    driver().expire();
    driver().failLaunch = true;
    expect((await post("/browser/usr_1/snapshot")).status).toBe(503);
    expect(await logTypes()).toContain("browser.failed");
  });

  it("logs browser.failed when a page can't open", async () => {
    expect((await post("/browser/usr_1/navigate", { url: "https://nowhere.example/" })).status).toBe(400);
    expect(await logTypes()).toContain("browser.failed");
  });
});
