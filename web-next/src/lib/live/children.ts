// A room's sandboxes and browsers (A8), as a member reaches them through
// the Worker (src/edge-proxy.ts): list them, read a sandbox's files, open its
// preview or terminal, watch a browser and hand it back after taking it over.
// Running code and driving a browser stay the agents'. The shapes are
// edge/src/contracts's.
import { apiRequest } from "@/lib/api/client"
import { livePath } from "./client"
import type { LiveRoom } from "./client"

export type ChildKind = "sandbox" | "browser"

/** One of the room's sandboxes or browsers. */
export type LiveChild = {
  kind: ChildKind
  /** "shared", "private-{member}", a run's id, or a browser's id. */
  name: string
  status: string
  private?: boolean
  member?: string
  createdAt: string
  lastUsedAt: string
}

export type FileEntry = {
  name: string
  path: string
  type: "file" | "directory" | "symlink" | "other"
  size?: number
}

export type FileList = { path: string; entries: FileEntry[]; truncated?: boolean }

export type FileContent = {
  path: string
  encoding?: "utf8" | "base64"
  content?: string
  mimeType?: string
  size: number
  truncated?: boolean
  error?: string
}

/** A link the edge signed: the preview, the terminal or a live view. */
export type SignedUrl = { url: string; expiresAt: string }

export type HandoffState = {
  active: boolean
  handoffId?: string
  /** Why the agent asked the member to take over (a login, a captcha). */
  reason?: string
  elapsedMs?: number
}

function childPath(
  organizationId: string,
  room: LiveRoom,
  kind: ChildKind,
  name: string,
  rest: string
) {
  return livePath(
    organizationId,
    room,
    `/${kind}/${encodeURIComponent(name)}/${rest}`
  )
}

export function childKey(organizationId: string, room: LiveRoom) {
  return ["live", organizationId, room.kind, room.id, "children"] as const
}

export async function listChildren(
  organizationId: string,
  room: LiveRoom,
  signal?: AbortSignal
): Promise<LiveChild[]> {
  const answer = await apiRequest<{ children: LiveChild[] }>(
    livePath(organizationId, room, "/children"),
    { signal }
  )
  return answer.children
}

export function listFiles(
  organizationId: string,
  room: LiveRoom,
  sandbox: string,
  path = "",
  signal?: AbortSignal
): Promise<FileList> {
  const query = path ? `?${new URLSearchParams({ path }).toString()}` : ""
  return apiRequest<FileList>(
    `${childPath(organizationId, room, "sandbox", sandbox, "files")}${query}`,
    { signal }
  )
}

export async function readFile(
  organizationId: string,
  room: LiveRoom,
  sandbox: string,
  path: string
): Promise<FileContent | undefined> {
  const answer = await apiRequest<{ files: FileContent[] }>(
    childPath(organizationId, room, "sandbox", sandbox, "files/read"),
    { method: "POST", body: { paths: [path] } }
  )
  return answer.files[0]
}

/** A signed link to a port the sandbox serves, for an iframe. */
export function openPreview(
  organizationId: string,
  room: LiveRoom,
  sandbox: string,
  port: number
): Promise<SignedUrl> {
  return apiRequest<SignedUrl>(
    childPath(organizationId, room, "sandbox", sandbox, "preview"),
    { method: "POST", body: { port } }
  )
}

/** A signed WebSocket link to a shell in the sandbox. */
export function openTerminal(
  organizationId: string,
  room: LiveRoom,
  sandbox: string,
  size: { cols: number; rows: number }
): Promise<SignedUrl> {
  return apiRequest<SignedUrl>(
    childPath(organizationId, room, "sandbox", sandbox, "terminal"),
    { method: "POST", body: size }
  )
}

/** A signed link to watch the browser, or with "full" to take it over. */
export function openLiveView(
  organizationId: string,
  room: LiveRoom,
  browser: string,
  mode: "tab" | "full" = "tab"
): Promise<SignedUrl> {
  return apiRequest<SignedUrl>(
    childPath(organizationId, room, "browser", browser, "live"),
    { method: "POST", body: { mode } }
  )
}

export function readHandoff(
  organizationId: string,
  room: LiveRoom,
  browser: string,
  signal?: AbortSignal
): Promise<HandoffState> {
  return apiRequest<HandoffState>(
    childPath(organizationId, room, "browser", browser, "handoff"),
    { signal }
  )
}

/** Hands the browser back to the agent once the member is done. */
export function handBack(
  organizationId: string,
  room: LiveRoom,
  browser: string,
  success: boolean,
  note?: string
) {
  return apiRequest<{ resumed: boolean }>(
    childPath(organizationId, room, "browser", browser, "resume"),
    { method: "POST", body: note ? { success, note } : { success } }
  )
}

/** A sandbox or browser to open, and the room it runs in. */
export type LiveTarget = { room: LiveRoom; child: LiveChild }

/** The edge's id pattern; a thread id outside it isn't a room's name. */
const ROOM_ID = /^[A-Za-z0-9_-]{1,128}$/

/**
 * The room an agent's conversation runs its sandbox and browser in. Go
 * names it by the thread (edgeclient.SafeID), hashing an id the edge can't
 * take; such a thread has no room a page can name, so none is offered.
 */
export function threadRoom(threadId: string | null | undefined): LiveRoom | undefined {
  return threadId && ROOM_ID.test(threadId) ? { kind: "thread", id: threadId } : undefined
}

/** How a child is named to a member. */
export function childLabel(child: LiveChild, viewerId?: string): string {
  if (child.kind === "browser") {
    return child.member && child.member === viewerId ? "Your browser" : "Browser"
  }
  if (child.name === "shared") return "Shared sandbox"
  if (child.name.startsWith("private-")) return "Your private sandbox"
  return "A run's sandbox"
}
