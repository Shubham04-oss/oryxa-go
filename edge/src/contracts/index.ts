// The live-layer contracts on the edge side: the twin of Go's
// contracts/edge/v1 (docs/design/live-layer/01-contracts.md). Each schema is
// strict, so a field the other side sends that this side doesn't know is a
// contract break, not something silently dropped. contracts.test.ts runs
// every schema against the shared fixtures in contracts/edge/v1/testdata.
import { z } from "zod";

// ---- Rooms and naming (§1) ----

export const ROOM_KINDS = ["thread", "teamspace", "dm", "side"] as const;
export const RoomKind = z.enum(ROOM_KINDS);
export type RoomKind = z.infer<typeof RoomKind>;

// Matches Go's idPattern: nothing that can break out of a name segment.
export const ID_PATTERN = /^[A-Za-z0-9_-]{1,128}$/;
export const Id = z.string().regex(ID_PATTERN);

export const Room = z.strictObject({ kind: RoomKind, id: Id });
export type Room = z.infer<typeof Room>;

export const SHARED_SANDBOX = "shared";
export const privateSandbox = (memberId: string) => `private-${memberId}`;
export const ROOM_RUN = "_room";

export function validId(id: string): boolean {
  return ID_PATTERN.test(id);
}

export function validSandboxName(name: string): boolean {
  if (name === SHARED_SANDBOX) return true;
  if (name.startsWith("private-")) return validId(name.slice("private-".length));
  return validId(name);
}

export function roomDOName(orgId: string, room: Room): string {
  if (!validId(orgId)) throw new Error("invalid organization id");
  if (!ROOM_KINDS.includes(room.kind) || !validId(room.id)) throw new Error("invalid room");
  return `org/${orgId}/room/${room.kind}/${room.id}`;
}

export function sandboxDOName(orgId: string, room: Room, name: string): string {
  if (!validSandboxName(name)) throw new Error("invalid sandbox name");
  return `${roomDOName(orgId, room)}/sandbox/${name}`;
}

export function browserDOName(orgId: string, room: Room, sessionId: string): string {
  if (!validId(sessionId)) throw new Error("invalid browser session id");
  return `${roomDOName(orgId, room)}/browser/${sessionId}`;
}

export function voiceDOName(orgId: string, room: Room): string {
  return `${roomDOName(orgId, room)}/voice`;
}

export function runPrefix(orgId: string, room: Room, runId: string): string {
  if (!validId(orgId)) throw new Error("invalid organization id");
  if (!ROOM_KINDS.includes(room.kind) || !validId(room.id)) throw new Error("invalid room");
  if (runId !== ROOM_RUN && !validId(runId)) throw new Error("invalid run id");
  return `runs/${orgId}/${room.kind}/${room.id}/${runId}/`;
}

// ---- The edge token (§2, §4) ----

export const ISSUER_AGENTS = "agents";
export const AUDIENCE_EDGE = "edge";
export const ISSUER_EDGE = "edge";
export const AUDIENCE_AGENTS = "agents";
export const MAX_TOKEN_LIFETIME_SECONDS = 300;

export const SCOPES = [
  "sandbox:exec",
  "sandbox:files",
  "browser:drive",
  "call:join",
  "call:speak",
  "log:write",
  "log:read",
  "room:members",
] as const;
export const Scope = z.enum(SCOPES);
export type Scope = z.infer<typeof Scope>;

export const TokenClaims = z.strictObject({
  iss: z.string(),
  aud: z.string(),
  org: Id,
  team: z.string(),
  room: Room,
  member: z.string(),
  agent: z.string(),
  run: z.string(),
  scopes: z.array(Scope),
  private: z.boolean(),
  iat: z.number().int(),
  exp: z.number().int(),
  jti: z.string().min(1).max(128),
});
export type TokenClaims = z.infer<typeof TokenClaims>;

// ---- The sandbox API (§5) ----

export const LANGUAGES = ["python", "javascript", "typescript"] as const;

export const ExecCodeRequest = z.strictObject({
  language: z.enum(LANGUAGES),
  code: z.string(),
  timeoutMs: z.number().int().positive().optional(),
  clearContext: z.boolean().optional(),
});
export type ExecCodeRequest = z.infer<typeof ExecCodeRequest>;

