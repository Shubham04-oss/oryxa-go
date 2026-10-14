// copyToR2 fetches a file RealtimeKit made (a recording, the post-call
// transcript) from the short-lived URL its webhook named, and keeps it in the
// runs bucket. The URL came in a webhook whose signature was checked; it is
// still held to HTTPS.
import type { Env } from "../env";

const DOWNLOAD_TIMEOUT_MS = 120_000;

export async function copyToR2(env: Env, url: string, key: string, org: string, fallbackType: string): Promise<number> {
  const parsed = new URL(url);
  if (parsed.protocol !== "https:") throw new Error("a RealtimeKit download must be HTTPS");
  const response = await fetch(parsed, { signal: AbortSignal.timeout(DOWNLOAD_TIMEOUT_MS) });
  if (!response.ok || !response.body) throw new Error(`download answered ${response.status}`);
  const contentType = response.headers.get("content-type") ?? fallbackType;
  const length = Number(response.headers.get("content-length") ?? "");
  let body: ReadableStream | ArrayBuffer;
  if (Number.isFinite(length) && length > 0) {
    // R2 takes a stream only when its length is known.
    const fixed = new FixedLengthStream(length);
    response.body.pipeTo(fixed.writable).catch(() => undefined);
    body = fixed.readable;
  } else {
    body = await response.arrayBuffer();
  }
  const stored = await env.RUNS.put(key, body, { httpMetadata: { contentType }, customMetadata: { org } });
  return stored?.size ?? 0;
}
