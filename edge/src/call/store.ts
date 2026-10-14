// The room's call, kept in the RoomDO's SQLite beside the room log: at most
// one call at a time, who is in it with which RealtimeKit participant, its
// recording and consent state, and its transcript until the call is saved.
import type { RecordingState } from "./contracts";

export type CallRow = {
  id: string;
  meeting_id: string;
  started_at: string;
  started_by: string;
  ended_at: string | null;
  recording_id: string | null;
  recording_by: string | null;
  recording_since: string | null;
  agent_present: number;
};

export type ParticipantRow = {
  call_id: string;
  member: string;
  name: string;
  participant_id: string;
  joined_at: string;
  left_at: string | null;
};

export type SegmentRow = {
  call_id: string;
  segment_id: string;
  speaker: string;
  text: string;
  at: number;
  // private_for, when set, is the one member a line is for: an answer the
  // work agent found with their access.
  private_for: string | null;
};

export class CallStore {
  constructor(private readonly sql: SqlStorage) {
    // Each statement is idempotent, so every store made over the same
    // storage may run them.
    sql.exec(`
      CREATE TABLE IF NOT EXISTS call_calls (
        id TEXT PRIMARY KEY,
        meeting_id TEXT NOT NULL,
        started_at TEXT NOT NULL,
        started_by TEXT NOT NULL,
        ended_at TEXT,
        recording_id TEXT,
        recording_by TEXT,
        recording_since TEXT,
        agent_present INTEGER NOT NULL DEFAULT 1
      );
      CREATE INDEX IF NOT EXISTS call_calls_meeting ON call_calls (meeting_id);
      CREATE TABLE IF NOT EXISTS call_participants (
        call_id TEXT NOT NULL,
        member TEXT NOT NULL,
        name TEXT NOT NULL,
        participant_id TEXT NOT NULL,
        joined_at TEXT NOT NULL,
        left_at TEXT,
        PRIMARY KEY (call_id, member)
      );
      CREATE TABLE IF NOT EXISTS call_segments (
        call_id TEXT NOT NULL,
        segment_id TEXT NOT NULL,
        speaker TEXT NOT NULL,
        text TEXT NOT NULL,
        at INTEGER NOT NULL,
        private_for TEXT,
        PRIMARY KEY (call_id, segment_id)
      );
      CREATE TABLE IF NOT EXISTS call_webhooks (id TEXT PRIMARY KEY, at INTEGER NOT NULL);
    `);
  }

  active(): CallRow | undefined {
    return this.sql.exec<CallRow>("SELECT * FROM call_calls WHERE ended_at IS NULL ORDER BY started_at DESC LIMIT 1").toArray()[0];
  }

  byId(id: string): CallRow | undefined {
    return this.sql.exec<CallRow>("SELECT * FROM call_calls WHERE id = ?", id).toArray()[0];
  }

  byMeeting(meetingId: string): CallRow | undefined {
    return this.sql.exec<CallRow>("SELECT * FROM call_calls WHERE meeting_id = ? ORDER BY started_at DESC LIMIT 1", meetingId).toArray()[0];
  }

  start(id: string, meetingId: string, member: string, at: string): void {
    this.sql.exec(
      "INSERT INTO call_calls (id, meeting_id, started_at, started_by) VALUES (?, ?, ?, ?)",
      id,
      meetingId,
      at,
      member,
    );
  }

  end(id: string, at: string): boolean {
    return this.sql.exec("UPDATE call_calls SET ended_at = ? WHERE id = ? AND ended_at IS NULL", at, id).rowsWritten > 0;
  }

  participant(callId: string, member: string): ParticipantRow | undefined {
    return this.sql
      .exec<ParticipantRow>("SELECT * FROM call_participants WHERE call_id = ? AND member = ?", callId, member)
      .toArray()[0];
  }

  // present lists who is in the call: joined and not left.
  present(callId: string): ParticipantRow[] {
    return this.sql
      .exec<ParticipantRow>("SELECT * FROM call_participants WHERE call_id = ? AND left_at IS NULL ORDER BY joined_at", callId)
      .toArray();
  }

  // everyone lists every participant the call ever had, so ending it can
  // revoke every token it issued.
  everyone(callId: string): ParticipantRow[] {
    return this.sql.exec<ParticipantRow>("SELECT * FROM call_participants WHERE call_id = ?", callId).toArray();
  }

  join(callId: string, member: string, name: string, participantId: string, at: string): void {
    this.sql.exec(
      `INSERT INTO call_participants (call_id, member, name, participant_id, joined_at, left_at)
       VALUES (?, ?, ?, ?, ?, NULL)
       ON CONFLICT (call_id, member) DO UPDATE SET name = excluded.name, participant_id = excluded.participant_id,
         joined_at = excluded.joined_at, left_at = NULL`,
      callId,
      member,
      name,
      participantId,
      at,
    );
  }

  leave(callId: string, member: string, at: string): boolean {
    return (
      this.sql.exec("UPDATE call_participants SET left_at = ? WHERE call_id = ? AND member = ? AND left_at IS NULL", at, callId, member)
        .rowsWritten > 0
    );
  }

  recording(call: CallRow): RecordingState {
    return { on: call.recording_id !== null, by: call.recording_by, since: call.recording_since };
  }

  setRecording(callId: string, recordingId: string | null, by: string | null, since: string | null): void {
    this.sql.exec(
      "UPDATE call_calls SET recording_id = ?, recording_by = ?, recording_since = ? WHERE id = ?",
      recordingId,
      by,
      since,
      callId,
    );
  }

  setAgentPresent(callId: string, present: boolean): void {
    this.sql.exec("UPDATE call_calls SET agent_present = ? WHERE id = ?", present ? 1 : 0, callId);
  }

  // addSegment keeps a final segment once, and reports whether it was new.
  addSegment(segment: SegmentRow): boolean {
    return (
      this.sql.exec(
        "INSERT OR IGNORE INTO call_segments (call_id, segment_id, speaker, text, at, private_for) VALUES (?, ?, ?, ?, ?, ?)",
        segment.call_id,
        segment.segment_id,
        segment.speaker,
        segment.text,
        segment.at,
        segment.private_for,
      ).rowsWritten > 0
    );
  }

  segments(callId: string): SegmentRow[] {
    return this.sql.exec<SegmentRow>("SELECT * FROM call_segments WHERE call_id = ? ORDER BY at, segment_id", callId).toArray();
  }

  dropSegments(callId: string): void {
    this.sql.exec("DELETE FROM call_segments WHERE call_id = ?", callId);
  }

  // seenWebhook reports whether a webhook delivery, by RealtimeKit's
  // rtk-uuid, was already applied; markWebhook records that it was.
  seenWebhook(id: string): boolean {
    return this.sql.exec("SELECT 1 FROM call_webhooks WHERE id = ?", id).toArray().length > 0;
  }

  markWebhook(id: string): void {
    const now = Date.now();
    this.sql.exec("DELETE FROM call_webhooks WHERE at < ?", now - 7 * 24 * 3600 * 1000);
    this.sql.exec("INSERT OR IGNORE INTO call_webhooks (id, at) VALUES (?, ?)", id, now);
  }
}
