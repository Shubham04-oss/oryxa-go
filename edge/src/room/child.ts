// The protocol between a RoomDO and the children it runs. The room has
// already checked who is asking and what they may do; a child only does the
// work and reports, in events, what the room's log should say.
import { z } from "zod";
import { type Actor, ERROR_STATUS, type ErrorBody, type Room } from "../contracts";
import { EdgeError } from "../http";

export interface ChildContext {
  org: string;
  room: Room;
  name: string;
  run: string | null;
  member: string;
  private: boolean;
  actor: Actor;
}

export interface ChildEvent {
  type: string;
  summary: string;
  data?: Record<string, unknown>;
  durationMs?: number;
}

export interface ChildReply {
  status: number;
  body: unknown;
  events: ChildEvent[];
}

// childError turns a thrown error into the reply a room passes on: across
// RPC an EdgeError loses its class, so the child encodes it here.
export function childError(error: unknown, events: ChildEvent[] = []): ChildReply {
  if (error instanceof EdgeError) {
    const body: ErrorBody = { error: { code: error.code, message: error.message, ...(error.retryable ? { retryable: true } : {}) } };
    return { status: ERROR_STATUS[error.code], body, events };
  }
  if (error instanceof z.ZodError) {
    const issue = error.issues[0];
    return { status: 400, body: { error: { code: "invalid_request", message: `${issue?.path.join(".") || "body"}: ${issue?.message ?? "invalid"}` } }, events };
  }
  console.error(JSON.stringify({ msg: "child operation failed", error: String(error), stack: (error as Error)?.stack }));
  return { status: 500, body: { error: { code: "internal", message: "internal error" } }, events };
}
