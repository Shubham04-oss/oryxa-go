import { z } from "zod"

// A specialist answers with an ADK output schema, so its final message's text
// is a JSON work result: agents/primary/internal/agents/managed.go
// `managedResultSchema`.

export type SpecialistResultStatus = "completed" | "needs_input" | "blocked"

/** Work the specialist suggests another agent take on. A suggestion only: nothing ran. */
export type SpecialistHandoff = { target: string; request: string }

export type SpecialistResult = {
  /** `completed` only when every action it needed succeeded; `needs_input` asks the member something; `blocked` names what stopped it. */
  status: SpecialistResultStatus
  /** The deliverable itself: the answer, findings, draft, or the question for the member. */
  summary: string
  /** Every tool it called, including failed calls. */
  tools_used: string[]
  /** Every file it saved. */
  artifacts: string[]
  handoffs: SpecialistHandoff[]
}

const resultSchema = z.object({
  status: z.enum(["completed", "needs_input", "blocked"]),
  summary: z.string(),
  tools_used: z.array(z.string()).catch([]),
  artifacts: z.array(z.string()).catch([]),
  handoffs: z
    .array(z.object({ target: z.string(), request: z.string() }))
    .catch([]),
})

/**
 * A specialist message's work result, or nothing for text that is not one
 * (an older answer, or a model that ignored the schema): show that text as
 * it is. The status and summary are required; a missing or malformed list
 * reads as empty.
 */
export function parseSpecialistResult(
  text: string
): SpecialistResult | undefined {
  const json = text
    .trim()
    .replace(/^```(?:json)?\s*/i, "")
    .replace(/\s*```$/, "")
  if (!json.startsWith("{")) return undefined
  let value: unknown
  try {
    value = JSON.parse(json)
  } catch {
    return undefined
  }
  const parsed = resultSchema.safeParse(value)
  return parsed.success ? parsed.data : undefined
}

/**
 * Whether a specialist conversation's "member" message is really the turn an
 * agent team's workroom wrote for it (agents/primary/internal/agentteamrooms
 * service.go): the team's purpose, the teammate's role and the recent group
 * chat, not words the member typed.
 */
export function isTeamWorkroomTurn(text: string): boolean {
  return (
    text.startsWith("You are ") &&
    text.includes(" collaborating in the member's agent team ") &&
    text.includes("Recent group chat follows.")
  )
}