export const ExecCommandRequest = z.strictObject({
  command: z.string().min(1),
  cwd: z.string().optional(),
  timeoutMs: z.number().int().positive().optional(),
});
export type ExecCommandRequest = z.infer<typeof ExecCommandRequest>;

export const ExecOutput = z.strictObject({
  type: z.string(),
  mimeType: z.string().optional(),
  text: z.string().optional(),
  data: z.string().optional(),
  name: z.string().optional(),
});
export type ExecOutput = z.infer<typeof ExecOutput>;

export const ExecResult = z.strictObject({
  stdout: z.string(),
  stderr: z.string(),
  exitCode: z.number().int(),
  results: z.array(ExecOutput),
  durationMs: z.number().int(),
  outputTruncated: z.boolean().optional(),
  timedOut: z.boolean().optional(),
  sessionRecreated: z.boolean().optional(),
  changedFiles: z.array(z.string()).optional(),
});
export type ExecResult = z.infer<typeof ExecResult>;

export const FileEntry = z.strictObject({
  name: z.string(),
  path: z.string(),
  type: z.enum(["file", "directory", "symlink", "other"]),
  size: z.number().int().optional(),
});
export type FileEntry = z.infer<typeof FileEntry>;

export const ListFilesResponse = z.strictObject({
  path: z.string(),
  entries: z.array(FileEntry),
  truncated: z.boolean().optional(),
});
export type ListFilesResponse = z.infer<typeof ListFilesResponse>;

export const ReadFilesRequest = z.strictObject({ paths: z.array(z.string().min(1)).min(1) });
export type ReadFilesRequest = z.infer<typeof ReadFilesRequest>;

export const Encoding = z.enum(["utf8", "base64"]);

export const FileContent = z.strictObject({
  path: z.string(),
  encoding: Encoding.optional(),
  content: z.string().optional(),
  mimeType: z.string().optional(),
  size: z.number().int(),
  truncated: z.boolean().optional(),
  error: z.string().optional(),
});
export type FileContent = z.infer<typeof FileContent>;

export const ReadFilesResponse = z.strictObject({ files: z.array(FileContent) });
export type ReadFilesResponse = z.infer<typeof ReadFilesResponse>;

export const WriteFile = z.strictObject({
  path: z.string().min(1),
  content: z.string(),
  encoding: Encoding.optional(),
});
export const WriteFilesRequest = z.strictObject({ files: z.array(WriteFile).min(1) });
export type WriteFilesRequest = z.infer<typeof WriteFilesRequest>;

export const WriteFilesResponse = z.strictObject({
  written: z.array(z.strictObject({ path: z.string(), size: z.number().int() })),
});
export type WriteFilesResponse = z.infer<typeof WriteFilesResponse>;

export const RemoveFilesRequest = z.strictObject({ paths: z.array(z.string().min(1)).min(1) });
export type RemoveFilesRequest = z.infer<typeof RemoveFilesRequest>;

export const RemoveFilesResponse = z.strictObject({ removed: z.array(z.string()) });
export type RemoveFilesResponse = z.infer<typeof RemoveFilesResponse>;

export const ResetResponse = z.strictObject({ reset: z.boolean() });

export const ArtifactRequest = z.strictObject({
  path: z.string().min(1),
  name: z.string().optional(),
  mimeType: z.string().optional(),
});
export type ArtifactRequest = z.infer<typeof ArtifactRequest>;

export const ArtifactResponse = z.strictObject({
  key: z.string(),
  name: z.string(),
  mimeType: z.string(),
  size: z.number().int(),
  sha256: z.string(),
});
export type ArtifactResponse = z.infer<typeof ArtifactResponse>;

export const PreviewRequest = z.strictObject({ port: z.number().int().min(1).max(65535) });
export type PreviewRequest = z.infer<typeof PreviewRequest>;

export const SignedURL = z.strictObject({ url: z.string(), expiresAt: z.string() });
export type SignedURL = z.infer<typeof SignedURL>;

export const TerminalRequest = z.strictObject({
  cols: z.number().int().positive().optional(),
  rows: z.number().int().positive().optional(),
});
export type TerminalRequest = z.infer<typeof TerminalRequest>;

export const SnapshotResponse = z.strictObject({
  id: z.string(),
  size: z.number().int(),
  createdAt: z.string(),
});
export type SnapshotResponse = z.infer<typeof SnapshotResponse>;

