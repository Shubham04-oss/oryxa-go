// The sandbox through the room (A1 + A3): every call the Go tools make, what
// the room logs and stores in R2, who may reach which sandbox, limits, idle
// stop and restore, previews, and egress filtering.
import { env, exports } from "cloudflare:workers";
import { runDurableObjectAlarm } from "cloudflare:test";
import { afterEach, beforeEach, describe, expect, it } from "vitest";
import type { Event, ExecResult, LogResponse } from "../src/contracts";
import { blockedTarget } from "../src/sandbox/egress";
import { BACKUPS_PREFIX, backupPrefix, setWorkspaceFactory } from "../src/sandbox/sandbox";
import { FakeWorkspace, workspaces } from "./fakes";
import { type TokenOptions, call } from "./tokens";

let roomCounter = 0;
let room: { kind: "thread"; id: string };
let base: string;

function workspace(name = "shared"): FakeWorkspace {
  const found = workspaces.get(`org_1/thread/${room.id}/${name}`);
  if (!found) throw new Error(`no workspace for ${name}`);
  return found;
}

function post(path: string, body: unknown, options: TokenOptions = {}) {
  return call(`${base}${path}`, { method: "POST", body: JSON.stringify(body), tokenOptions: { room, ...options } });
}

function get(path: string, options: TokenOptions = {}) {
  return call(`${base}${path}`, { tokenOptions: { room, ...options } });
}

async function log(options: TokenOptions = {}): Promise<Event[]> {
  return ((await (await get("/log", options)).json()) as LogResponse).events;
}

beforeEach(() => {
  room = { kind: "thread", id: `thr_sbx_${++roomCounter}` };
  base = `/v1/rooms/thread/${room.id}`;
  setWorkspaceFactory((_sandbox, _env, identity) => {
    const key = `${identity.org}/${identity.room.kind}/${identity.room.id}/${identity.name}`;
    let found = workspaces.get(key);
    if (!found) {
      found = new FakeWorkspace();
      workspaces.set(key, found);
    }
    return found;
  });
});

afterEach(() => setWorkspaceFactory(null));

