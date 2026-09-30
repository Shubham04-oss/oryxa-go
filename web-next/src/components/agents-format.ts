// Small, pure helpers shared by Agents mode's views and the request views:
// identities for the voxel avatars, status and time labels, and the copy for
// the agents service's refusals.
import { ApiError } from "@/lib/api/client"
import { isUnavailableHere } from "@/lib/agents/policy"
import type { AvatarIdentity } from "./workspace-avatar"

function initialsOf(name: string): string {
  return (
    name
      .split(/\s+/)
      .filter(Boolean)
      .slice(0, 2)
      .map((part) => part.charAt(0).toUpperCase())
      .join("") || "A"
  )
}

/**
 * An agent's voxel avatar, seeded by its id so it survives a rename and
 * matches the one the agent panel shows for the same agent.
 */
export function agentIdentity(agent: {
  id: string
  name: string
}): AvatarIdentity {
  return {
    id: agent.id,
    name: agent.name || "Agent",
    initials: initialsOf(agent.name),
    kind: "agent",
  }
}

export function statusLabel(status: "active" | "disabled"): string {
  return status === "active" ? "Active" : "Paused"
}

function formatDate(iso: string): string {
  const date = new Date(iso)
  if (Number.isNaN(date.getTime())) return ""
  return date.toLocaleDateString(undefined, {
    month: "short",
    day: "numeric",
    year: "numeric",
  })
}

/** "just now", "5 min ago", "3 h ago", "2 d ago", then the date. */
export function timeAgo(iso: string, now = Date.now()): string {
  const time = Date.parse(iso)
  if (Number.isNaN(time)) return ""
  const seconds = Math.max(0, Math.round((now - time) / 1000))
  if (seconds < 45) return "just now"
  const minutes = Math.round(seconds / 60)
  if (minutes < 60) return `${minutes} min ago`
  const hours = Math.round(minutes / 60)
  if (hours < 24) return `${hours} h ago`
  const days = Math.round(hours / 24)
  if (days < 7) return `${days} d ago`
  return formatDate(iso)
}

export function plural(count: number, one: string, many = `${one}s`) {
  return `${count} ${count === 1 ? one : many}`
}

/**
 * Copy for particular refusals, by the service's error code. `not_found`
 * and `unavailable_here` (a 503 meaning the feature is not set up on this
 * server) name their case too.
 */
export type ErrorCopy = Partial<Record<string, string>>

// The agents service prefixes some 400s with its sentinel ("invalid <thing>:
// <reason>"); the reason is what the member needs.
const sentinel = /^invalid [a-z ]+: /i

function sentence(message: string): string {
  const text = message.trim().replace(sentinel, "")
  if (!text) return ""
  const capitalised = text.charAt(0).toUpperCase() + text.slice(1)
  return /[.!?]$/.test(capitalised) ? capitalised : `${capitalised}.`
}

/**
 * What to tell the member about a failed agents call: the copy given for
 * its code, the service's own words for a request it refused (its 400s say
 * what to fix), and the fallback for anything else.
 */
export function agentsErrorMessage(
  error: unknown,
  fallback: string,
  copy: ErrorCopy = {}
): string {
  if (!(error instanceof ApiError)) return fallback
  const forCode = error.code ? copy[error.code] : undefined
  if (forCode) return forCode
  if (isUnavailableHere(error)) {
    return copy.unavailable_here ?? "This isn't set up on this server."
  }
  if (error.status === 401) return "Your session has ended. Sign in again."
  if (error.status === 404) return copy.not_found ?? "It no longer exists."
  if (error.status === 429) return "Too many requests. Try again in a minute."
  if (error.status >= 500) return fallback
  return sentence(error.message) || fallback
}
