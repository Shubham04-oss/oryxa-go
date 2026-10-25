// A teamspace call's notes (T1): when the call ends, what was said is kept
// in a small outbox in the room, summarized into decisions and actions, and
// posted to Go's POST /v1/edge/teamspaces/{id}/parley-notes with a log:write
// edge token. Go posts them in the room as the organization agent, once per
// call. A summary or a post that fails (no model yet, agents restarting, a
// paused teamspace) is tried again from the room's alarm, backing off, for a
// day; then the room log says the notes were given up on.
import type { Line } from "../voice/brain";
import type { ParleyNotes, TokenSubject } from "../voice/dispatch";
import { voiceProviders } from "../voice/providers";
import { CALL_EVENTS } from "./contracts";
import { agentName, type CallRoom, type RoomIdentity } from "./index";
import type { CallRow, CallStore } from "./store";

// The most Go takes (rooms/notes.go): items per list, characters per item.
const MAX_ITEMS = 50;
const MAX_ITEM_CHARS = 500;
const FIRST_RETRY_MS = 30_000;
const MAX_RETRY_MS = 60 * 60_000;
// GIVE_UP_MS is how long notes are tried: a paused teamspace may resume.
const GIVE_UP_MS = 24 * 60 * 60_000;

type NotesRow = {
  call_id: string;
  subject: string;
  duration_ms: number;
  lines: string;
  people: string;
  summary: string | null;
  attempts: number;
  queued_at: number;
  next_at: number;
};

// NotesLine is a line of the transcript, with the member who said it.
interface NotesLine extends Line {
  speaker: string;
}

function migrate(sql: SqlStorage): void {
  sql.exec(`
    CREATE TABLE IF NOT EXISTS call_notes (
      call_id TEXT PRIMARY KEY,
      subject TEXT NOT NULL,
      duration_ms INTEGER NOT NULL,
      lines TEXT NOT NULL,
      people TEXT NOT NULL,
      summary TEXT,
      status TEXT NOT NULL,
      attempts INTEGER NOT NULL DEFAULT 0,
      error TEXT,
      queued_at INTEGER NOT NULL,
      next_at INTEGER NOT NULL
    );
  `);
}

// notesPossible says whether this room's calls can leave notes here: a
// teamspace room, with a model to summarize and Go to post to.
export function notesPossible(ctx: CallRoom, identity: RoomIdentity): boolean {
  const providers = voiceProviders(ctx.env);
  return identity.room.kind === "teamspace" && Boolean(providers.notesWriter && providers.notes);
}

// queueNotes keeps what an ended call needs for its notes, before the
// transcript is saved to R2 and dropped from the room, and starts on them.
export function queueNotes(ctx: CallRoom, store: CallStore, identity: RoomIdentity, call: CallRow, endedAt: string): void {
  if (identity.room.kind !== "teamspace") return;
  migrate(ctx.sql);
  // Private answers stay their member's: they never reach the room's notes.
  const lines: NotesLine[] = store
    .segments(call.id)
    .filter((segment) => segment.private_for === null)
    .map((segment) => ({ speaker: segment.speaker, name: nameOf(store, call.id, segment.speaker, ctx), text: segment.text }));
  if (lines.every((line) => line.speaker === "agent")) {
    ctx.room.append({
      actor: { type: "system", id: "edge" },
      run: null,
      type: CALL_EVENTS.notesSkipped,
      summary: "No notes: nothing anyone said in the call reached the room",
      data: { callId: call.id, reason: "no_transcript" },
    });
    return;
  }
  const people: Record<string, string> = {};
  for (const participant of store.everyone(call.id)) people[participant.name.toLowerCase()] = participant.member;
  const subject: TokenSubject = { org: identity.org, ...(identity.team ? { team: identity.team } : {}), room: identity.room, member: call.started_by };
  const now = Date.now();
  ctx.sql.exec(
    `INSERT OR IGNORE INTO call_notes (call_id, subject, duration_ms, lines, people, status, queued_at, next_at)
     VALUES (?, ?, ?, ?, ?, 'pending', ?, ?)`,
    call.id,
    JSON.stringify(subject),
    Math.max(0, Date.parse(endedAt) - Date.parse(call.started_at)) || 0,
    JSON.stringify(lines),
    JSON.stringify(people),
    now,
    now,
  );
  ctx.waitUntil(runNotes(ctx));
}

function nameOf(store: CallStore, callId: string, speaker: string, ctx: CallRoom): string {
  if (speaker === "agent") return agentName(ctx.env);
  return store.participant(callId, speaker)?.name ?? speaker;
}

// notesDue is when the outbox next needs the room's alarm, if ever.
export function notesDue(sql: SqlStorage): number | null {
  migrate(sql);
  return sql.exec<{ at: number | null }>("SELECT MIN(next_at) AS at FROM call_notes WHERE status = 'pending'").one().at;
}