describe("exec", () => {
  it("runs code, says the sandbox started, and logs it", async () => {
    const response = await post("/sandbox/shared/exec/code", { language: "python", code: "print(1 + 1)" });
    expect(response.status).toBe(200);
    const result = (await response.json()) as ExecResult;
    expect(result.stdout).toContain("print(1 + 1)");
    expect(result.exitCode).toBe(0);
    expect(result.sessionRecreated).toBe(true);
    const again = (await (await post("/sandbox/shared/exec/code", { language: "python", code: "x" })).json()) as ExecResult;
    expect(again.sessionRecreated).toBeUndefined();
    expect(workspace().starts).toBe(1);

    const events = await log();
    const types = events.map((event) => event.type);
    expect(types).toEqual(["room.created", "sandbox.started", "sandbox.exec", "sandbox.exec"]);
    expect(events[2]).toMatchObject({
      actor: { type: "agent", id: "agt_1", onBehalfOf: "usr_1" },
      run: "run_1",
      data: { language: "python", exitCode: 0, child: "shared" },
    });
  });

  it("runs each language in its kernel, so state lasts between cells", async () => {
    await post("/sandbox/shared/exec/code", { language: "python", code: "x = 41" });
    await post("/sandbox/shared/exec/code", { language: "python", code: "x + 1" });
    await post("/sandbox/shared/exec/code", { language: "typescript", code: "const n: number = 1" });
    expect(workspace().kernels.get(8899)!.cells.map((cell) => cell.code)).toEqual(["x = 41", "x + 1"]);
    expect(workspace().kernels.get(8898)!.cells).toEqual([{ code: "const n: number = 1", language: "typescript" }]);
    expect([...workspace().files.keys()].filter((path) => path.includes(".oryxa"))).toEqual([]);
  });

  it("resets a kernel when the call clears its context", async () => {
    await post("/sandbox/shared/exec/code", { language: "python", code: "x = 1" });
    await post("/sandbox/shared/exec/code", { language: "python", code: "x", clearContext: true });
    expect(workspace().kernels.get(8899)!.resets).toBe(1);
    const events = await log();
    expect(events.filter((event) => event.type === "sandbox.exec").at(-1)?.data).toMatchObject({ contextCleared: true });
  });

  it("says the session was recreated when the kernel restarted since its last cell", async () => {
    const first = (await (await post("/sandbox/shared/exec/code", { language: "python", code: "x = 1" })).json()) as ExecResult;
    expect(first.sessionRecreated).toBe(true);
    const second = (await (await post("/sandbox/shared/exec/code", { language: "python", code: "x" })).json()) as ExecResult;
    expect(second.sessionRecreated).toBeUndefined();
    workspace().kernels.get(8899)!.pid = 101;
    const third = (await (await post("/sandbox/shared/exec/code", { language: "python", code: "x" })).json()) as ExecResult;
    expect(third.sessionRecreated).toBe(true);
  });

  it("kills a kernel a cell wedged, so the next cell gets a fresh one", async () => {
    await post("/sandbox/shared/exec/code", { language: "javascript", code: "const kept = 1" });
    const stuck = (await (await post("/sandbox/shared/exec/code", { language: "javascript", code: "await null; while (true) {}", timeoutMs: 1_000 })).json()) as ExecResult;
    expect(stuck).toMatchObject({ timedOut: true, exitCode: -1 });
    expect(workspace().kernels.get(8898)!.kills).toBe(1);
    const next = (await (await post("/sandbox/shared/exec/code", { language: "javascript", code: "1 + 1" })).json()) as ExecResult;
    expect(next.exitCode).toBe(0);
    expect(next.sessionRecreated).toBe(true);
  }, 20_000);

  it("kills a kernel that won't answer its health check rather than wait on it", async () => {
    await post("/sandbox/shared/exec/code", { language: "python", code: "x = 1" });
    workspace().kernels.get(8899)!.wedged = true;
    const result = (await (await post("/sandbox/shared/exec/code", { language: "python", code: "x" })).json()) as ExecResult;
    expect(workspace().kernels.get(8899)!.kills).toBe(1);
    expect(result.sessionRecreated).toBe(true);
  }, 20_000);

  it("refuses a preview on a kernel's port", async () => {
    expect((await post("/sandbox/shared/preview", { port: 8899 })).status).toBe(400);
  });

  it("reports files a run changed, logs them, and returns small images as results", async () => {
    await post("/sandbox/shared/exec/command", { command: "true" });
    workspace().kernelRun = (_body, ws) => {
      ws.write("/workspace/chart.png", new Uint8Array([0x89, 0x50, 0x4e, 0x47]));
      ws.write("/workspace/out.csv", "a,b\n");
      return { stdout: "", stderr: "", exitCode: 0, truncated: false, timedOut: false };
    };
    const result = (await (await post("/sandbox/shared/exec/code", { language: "python", code: "plot()" })).json()) as ExecResult;
    expect(result.changedFiles).toEqual(["/workspace/chart.png", "/workspace/out.csv"]);
    expect(result.results).toEqual([{ type: "image", mimeType: "image/png", data: "iVBORw==", name: "chart.png" }]);
    const changed = (await log()).find((event) => event.type === "sandbox.file.changed")!;
    expect(changed.data).toMatchObject({ paths: ["/workspace/chart.png", "/workspace/out.csv"] });
  });

  it("reports a timeout with exit code -1", async () => {
    await post("/sandbox/shared/exec/command", { command: "true" });
    workspace().handler = (_argv, options) => {
      expect(options.timeoutMs).toBe(2_000);
      return { stdout: "partial", stderr: "", exitCode: -1, truncated: false, timedOut: true };
    };
    const result = (await (await post("/sandbox/shared/exec/command", { command: "sleep 99", timeoutMs: 2_000 })).json()) as ExecResult;
    expect(result).toMatchObject({ timedOut: true, exitCode: -1, stdout: "partial" });
  });

  it("refuses an unknown language and an oversized command", async () => {
    expect((await post("/sandbox/shared/exec/code", { language: "ruby", code: "1" })).status).toBe(400);
    expect((await post("/sandbox/shared/exec/command", { command: "x".repeat(70_000) })).status).toBe(413);
  });

  it("answers retryable when the container won't start", async () => {
    setWorkspaceFactory(() => {
      const broken = new FakeWorkspace();
      broken.start = () => Promise.reject(new Error("no capacity"));
      return broken;
    });
    const response = await post("/sandbox/shared/exec/command", { command: "true" });
    expect(response.status).toBe(503);
    expect(await response.json()).toMatchObject({ error: { code: "unavailable", retryable: true } });
  });
});