// ---- The browser API (§6) ----

export const NavigateRequest = z.strictObject({ url: z.string().min(1) });
export type NavigateRequest = z.infer<typeof NavigateRequest>;

export const ClickRequest = z.strictObject({ selector: z.string().min(1) });
export type ClickRequest = z.infer<typeof ClickRequest>;

export const FillRequest = z.strictObject({ selector: z.string().min(1), text: z.string() });
export type FillRequest = z.infer<typeof FillRequest>;

export const Element = z.strictObject({
  selector: z.string(),
  tag: z.string(),
  role: z.string().optional(),
  type: z.string().optional(),
  name: z.string().optional(),
  text: z.string().optional(),
  href: z.string().optional(),
  disabled: z.boolean().optional(),
});
export type Element = z.infer<typeof Element>;

export const PageSnapshot = z.strictObject({
  title: z.string(),
  url: z.string(),
  text: z.string().optional(),
  elements: z.array(Element).optional(),
  textTruncated: z.boolean().optional(),
  elementsTruncated: z.boolean().optional(),
});
export type PageSnapshot = z.infer<typeof PageSnapshot>;

export const PageResponse = z.strictObject({
  page: PageSnapshot,
  sessionRecreated: z.boolean().optional(),
});
export type PageResponse = z.infer<typeof PageResponse>;

export const ScreenshotResponse = z.strictObject({
  key: z.string(),
  mimeType: z.string(),
  width: z.number().int(),
  height: z.number().int(),
  data: z.string(),
  sessionRecreated: z.boolean().optional(),
});
export type ScreenshotResponse = z.infer<typeof ScreenshotResponse>;

export const LiveRequest = z.strictObject({ mode: z.enum(["tab", "full"]).optional() });
export type LiveRequest = z.infer<typeof LiveRequest>;

export const MAX_HANDOFF_MS = 30 * 60 * 1000;

export const HandoffRequest = z.strictObject({
  reason: z.string().min(1).max(4096),
  timeoutMs: z.number().int().positive().max(MAX_HANDOFF_MS).optional(),
});
export type HandoffRequest = z.infer<typeof HandoffRequest>;

export const HandoffResponse = z.strictObject({ handoffId: z.string(), live: SignedURL });
export type HandoffResponse = z.infer<typeof HandoffResponse>;

export const HandoffState = z.strictObject({
  active: z.boolean(),
  handoffId: z.string().optional(),
  reason: z.string().optional(),
  elapsedMs: z.number().int().optional(),
});
export type HandoffState = z.infer<typeof HandoffState>;

export const ResumeRequest = z.strictObject({
  success: z.boolean(),
  note: z.string().max(1024).optional(),
});
export type ResumeRequest = z.infer<typeof ResumeRequest>;

export const ResumeResponse = z.strictObject({ resumed: z.boolean(), page: PageSnapshot });
export type ResumeResponse = z.infer<typeof ResumeResponse>;

// ---- The room log (§8) and children (A3) ----

export const Actor = z.strictObject({
  type: z.enum(["member", "agent", "system"]),
  id: z.string(),
  onBehalfOf: z.string().optional(),
});
export type Actor = z.infer<typeof Actor>;

export const Cost = z.strictObject({
  unit: z.string(),
  amount: z.number(),
  model: z.string().optional(),
});
export type Cost = z.infer<typeof Cost>;

export const Event = z.strictObject({
  seq: z.number().int(),
  at: z.string(),
  org: z.string(),
  room: Room,
  actor: Actor,
  run: z.string().nullable(),
  type: z.string(),
  summary: z.string(),
  data: z.record(z.string(), z.unknown()),
  durationMs: z.number().int(),
  cost: Cost.optional(),
  private: z.boolean(),
});
export type Event = z.infer<typeof Event>;

export const EVENT_TYPE_PATTERN = /^[a-z][a-z0-9_.]{0,63}$/;

export const AppendEventRequest = z.strictObject({
  type: z.string().regex(EVENT_TYPE_PATTERN),
  summary: z.string().max(512),
  data: z.record(z.string(), z.unknown()).optional(),
  durationMs: z.number().int().nonnegative().optional(),
  cost: Cost.optional(),
});
export type AppendEventRequest = z.infer<typeof AppendEventRequest>;

