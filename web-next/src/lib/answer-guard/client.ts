// A member's own answer guard settings: how much of what their agents say the
// guard hides from whoever reads it. The handlers are in
// agents/primary/internal/httpserver/answer_guard.go, and the rule that
// combines the levels is in agents/primary/internal/guardpolicy.
//
// The guard reads an answer at one of two levels. `secrets` hides what it
// always hides: passwords, keys, and card, bank and ID numbers. `strict` also
// holds emails, phone numbers and addresses. A level applies to an origin: an
// answer the member started themselves (`owner`), or one another member reads
// (`peer`). For each origin Go says what the member chose (`chosen`, null when
// they chose nothing), the level that stands in for that (`default`), the
// least their organization allows (`floor`), and what applies (`effective`):
// the stricter of the floor and the choice, or of the floor and the default
// when there is none. A choice below the floor is accepted and kept, and the
// floor wins.
//
// The routes are the member's own, in their own session: an agent's token is
// refused (403), and nothing in a request says whose settings they are. What
// comes back from them:
// - 404, or 503 `answer_guard_unavailable`, means this deployment does not
//   serve the settings (yet, or at all). Both are answered as
//   `{ available: false }` rather than thrown, so a page can render nothing
//   instead of an error.
// - 400 (invalid input), 401 (the session ended; `apiRequest` has already
//   tried one refresh), 403 and anything else are thrown as `ApiError`.
// - A 200 that is not JSON, or holds a shape or a value this page does not
//   know, is an `AnswerGuardResponseError`: the page shows an error state
//   rather than guess.
import { queryOptions } from "@tanstack/react-query"
import { z } from "zod"
import { ApiError, apiRequest } from "@/lib/api/client"
import { organizationPath, pick } from "@/lib/agents/http"
import type { ReadOptions } from "@/lib/agents/http"

const levelSchema = z.enum(["secrets", "strict"])
const originSchema = z.object({
  chosen: levelSchema.nullable(),
  default: levelSchema,
  floor: levelSchema,
  effective: levelSchema,
})
// Fields Go may add later are dropped, but every field named here must be
// there, and hold a value this page knows.
const preferencesSchema = z.object({
  owner: originSchema,
  peer: originSchema,
  relevance: z.enum(["hold", "shadow", "off"]),
  regulated: z.boolean(),
})

/** How much the guard hides: `secrets` (the least) or `strict` (also personal details). */
export type GuardLevel = z.infer<typeof levelSchema>

/** Whose answer a level applies to: one the member started (`owner`) or one another member reads (`peer`). */
export const guardOrigins = ["owner", "peer"] as const
export type GuardOrigin = (typeof guardOrigins)[number]

/** What one origin's level is, and why. */
export type GuardOriginPreference = z.infer<typeof originSchema>

export type AnswerGuardPreferences = z.infer<typeof preferencesSchema>

/** How the relevance check treats an answer that does not fit what was asked. */
export type GuardRelevance = AnswerGuardPreferences["relevance"]

/**
 * What to change. A key left out (or undefined) leaves that origin as it is;
 * null clears the member's choice, back to its default.
 */
export type AnswerGuardChange = {
  owner?: GuardLevel | null
  peer?: GuardLevel | null
}

/** The settings, or that this server does not serve them. */
export type AnswerGuardResult =
  | { available: true; preferences: AnswerGuardPreferences }
  | { available: false }

/** Go answered 200, but not with something this page can read. */
export class AnswerGuardResponseError extends Error {
  constructor(message: string, options?: { cause?: unknown }) {
    super(message, options)
    this.name = "AnswerGuardResponseError"
  }
}

const strictness: Record<GuardLevel, number> = { secrets: 0, strict: 1 }

/** Whether `level` hides less than `floor` requires. Go accepts such a choice, but the floor wins. */
export function isBelowFloor(level: GuardLevel, floor: GuardLevel): boolean {
  return strictness[level] < strictness[floor]
}

/**
 * What applies to an origin: the stricter of its floor and the member's
 * choice, or of the floor and the default when there is none. Go works this
 * out (`guardpolicy.Resolve`); a page repeats it only to show a change
 * before Go has confirmed it.
 */
export function effectiveLevel({
  chosen,
  default: standIn,
  floor,
}: Pick<GuardOriginPreference, "chosen" | "default" | "floor">): GuardLevel {
  const wanted = chosen ?? standIn
  return isBelowFloor(wanted, floor) ? floor : wanted
}

/** The settings as they will read once `change` is saved, without changing the ones given. */
export function applyAnswerGuardChange(
  preferences: AnswerGuardPreferences,
  change: AnswerGuardChange
): AnswerGuardPreferences {
  const next = { ...preferences }
  for (const origin of guardOrigins) {
    const chosen = change[origin]
    if (chosen === undefined) continue
    const changed = { ...preferences[origin], chosen }
    next[origin] = { ...changed, effective: effectiveLevel(changed) }
  }
  return next
}

function readPreferences(body: unknown): AnswerGuardPreferences {
  const parsed = preferencesSchema.safeParse(body)
  if (!parsed.success) {
    throw new AnswerGuardResponseError(
      "The answer guard's settings came back in a form this page does not know.",
      { cause: parsed.error }
    )
  }
  return parsed.data
}

function notServed(error: unknown): boolean {
  return (
    error instanceof ApiError &&
    (error.status === 404 ||
      (error.status === 503 && error.code === "answer_guard_unavailable"))
  )
}

async function call(
  organizationId: string,
  init: { body?: unknown; method?: "PUT"; signal?: AbortSignal }
): Promise<AnswerGuardResult> {
  let body: unknown
  try {
    body = await apiRequest<unknown>(
      organizationPath(organizationId, "answer-guard", "preferences"),
      init
    )
  } catch (error) {
    if (notServed(error)) return { available: false }
    // `Response.json()` rejects with a SyntaxError for a body that is not JSON.
    if (error instanceof Error && error.name === "SyntaxError") {
      throw new AnswerGuardResponseError(
        "The answer guard's settings did not come back as JSON.",
        { cause: error }
      )
    }
    throw error
  }
  return { available: true, preferences: readPreferences(body) }
}

/** `GET …/answer-guard/preferences`: the calling member's own settings. */
export function getAnswerGuardPreferences(
  organizationId: string,
  { signal }: ReadOptions = {}
): Promise<AnswerGuardResult> {
  return call(organizationId, { signal })
}

/**
 * `PUT …/answer-guard/preferences`: changes the calling member's own
 * settings and answers what they now are. Only the keys `change` names are
 * sent (Go refuses any other), so a page that passes a whole settings object
 * still sends nothing it did not mean to change.
 */
export function setAnswerGuardPreferences(
  organizationId: string,
  change: AnswerGuardChange
): Promise<AnswerGuardResult> {
  return call(organizationId, {
    method: "PUT",
    body: pick(change, guardOrigins),
  })
}

export const answerGuardKeys = {
  preferences: (organizationId: string) =>
    ["settings", "answer-guard", organizationId] as const,
}

/** The member's settings for one organization. They change only through this page, so a minute of staleness is fine. */
export function answerGuardPreferencesQueryOptions(organizationId: string) {
  return queryOptions({
    queryKey: answerGuardKeys.preferences(organizationId),
    queryFn: ({ signal }) =>
      getAnswerGuardPreferences(organizationId, { signal }),
    retry: false,
    staleTime: 60_000,
  })
}