describe("files", () => {
  it("writes, lists and reads text and binary, and reports a missing file in place", async () => {
    const written = await post("/sandbox/shared/files/write", {
      files: [
        { path: "main.py", content: "print(1)\n" },
        { path: "/workspace/img/logo.png", content: "iVBORw0KGgo=", encoding: "base64" },
      ],
    });
    expect(await written.json()).toEqual({ written: [{ path: "/workspace/main.py", size: 9 }, { path: "/workspace/img/logo.png", size: 8 }] });

    const listed = await (await get("/sandbox/shared/files")).json();
    expect(listed).toEqual({
      path: "/workspace",
      entries: [
        { name: "main.py", type: "file", size: 9, path: "/workspace/main.py" },
        { name: "img", type: "directory", path: "/workspace/img" },
      ],
    });

    const read = await (await post("/sandbox/shared/files/read", { paths: ["main.py", "img/logo.png", "nope.txt"] })).json();
    expect(read).toEqual({
      files: [
        { path: "/workspace/main.py", encoding: "utf8", content: "print(1)\n", mimeType: "text/x-python", size: 9 },
        { path: "/workspace/img/logo.png", encoding: "base64", content: "iVBORw0KGgo=", mimeType: "image/png", size: 8 },
        { path: "/workspace/nope.txt", size: 0, error: "ENOENT: /workspace/nope.txt: no such file or directory" },
      ],
    });
  });

  it("caps what one read returns", async () => {
    await post("/sandbox/shared/files/write", { files: [{ path: "big.txt", content: "a".repeat(300_000) }] });
    const read = (await (await post("/sandbox/shared/files/read", { paths: ["big.txt"] })).json()) as { files: { content: string; truncated?: boolean; size: number }[] };
    expect(read.files[0]!.content.length).toBe(256 << 10);
    expect(read.files[0]).toMatchObject({ truncated: true, size: 300_000 });
  });

  it("reads a whole file when the caller raises the budget for an artifact", async () => {
    await post("/sandbox/shared/files/write", { files: [{ path: "deck.txt", content: "a".repeat(300_000) }] });
    const read = (await (await post("/sandbox/shared/files/read", { paths: ["deck.txt"], maxBytes: 25 << 20 })).json()) as { files: { content: string; truncated?: boolean }[] };
    expect(read.files[0]!.content.length).toBe(300_000);
    expect(read.files[0]!.truncated).toBeUndefined();
  });

  it("refuses a read budget past the artifact cap", async () => {
    const response = await post("/sandbox/shared/files/read", { paths: ["deck.txt"], maxBytes: (64 << 20) + 1 });
    expect(response.status).toBe(400);
  });

  it("resolves .. inside the sandbox and refuses removing the workspace itself", async () => {
    await post("/sandbox/shared/files/write", { files: [{ path: "a/../b.txt", content: "x" }] });
    expect(workspace().files.has("/workspace/b.txt")).toBe(true);
    expect((await post("/sandbox/shared/files/remove", { paths: ["/workspace"] })).status).toBe(400);
    expect((await post("/sandbox/shared/files/remove", { paths: ["b.txt"] })).status).toBe(200);
    expect(workspace().files.has("/workspace/b.txt")).toBe(false);
    expect((await post("/sandbox/shared/files/remove", { paths: ["b.txt"] })).status).toBe(404);
  });

  it("needs sandbox:files", async () => {
    expect((await get("/sandbox/shared/files", { scopes: ["sandbox:exec"] })).status).toBe(403);
  });
});

