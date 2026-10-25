// Whether a member is talking to the agent. The baseline answers only when
// addressed (docs/design/live-layer/03, B4): its name near the start or the
// end of what was said ("Krude, what's…", "hey Krude …", "… right, Krude?"),
// or a question right after it spoke, from whoever it was answering.
// Precision is calibrated in Layer C; this errs on the side of staying quiet.
//
// The agent answers to several names (T3): its own and those the room shows
// for its agents. Speech recognition misspells names ("crude" for Krude), so
// a hard c, k and q sound the same, and a word one letter off a name of five
// letters or more counts, as long as it starts the same.

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

// nameWords is what each name is called by: its first word ("Arlo" for
// "Arlo-UJVX" or "Arlo Chen"), lowercased.
export function nameWords(names: string[]): string[] {
  return [...new Set(names.map((name) => words(name)[0] ?? "").filter(Boolean))];
}

function sound(word: string): string {
  return word.replace(/^(c|q)(?=[^eiy])/, "k").replace(/ck/g, "k");
}

// oneLetterOff reports whether a and b, the same length, differ in one
// letter, and not the first: "krudy" for "krude", never "rude" or "prude".
function oneLetterOff(a: string, b: string): boolean {
  if (a.length !== b.length || a[0] !== b[0]) return false;
  let differ = 0;
  for (let i = 1; i < a.length; i++) if (a[i] !== b[i] && ++differ > 1) return false;
  return differ === 1;
}

// isName reports whether a word said is one of the names.
export function isName(word: string, names: string[]): boolean {
  return names.some((name) => {
    if (word === name) return true;
    const heard = sound(word);
    const wanted = sound(name);
    if (heard === wanted) return true;
    return wanted.length >= 5 && oneLetterOff(heard, wanted);
  });
}

export function addressedTo(text: string, names: string[], followUp: boolean): Addressed {
  const said = words(text);
  const wanted = nameWords(names);
  const index = said.findIndex((word) => isName(word, wanted));
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
    if (!GREETINGS.has(word) && !isName(word, names)) break;
    rest = rest.slice(match[0].length);
  }
  return rest.trim();
}
