// RealtimeKit's webhooks (developers.cloudflare.com/realtime/realtimekit/webhooks):
// the meeting ending, recordings uploaded, and the post-call transcript.
// They reach this Worker at POST /v1/webhooks/realtimekit. Each is checked
// against RealtimeKit's public key (rtk-signature: RSA-SHA256 over the raw
// body) before anything in it is believed, then handed to the room whose call
// it is: the meeting's title names it (index.ts, meetingTitle), or, for a
// recording, the file name prefix the recording was started with.
import type { Env } from "../env";
import { base64Decode, errorResponse, json } from "../http";
import { roomForMeeting } from "./index";

export interface RealtimeKitEvent {
  event: string;
  meeting?: { id?: string; sessionId?: string; title?: string; status?: string };
  reason?: string;
  participant?: { peerId?: string; userDisplayName?: string; customParticipantId?: string };
  recording?: {
    id?: string;
    recordingId?: string;
    status?: string;
    downloadUrl?: string;
    outputFileName?: string;
    meetingId?: string;
    recordingDuration?: number;
  };
  transcriptDownloadUrl?: string;
}

export const WEBHOOK_PATH = "/v1/webhooks/realtimekit";
const MAX_WEBHOOK_BYTES = 1 << 20;
// The events the call acts on; any other is acknowledged and dropped.
export const HANDLED_EVENTS = new Set(["meeting.ended", "meeting.participantLeft", "recording.statusUpdate", "meeting.transcript"]);

const keys = new Map<string, Promise<CryptoKey>>();

function publicKey(pem: string): Promise<CryptoKey> {
  let key = keys.get(pem);
  if (!key) {
    const body = pem.replace(/-----(BEGIN|END) PUBLIC KEY-----/g, "").replace(/\s+/g, "");
    key = crypto.subtle.importKey("spki", base64Decode(body), { name: "RSASSA-PKCS1-v1_5", hash: "SHA-256" }, false, ["verify"]);
    keys.set(pem, key);
  }
  return key;
}

export async function verifyWebhook(pem: string, body: Uint8Array, signature: string): Promise<boolean> {
  try {
    return await crypto.subtle.verify("RSASSA-PKCS1-v1_5", await publicKey(pem), base64Decode(signature), body);
  } catch {
    return false;
  }
}

// recordingTitle is the file name prefix a call's recording starts with; it
// carries the same names as the meeting's title, with dots, which no
// contract id holds.
export function recordingPrefix(title: string): string {
  return title.replaceAll(":", ".");
}

function titleOf(event: RealtimeKitEvent): string | undefined {
  if (event.meeting?.title) return event.meeting.title;
  const file = event.recording?.outputFileName;
  if (!file) return undefined;
  // RealtimeKit may add to the prefix; the call id is read by its own shape.
  const parts = file.split(".");
  const callId = /^call_[0-9a-f]{32}/.exec(parts[4] ?? "")?.[0];
  return parts.length >= 5 && callId ? [...parts.slice(0, 4), callId].join(":") : undefined;
}

export async function handleRealtimeKitWebhook(request: Request, env: Env): Promise<Response> {
  if (request.method !== "POST") return errorResponse("not_found", "no such route");
  if (!env.REALTIMEKIT_WEBHOOK_PUBLIC_KEY) return errorResponse("unavailable", "calls are not set up here");
  const signature = request.headers.get("rtk-signature") ?? "";
  const deliveryId = request.headers.get("rtk-uuid") ?? "";
  const raw = new Uint8Array(await request.arrayBuffer());
  if (raw.length > MAX_WEBHOOK_BYTES) return errorResponse("too_large", "webhook too large");
  if (!signature || !(await verifyWebhook(env.REALTIMEKIT_WEBHOOK_PUBLIC_KEY, raw, signature))) {
    console.warn(JSON.stringify({ msg: "realtimekit webhook refused: bad signature" }));
    return errorResponse("unauthorized", "bad signature");
  }
  let event: RealtimeKitEvent;
  try {
    event = JSON.parse(new TextDecoder().decode(raw)) as RealtimeKitEvent;
  } catch {
    return errorResponse("invalid_request", "body is not JSON");
  }
  if (!HANDLED_EVENTS.has(event.event)) return json({ ignored: true });
  const target = roomForMeeting(titleOf(event));
  if (!target || !deliveryId) {
    console.warn(JSON.stringify({ msg: "realtimekit webhook names no Oryxa call", event: event.event }));
    return json({ ignored: true });
  }
  try {
    await env.ROOM.getByName(target.doName).callOp({ type: "webhook", event, deliveryId, callId: target.callId });
  } catch (error) {
    // RealtimeKit sends it again after a 5xx.
    console.error(JSON.stringify({ msg: "apply a realtimekit webhook", event: event.event, error: String(error) }));
    return errorResponse("unavailable", "try again", true);
  }
  return json({ ok: true });
}