describe("artifacts", () => {
  it("copies a file to R2 under the run, with its hash", async () => {
    await post("/sandbox/shared/files/write", { files: [{ path: "report.csv", content: "a,b\n1,2\n" }] });
    const response = await post("/sandbox/shared/artifact", { path: "report.csv" });
    expect(response.status).toBe(200);
    const saved = (await response.json()) as { key: string; sha256: string; size: number; mimeType: string };
    expect(saved).toMatchObject({ key: `runs/org_1/thread/${room.id}/run_1/artifacts/report.csv`, size: 8, mimeType: "text/csv", name: "report.csv" });
    const digest = new Uint8Array(await crypto.subtle.digest("SHA-256", new TextEncoder().encode("a,b\n1,2\n")));
    expect(saved.sha256).toBe([...digest].map((byte) => byte.toString(16).padStart(2, "0")).join(""));
    expect(await (await env.RUNS.get(saved.key))!.text()).toBe("a,b\n1,2\n");
  });

  it("refuses a name that could leave the run's prefix", async () => {
    await post("/sandbox/shared/files/write", { files: [{ path: "x.txt", content: "x" }] });
    expect((await post("/sandbox/shared/artifact", { path: "x.txt", name: "../../other/x.txt" })).status).toBe(400);
  });
});

describe("previews", () => {
  it("strips headers that would reach past the preview, and answers 502 when nothing listens", async () => {
    await post("/sandbox/shared/exec/command", { command: "true" });
    workspace().ports.set(
      3000,
      () =>
        new Response("<script>navigator.serviceWorker.register('/sw.js',{scope:'/'})</script>", {
          headers: { "content-type": "text/html", "set-cookie": "session=stolen; Domain=.oryxa.in", "service-worker-allowed": "/", "x-kept": "yes" },
        }),
    );
    const signed = (await (await post("/sandbox/shared/preview", { port: 3000 })).json()) as { url: string };
    const worker = (exports as unknown as { default: Fetcher }).default;
    const page = await worker.fetch(signed.url);
    expect(page.status).toBe(200);
    expect(page.headers.get("set-cookie")).toBeNull();
    expect(page.headers.get("service-worker-allowed")).toBeNull();
    expect(page.headers.get("x-kept")).toBe("yes");
    await page.text();

    workspace().ports.set(4000, () => {
      throw new Error("The container is not listening in the TCP address 10.0.0.1:4000");
    });
    const dead = (await (await post("/sandbox/shared/preview", { port: 4000 })).json()) as { url: string };
    const answer = await worker.fetch(dead.url);
    expect(answer.status).toBe(502);
    expect(await answer.text()).toContain("port 4000");
  });

  it("signs a URL that reaches the sandbox port, and refuses a tampered one", async () => {
    await post("/sandbox/shared/exec/command", { command: "python3 -m http.server 8080 &" });
    workspace().ports.set(8080, (request) => new Response(`hello from ${new URL(request.url).pathname}`));
    const signed = (await (await post("/sandbox/shared/preview", { port: 8080 })).json()) as { url: string; expiresAt: string };
    expect(signed.url).toMatch(/^https:\/\/edge\.test\/p\/[^/]+\/$/);
    const worker = (exports as unknown as { default: Fetcher }).default;
    const page = await worker.fetch(`${signed.url}index.html`);
    expect(await page.text()).toBe("hello from /index.html");

    const token = signed.url.split("/p/")[1]!.slice(0, -1);
    const [payload, signature] = token.split(".");
    const tampered = `https://edge.test/p/${payload}.${signature!.slice(0, -2)}AA/`;
    expect((await worker.fetch(tampered)).status).toBe(403);
    const asTerminal = await worker.fetch(`https://edge.test/t/${token}`, { headers: { upgrade: "websocket" } });
    expect(asTerminal.status).toBe(403);
  });
});

