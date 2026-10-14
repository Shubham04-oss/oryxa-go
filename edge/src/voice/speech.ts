// Text to speech on Workers AI (the owner's decision of 2026-10-14):
// Deepgram Aura, as MP3 one sentence at a time, which every browser decodes.
import type { Env } from "../env";

export interface Speech {
  synthesize(text: string): Promise<Uint8Array>;
}

const DEFAULT_MODEL = "@cf/deepgram/aura-1";
const DEFAULT_SPEAKER = "angus";

export class WorkersAISpeech implements Speech {
  constructor(
    private readonly ai: Ai,
    private readonly model: string = DEFAULT_MODEL,
    private readonly speaker: string = DEFAULT_SPEAKER,
  ) {}

  static fromEnv(env: Env): WorkersAISpeech | undefined {
    if (!env.AI) return undefined;
    return new WorkersAISpeech(env.AI, env.VOICE_TTS_MODEL?.trim() || DEFAULT_MODEL, env.VOICE_TTS_SPEAKER?.trim() || DEFAULT_SPEAKER);
  }

  async synthesize(text: string): Promise<Uint8Array> {
    const output: unknown = await (this.ai as unknown as { run(model: string, input: unknown): Promise<unknown> }).run(this.model, {
      text,
      speaker: this.speaker,
      encoding: "mp3",
    });
    return toBytes(output);
  }
}

// toBytes reads whichever shape the model's audio came back in.
export async function toBytes(output: unknown): Promise<Uint8Array> {
  if (output instanceof Uint8Array) return output;
  if (output instanceof ArrayBuffer) return new Uint8Array(output);
  if (output instanceof ReadableStream) return new Uint8Array(await new Response(output).arrayBuffer());
  if (output instanceof Response) return new Uint8Array(await output.arrayBuffer());
  const audio = (output as { audio?: unknown } | null)?.audio;
  if (typeof audio === "string") {
    const binary = atob(audio);
    const bytes = new Uint8Array(binary.length);
    for (let index = 0; index < binary.length; index++) bytes[index] = binary.charCodeAt(index);
    return bytes;
  }
  throw new Error("the speech model returned no audio");
}

// sentences splits an answer into the pieces spoken one at a time, so the
// first is heard while the rest are made.
export function sentences(text: string): string[] {
  const parts = text.match(/[^.!?]+[.!?]+["')\]]*\s*|[^.!?]+$/g) ?? [text];
  const out: string[] = [];
  for (const part of parts) {
    const piece = part.trim();
    if (!piece) continue;
    // Very short pieces ride with the one before.
    if (out.length > 0 && piece.length < 12) out[out.length - 1] += ` ${piece}`;
    else out.push(piece);
  }
  return out;
}

// MAX_SPOKEN_CHARS bounds what is read aloud; the rest of a long answer is
// in the room.
export const MAX_SPOKEN_CHARS = 600;

// spoken turns an answer written for reading into one for listening: no
// markdown, and cut at a sentence once it runs long.
export function spoken(text: string): { text: string; cut: boolean } {
  const plain = text
    .replace(/```[\s\S]*?```/g, " ")
    .replace(/\[([^\]]+)\]\([^)]+\)/g, "$1")
    .replace(/https?:\/\/\S+/g, "")
    .replace(/[*_`#>|]+/g, "")
    .replace(/^\s*[-•]\s+/gm, "")
    .replace(/\s+/g, " ")
    .trim();
  if (plain.length <= MAX_SPOKEN_CHARS) return { text: plain, cut: false };
  let kept = "";
  for (const sentence of sentences(plain)) {
    if ((kept + " " + sentence).length > MAX_SPOKEN_CHARS) break;
    kept = kept ? `${kept} ${sentence}` : sentence;
  }
  return { text: kept || plain.slice(0, MAX_SPOKEN_CHARS), cut: true };
}
