// Whether a member is talking to the agent. The baseline answers only when
// addressed (docs/design/live-layer/03, B4): its name near the start or the
// end of what was said ("Krude, what's…", "hey Krude …", "… right, Krude?"),
// or a question right after it spoke, from whoever it was answering.
// Precision is calibrated in Layer C; this errs on the side of staying quiet.

// FOLLOW_UP_MS is how long after the agent stops speaking a question from
// the member it answered counts as addressed to it.
export const FOLLOW_UP_MS = 10_000;

const GREETINGS = new Set(["hey", "hi", "ok", "okay", "so", "and", "yo", "um", "uh", "please"]);

function words(text: string): string[] {
  return text
    .toLowerCase()
    .replace(/[^\p{L}\p{N}'\s]/gu, " ")
    .split(/\s+/)
    .filter(Boolean);
}

export interface Addressed {
  addressed: boolean;
  // request is what was said with the greeting and the name taken off.
  request: string;
}

export function addressedTo(text: string, names: string[], followUp: boolean): Addressed {
  const said = words(text);
  const wanted = names.map((name) => name.toLowerCase().trim()).filter(Boolean);
  const index = said.findIndex((word) => wanted.includes(word));
  const near = index >= 0 && (index <= 3 || index >= said.length - 3);
  if (near) return { addressed: true, request: stripName(text, wanted) || text.trim() };
  if (followUp && /\?\s*$/.test(text.trim())) return { addressed: true, request: text.trim() };
  return { addressed: false, request: "" };
}

// stripName removes a leading "hey Krude," from the request, keeping the
// rest as it was said.
function stripName(text: string, names: string[]): string {
  let rest = text.trim();
  for (;;) {
    const match = /^([\p{L}\p{N}']+)[\s,.!:;-]*/u.exec(rest);
    if (!match) break;
    const word = match[1]!.toLowerCase();
    if (!GREETINGS.has(word) && !names.includes(word)) break;
    rest = rest.slice(match[0].length);
  }
  return rest.trim();
}