describe("lifecycle", () => {
  it("stops an idle sandbox with its files saved, and restores them on the next call", async () => {
    await post("/sandbox/shared/files/write", { files: [{ path: "keep.txt", content: "kept" }] });
    const stub = env.ROOM.getByName(`org/org_1/room/thread/${room.id}`);
    const past = Date.now() - 16 * 60 * 1000;
    await (await import("cloudflare:test")).runInDurableObject(stub, async (_instance, state) => {
      state.storage.sql.exec("UPDATE children SET last_used_at = ?", past);
    });
    expect(await runDurableObjectAlarm(stub)).toBe(true);
    expect(workspace().stops).toBe(1);
    expect(workspace().files.size).toBe(0);
    const children = (await (await get("/children")).json()) as { children: { status: string }[] };
    expect(children.children[0]!.status).toBe("stopped");

    const read = (await (await post("/sandbox/shared/files/read", { paths: ["keep.txt"] })).json()) as { files: { content: string }[] };
    expect(read.files[0]!.content).toBe("kept");
    expect(workspace().restored.length).toBe(1);
    const types = (await log()).map((event) => event.type);
    expect(types).toContain("sandbox.stopped");
    expect(types.filter((type) => type === "sandbox.started").length).toBe(2);
  });

  it("reset clears files and their backups", async () => {
    await post("/sandbox/shared/files/write", { files: [{ path: "a.txt", content: "a" }] });
    await post("/sandbox/shared/snapshot", {});
    expect((await post("/sandbox/shared/reset", {})).status).toBe(200);
    expect(workspace().running()).toBe(false);
    const read = (await (await post("/sandbox/shared/files/read", { paths: ["a.txt"] })).json()) as { files: { error?: string }[] };
    expect(read.files[0]!.error).toContain("ENOENT");
    expect(workspace().restored.length).toBe(0);
  });

  it("keeps backups under a fixed prefix a lifecycle rule can expire, and reset clears them (A17)", async () => {
    const prefix = backupPrefix({ org: "org_1", room, name: "shared" });
    expect(prefix).toBe(`backups/sandbox/org_1/thread/${room.id}/shared/`);
    expect(prefix.startsWith(BACKUPS_PREFIX)).toBe(true);
    await env.RUNS.put(`${prefix}0000-backup.tar.zst`, "backup");
    await env.RUNS.put(`${BACKUPS_PREFIX}org_1/thread/${room.id}/other/keep.tar.zst`, "other sandbox");
    await post("/sandbox/shared/exec/command", { command: "true" });
    expect((await post("/sandbox/shared/reset", {})).status).toBe(200);
    expect((await env.RUNS.list({ prefix })).objects).toEqual([]);
    expect((await env.RUNS.list({ prefix: `${BACKUPS_PREFIX}org_1/thread/${room.id}/other/` })).objects.length).toBe(1);
  });

  it("stops a sandbox on DELETE", async () => {
    await post("/sandbox/shared/exec/command", { command: "true" });
    const response = await call(`${base}/sandbox/shared`, { method: "DELETE", tokenOptions: { room } });
    expect(response.status).toBe(200);
    expect(workspace().running()).toBe(false);
  });
});

describe("who may reach which sandbox", () => {
  it("keeps a private sandbox to its member and a private token", async () => {
    const mine = { member: "usr_a", private: true };
    expect((await post("/sandbox/private-usr_a/exec/command", { command: "true" }, mine)).status).toBe(200);
    expect((await post("/sandbox/private-usr_a/exec/command", { command: "true" }, { member: "usr_b", private: true })).status).toBe(403);
    expect((await post("/sandbox/private-usr_a/exec/command", { command: "true" }, { member: "usr_a" })).status).toBe(403);
    // A private token reaches nothing shared.
    expect((await post("/sandbox/shared/exec/command", { command: "true" }, mine)).status).toBe(403);
    // Its work is invisible to the rest of the room.
    const others = await log({ member: "usr_b" });
    expect(others.some((event) => event.data["child"] === "private-usr_a")).toBe(false);
    const theirs = await log({ member: "usr_a" });
    expect(theirs.some((event) => event.data["child"] === "private-usr_a")).toBe(true);
    const listed = (await (await get("/children", { member: "usr_b" })).json()) as { children: unknown[] };
    expect(listed.children).toEqual([]);
  });

  it("keeps a run's sandbox to that run", async () => {
    expect((await post("/sandbox/run_9/exec/command", { command: "true" }, { run: "run_9" })).status).toBe(200);
    expect((await post("/sandbox/run_9/exec/command", { command: "true" }, { run: "run_8" })).status).toBe(403);
  });

  it("refuses a sandbox past the room's limit", async () => {
    for (const run of ["r1", "r2", "r3", "r4"]) {
      expect((await post(`/sandbox/${run}/exec/command`, { command: "true" }, { run })).status).toBe(200);
    }
    const response = await post("/sandbox/r5/exec/command", { command: "true" }, { run: "r5" });
    expect(response.status).toBe(429);
    expect(await response.json()).toMatchObject({ error: { code: "limit" } });
  });

  it("lets a member watch files through web-next but not run anything", async () => {
    await post("/sandbox/shared/files/write", { files: [{ path: "a.txt", content: "a" }] });
    const web = (exports as unknown as { WebEdge: Fetcher }).WebEdge;
    const headers = { "x-oryxa-member": JSON.stringify({ org: "org_1", member: "usr_1" }) };
    expect((await web.fetch(`https://edge.test${base}/sandbox/shared/files`, { headers })).status).toBe(200);
    const exec = await web.fetch(`https://edge.test${base}/sandbox/shared/exec/command`, {
      method: "POST",
      headers,
      body: JSON.stringify({ command: "rm -rf /" }),
    });
    expect(exec.status).toBe(403);
  });
});