// One run of the outbox at a time per room: the alarm and a call ending can
// both start one.
const running = new WeakMap<object, Promise<void>>();

// runNotes tries every note that is due.
export function runNotes(ctx: CallRoom, now = Date.now()): Promise<void> {
  const current = running.get(ctx.sql);
  if (current) return current;
  const run = (async () => {
    migrate(ctx.sql);
    const due = ctx.sql.exec<NotesRow>("SELECT * FROM call_notes WHERE status = 'pending' AND next_at <= ? ORDER BY next_at", now).toArray();
    for (const row of due) await attempt(ctx, row);
  })().finally(() => running.delete(ctx.sql));
  running.set(ctx.sql, run);
  return run;
}

async function attempt(ctx: CallRoom, row: NotesRow): Promise<void> {
  const providers = voiceProviders(ctx.env);
  const subject = JSON.parse(row.subject) as TokenSubject;
  let notes: ParleyNotes;
  if (row.summary) {
    notes = JSON.parse(row.summary) as ParleyNotes;
  } else {
    if (!providers.notesWriter) return retry(ctx, row, "no model to summarize the call");
    try {
      const lines = (JSON.parse(row.lines) as NotesLine[]).map(({ name, text }) => ({ name, text }));
      const summary = await providers.notesWriter.summarize({ agentName: agentName(ctx.env), lines });
      notes = toNotes(row, summary, JSON.parse(row.people) as Record<string, string>);
    } catch (error) {
      return retry(ctx, row, `summarize: ${String(error)}`);
    }
    // Summarized once: a retry posts the same notes.
    ctx.sql.exec("UPDATE call_notes SET summary = ? WHERE call_id = ?", JSON.stringify(notes), row.call_id);
  }
  if (!providers.notes) return retry(ctx, row, "agents not configured");
  let status: number;
  try {
    status = await providers.notes.postNotes(subject, notes);
  } catch (error) {
    return retry(ctx, row, `post: ${String(error)}`);
  }
  if (status === 200 || status === 201) {
    ctx.sql.exec("UPDATE call_notes SET status = 'posted', attempts = attempts + 1, error = NULL, lines = '[]' WHERE call_id = ?", row.call_id);
    ctx.room.append({
      actor: { type: "agent", id: "voice" },
      run: null,
      type: CALL_EVENTS.notesPosted,
      summary: `${agentName(ctx.env)} posted the call's notes`,
      data: { callId: row.call_id, decisions: notes.decisions.length, actions: notes.actions.length },
    });
    return;
  }
  // A paused teamspace (409), a busy or restarting Go: later. Anything else
  // will not change by sending the same notes again.
  if (status === 409 || status === 429 || status === 408 || status >= 500) return retry(ctx, row, `agents answered ${status}`);
  fail(ctx, row, `agents answered ${status}`);
}

function toNotes(row: NotesRow, summary: { decisions: string[]; actions: { text: string; owner: string }[] }, people: Record<string, string>): ParleyNotes {
  const clip = (text: string) => text.replace(/\s+/g, " ").trim().slice(0, MAX_ITEM_CHARS);
  return {
    call_id: row.call_id,
    duration_ms: row.duration_ms,
    decisions: summary.decisions.map(clip).filter(Boolean).slice(0, MAX_ITEMS),
    actions: summary.actions
      .map((action) => {
        const owner = people[action.owner.trim().toLowerCase()];
        return { text: clip(action.text), ...(owner ? { owner_user_id: owner } : {}) };
      })
      .filter((action) => action.text !== "")
      .slice(0, MAX_ITEMS),
  };
}

function retry(ctx: CallRoom, row: NotesRow, error: string): void {
  const now = Date.now();
  if (now - row.queued_at >= GIVE_UP_MS) return fail(ctx, row, error);
  const next = now + Math.min(FIRST_RETRY_MS * 2 ** row.attempts, MAX_RETRY_MS);
  ctx.sql.exec("UPDATE call_notes SET attempts = attempts + 1, error = ?, next_at = ? WHERE call_id = ?", error, next, row.call_id);
  console.warn(JSON.stringify({ msg: "a call's notes will be tried again", callId: row.call_id, attempt: row.attempts + 1, error }));
  ctx.wake(next);
}

function fail(ctx: CallRoom, row: NotesRow, error: string): void {
  ctx.sql.exec("UPDATE call_notes SET status = 'failed', attempts = attempts + 1, error = ?, lines = '[]' WHERE call_id = ?", error, row.call_id);
  console.error(JSON.stringify({ msg: "a call's notes were given up on", callId: row.call_id, error }));
  ctx.room.append({
    actor: { type: "system", id: "edge" },
    run: null,
    type: CALL_EVENTS.notesFailed,
    summary: "The call's notes could not be posted",
    data: { callId: row.call_id, error: error.slice(0, 500) },
  });
}
