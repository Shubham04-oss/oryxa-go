// Runs every shared fixture through its schema: the edge's half of the
// contract test (Go's half is contracts/edge/v1/v1_test.go, over the same
// files). A fixture the Go side has and this side doesn't map fails here.
import { readdirSync, readFileSync } from "node:fs";
import { join } from "node:path";
import { describe, expect, it } from "vitest";
import type { z } from "zod";
import * as c from "./index";

const TESTDATA = join(__dirname, "../../../contracts/edge/v1/testdata");

const fixtures: Record<string, z.ZodType> = {
  "token.claims.json": c.TokenClaims,
  "sandbox.exec_code.request.json": c.ExecCodeRequest,
  "sandbox.exec_command.request.json": c.ExecCommandRequest,
  "sandbox.exec.result.json": c.ExecResult,
  "sandbox.exec.timeout.result.json": c.ExecResult,
  "sandbox.files.list.response.json": c.ListFilesResponse,
  "sandbox.files.read.request.json": c.ReadFilesRequest,
  "sandbox.files.read.response.json": c.ReadFilesResponse,
  "sandbox.files.write.request.json": c.WriteFilesRequest,
  "sandbox.files.write.response.json": c.WriteFilesResponse,
  "sandbox.files.remove.request.json": c.RemoveFilesRequest,
  "sandbox.files.remove.response.json": c.RemoveFilesResponse,
  "sandbox.reset.response.json": c.ResetResponse,
  "sandbox.artifact.request.json": c.ArtifactRequest,
  "sandbox.artifact.response.json": c.ArtifactResponse,
  "sandbox.preview.request.json": c.PreviewRequest,
  "sandbox.terminal.request.json": c.TerminalRequest,
  "sandbox.snapshot.response.json": c.SnapshotResponse,
  "signed_url.json": c.SignedURL,
  "browser.navigate.request.json": c.NavigateRequest,
  "browser.click.request.json": c.ClickRequest,
  "browser.fill.request.json": c.FillRequest,
  "browser.page.response.json": c.PageResponse,
  "browser.screenshot.response.json": c.ScreenshotResponse,
  "browser.live.request.json": c.LiveRequest,
  "browser.handoff.request.json": c.HandoffRequest,
  "browser.handoff.response.json": c.HandoffResponse,
  "browser.handoff.state.json": c.HandoffState,
  "browser.resume.request.json": c.ResumeRequest,
  "browser.resume.response.json": c.ResumeResponse,
  "event.json": c.Event,
  "event.room.json": c.Event,
  "log.append.request.json": c.AppendEventRequest,
  "log.response.json": c.LogResponse,
  "children.response.json": c.ChildrenResponse,
  "members.replace.request.json": c.MembersRequest,
  "members.change.request.json": c.MembersRequest,
  "members.response.json": c.MembersResponse,
  "error.json": c.ErrorBody,
  "error.retryable.json": c.ErrorBody,
};

describe("shared fixtures", () => {
  for (const [name, schema] of Object.entries(fixtures)) {
    it(`${name} parses and survives a round trip`, () => {
      const raw = JSON.parse(readFileSync(join(TESTDATA, name), "utf8"));
      const parsed = schema.parse(raw);
      expect(JSON.parse(JSON.stringify(parsed))).toEqual(raw);
    });
  }

  it("maps every fixture file", () => {
    const files = readdirSync(TESTDATA).filter((file) => file.endsWith(".json"));
    expect(files.filter((file) => !(file in fixtures))).toEqual([]);
  });

  it("refuses a field the contract doesn't have", () => {
    const raw = JSON.parse(readFileSync(join(TESTDATA, "sandbox.exec_code.request.json"), "utf8"));
    expect(c.ExecCodeRequest.safeParse({ ...raw, shell: true }).success).toBe(false);
  });
});

describe("names", () => {
  const room = { kind: "thread", id: "thr_1" } as const;
  it("matches Go's names", () => {
    expect(c.roomDOName("org_1", room)).toBe("org/org_1/room/thread/thr_1");
    expect(c.sandboxDOName("org_1", room, "shared")).toBe("org/org_1/room/thread/thr_1/sandbox/shared");
    expect(c.sandboxDOName("org_1", room, c.privateSandbox("usr_1"))).toBe(
      "org/org_1/room/thread/thr_1/sandbox/private-usr_1",
    );
    expect(c.browserDOName("org_1", room, "default")).toBe("org/org_1/room/thread/thr_1/browser/default");
    expect(c.voiceDOName("org_1", room)).toBe("org/org_1/room/thread/thr_1/voice");
    expect(c.runPrefix("org_1", room, "run_1")).toBe("runs/org_1/thread/thr_1/run_1/");
    expect(c.runPrefix("org_1", room, c.ROOM_RUN)).toBe("runs/org_1/thread/thr_1/_room/");
  });

  it("refuses separators", () => {
    expect(() => c.roomDOName("org/2", room)).toThrow();
    expect(() => c.roomDOName("org_1", { kind: "thread", id: ".." })).toThrow();
    expect(() => c.sandboxDOName("org_1", room, "shared/../x")).toThrow();
    expect(() => c.sandboxDOName("org_1", room, "private-")).toThrow();
    expect(() => c.browserDOName("org_1", room, "a/b")).toThrow();
    expect(() => c.runPrefix("org_1", room, "rün")).toThrow();
  });
});

describe("conversation rooms", () => {
  const a = "6c1d5f0e-2b7a-4c8e-9f3d-1a2b3c4d5e6f";
  const b = "0a1d5f0e-2b7a-4c8e-9f3d-1a2b3c4d5e6f";
  it("names them as Go does", () => {
    expect(c.directRoomId(a, b)).toBe(`${b}_${a}`);
    expect(c.directRoomId(b, a)).toBe(`${b}_${a}`);
    expect(c.sideRoomId("tsp_Ab-c_d9", a, b)).toBe(`tsp_Ab-c_d9_${b}_${a}`);
    expect(c.conversationMembers({ kind: "dm", id: `${b}_${a}` })).toEqual([b, a]);
    expect(c.conversationMembers({ kind: "side", id: `tsp_Ab-c_d9_${b}_${a}` })).toEqual([b, a]);
  });
  it("refuses what isn't canonical", () => {
    expect(c.conversationMembers({ kind: "dm", id: `${a}_${b}` })).toBeNull();
    expect(c.conversationMembers({ kind: "thread", id: `${b}_${a}` })).toBeNull();
    expect(() => c.directRoomId(a, a)).toThrow();
    expect(c.MembersRequest.safeParse({ members: [a], add: [b] }).success).toBe(false);
    expect(c.MembersRequest.safeParse({}).success).toBe(false);
  });
});
