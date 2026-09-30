// Small sentences a request card says about what its agent may read, and the
// way a name starts one. Pure, so the card and its tests share one set of
// rules.
import { AGENT_REQUEST_TOOLBOXES } from "@/lib/agents/agent-requests"
import type { AgentRequestToolbox } from "@/lib/agents/agent-requests"

/** A name that starts a sentence: "your agent" is "Your agent" there. */
export function capitalized(text: string): string {
  return text.charAt(0).toUpperCase() + text.slice(1)
}

/** Words as a sentence lists them: "a", "a and b", "a, b and c". */
function listWords(words: readonly string[]): string {
  if (words.length < 2) return words.join("")
  return `${words.slice(0, -1).join(", ")} and ${words.at(-1)}`
}

/** Each toolbox a request may ask for, as its owner reads what it opens. */
const toolboxWords: Record<AgentRequestToolbox, string> = {
  apps: "your connected apps",
  knowledge: "team knowledge",
}

/**
 * What approving a request lets its agent read, as its owner is asked: the
 * toolboxes among what it asked for ("Nia may read your connected apps and
 * team knowledge."), or nothing when it asked for none. Other names grant
 * nothing, so they are not said.
 */
export function toolsLine(
  agentName: string,
  requestedTools: readonly string[] | undefined
): string | undefined {
  const asked = new Set(requestedTools)
  const read = AGENT_REQUEST_TOOLBOXES.filter((toolbox) =>
    asked.has(toolbox)
  ).map((toolbox) => toolboxWords[toolbox])
  return read.length > 0
    ? `${capitalized(agentName)} may read ${listWords(read)}.`
    : undefined
}