export const LogResponse = z.strictObject({
  events: z.array(Event),
  next: z.number().int(),
  more: z.boolean(),
});
export type LogResponse = z.infer<typeof LogResponse>;

export const Child = z.strictObject({
  kind: z.enum(["sandbox", "browser"]),
  name: z.string(),
  status: z.string(),
  private: z.boolean().optional(),
  member: z.string().optional(),
  createdAt: z.string(),
  lastUsedAt: z.string(),
});
export type Child = z.infer<typeof Child>;

export const ChildrenResponse = z.strictObject({ children: z.array(Child) });
export type ChildrenResponse = z.infer<typeof ChildrenResponse>;

// ---- Errors ----

export const ERROR_CODES = [
  "unauthorized",
  "forbidden",
  "not_found",
  "invalid_request",
  "too_large",
  "conflict",
  "limit",
  "timeout",
  "unavailable",
  "internal",
] as const;
export const ErrorCode = z.enum(ERROR_CODES);
export type ErrorCode = z.infer<typeof ErrorCode>;

export const ERROR_STATUS: Record<ErrorCode, number> = {
  unauthorized: 401,
  forbidden: 403,
  not_found: 404,
  invalid_request: 400,
  too_large: 413,
  conflict: 409,
  limit: 429,
  timeout: 504,
  unavailable: 503,
  internal: 500,
};

export const ErrorBody = z.strictObject({
  error: z.strictObject({
    code: ErrorCode,
    message: z.string(),
    retryable: z.boolean().optional(),
  }),
});
export type ErrorBody = z.infer<typeof ErrorBody>;

// ---- Room membership (A18) ----

export const MAX_ROOM_MEMBERS = 1000;
const MemberId = z.string().min(1).max(128);

// MembersRequest is PUT …/members: a full replace (members) or a change
// (add/remove), never both.
export const MembersRequest = z
  .strictObject({
    members: z.array(MemberId).max(MAX_ROOM_MEMBERS).optional(),
    add: z.array(MemberId).max(MAX_ROOM_MEMBERS).optional(),
    remove: z.array(MemberId).max(MAX_ROOM_MEMBERS).optional(),
  })
  .refine((body) => (body.members !== undefined) !== (body.add !== undefined || body.remove !== undefined), {
    message: "send members (a replace) or add/remove (a change), not both",
  });
export type MembersRequest = z.infer<typeof MembersRequest>;

export const MembersResponse = z.strictObject({ members: z.array(z.string()) });
export type MembersResponse = z.infer<typeof MembersResponse>;

function memberPair(a: string, b: string): [string, string] {
  if (!a || !b || a === b || a.includes("_") || b.includes("_") || !validId(a) || !validId(b)) {
    throw new Error("a conversation needs two distinct member ids without _");
  }
  return a < b ? [a, b] : [b, a];
}

function checkedRoomId(id: string): string {
  if (!validId(id)) throw new Error("room id too long");
  return id;
}

// directRoomId is the edge room id of two members' direct message: their
// ids sorted, joined by "_" (Go: DirectRoomID).
export function directRoomId(memberA: string, memberB: string): string {
  const [a, b] = memberPair(memberA, memberB);
  return checkedRoomId(`${a}_${b}`);
}

// sideRoomId is a side conversation's: teamspace, then the members sorted
// (Go: SideRoomID).
export function sideRoomId(teamspaceId: string, memberA: string, memberB: string): string {
  if (!teamspaceId || !validId(teamspaceId)) throw new Error("invalid teamspace id");
  const [a, b] = memberPair(memberA, memberB);
  return checkedRoomId(`${teamspaceId}_${a}_${b}`);
}

// conversationMembers returns the two members a dm or side room's id names,
// or null when the id isn't one in canonical form.
export function conversationMembers(room: Room): [string, string] | null {
  const parts = room.id.split("_");
  try {
    if (room.kind === "dm" && parts.length === 2) {
      return directRoomId(parts[0]!, parts[1]!) === room.id ? memberPair(parts[0]!, parts[1]!) : null;
    }
    if (room.kind === "side" && parts.length >= 3) {
      const [a, b] = [parts[parts.length - 2]!, parts[parts.length - 1]!];
      return sideRoomId(parts.slice(0, -2).join("_"), a, b) === room.id ? memberPair(a, b) : null;
    }
  } catch {
    return null;
  }
  return null;
}
