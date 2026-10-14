// The browser through the room (A2 + A3): the browser_* tools' calls, URL
// checks, screenshots to R2, handoff to a member and back, and who may drive
// whose browser.
import { env, exports } from "cloudflare:workers";
import { afterEach, beforeEach, describe, expect, it } from "vitest";
import { type Event, type LogResponse, PageResponse } from "../src/contracts";
import { setDriverFactory } from "../src/browser/browser";
import { FakeDriver, drivers } from "./fakes";
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