describe("watching through web-next (#270 review)", () => {
  const web = () => (exports as unknown as { WebEdge: Fetcher }).WebEdge;
  const as = (member: string) => ({ "x-oryxa-member": JSON.stringify({ org: "org_1", member }) });

  it("opens a member's terminal only in their own private sandbox", async () => {
    await post("/sandbox/shared/exec/command", { command: "true" });
    await post("/sandbox/private-usr_1/exec/command", { command: "true" }, { member: "usr_1", private: true });
    const shared = await web().fetch(`https://edge.test${base}/sandbox/shared/terminal`, { method: "POST", headers: as("usr_1"), body: "{}" });
    expect(shared.status).toBe(403);
    expect(await shared.text()).toContain("watch-only");
    const own = await web().fetch(`https://edge.test${base}/sandbox/private-usr_1/terminal`, { method: "POST", headers: as("usr_1"), body: "{}" });
    expect(own.status).toBe(200);
    expect(((await own.json()) as { url: string }).url).toContain("/t/");
  });

  it("doesn't count listing files as use, so watching doesn't keep a sandbox up", async () => {
    await post("/sandbox/shared/exec/command", { command: "true" });
    const stub = env.ROOM.getByName(`org/org_1/room/thread/${room.id}`);
    const past = Date.now() - 10 * 60 * 1000;
    const { runInDurableObject } = await import("cloudflare:test");
    await runInDurableObject(stub, async (_instance, state) => {
      state.storage.sql.exec("UPDATE children SET last_used_at = ?", past);
    });
    expect((await web().fetch(`https://edge.test${base}/sandbox/shared/files`, { headers: as("usr_1") })).status).toBe(200);
    await runInDurableObject(stub, async (_instance, state) => {
      expect(state.storage.sql.exec<{ last_used_at: number }>("SELECT last_used_at FROM children").one().last_used_at).toBe(past);
    });
  });
});

describe("egress", () => {
  it("refuses metadata, private and internal targets, and allows the public internet", () => {
    for (const blocked of [
      "http://169.254.169.254/latest/meta-data/",
      "http://10.0.0.5/",
      "http://172.20.1.1/",
      "http://192.168.1.1/",
      "http://127.0.0.1:8080/",
      "http://localhost/",
      "http://[::1]/",
      "http://[fd00::1]/",
      "http://[::ffff:10.0.0.1]/",
      "http://metadata.google.internal/",
      "https://api.oryxa.in/v1/internal/agent-tokens",
    ]) {
      expect(blockedTarget(new URL(blocked)), blocked).not.toBeNull();
    }
    for (const allowed of ["https://pypi.org/simple/", "http://example.com/", "https://api.oryxa.in/v1/threads", "http://8.8.8.8/"]) {
      expect(blockedTarget(new URL(allowed)), allowed).toBeNull();
    }
  });
});
