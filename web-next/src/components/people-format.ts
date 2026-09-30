// How a person is named on screen: their initials and avatar identity, and
// what they are called in a sentence. Pure, so the views and their tests
// share one set of rules.
import type { AvatarIdentity } from "./workspace-avatar"

export function initialsOf(name: string): string {
  const letters = name
    .trim()
    .split(/\s+/)
    .slice(0, 2)
    .map((part) => part.charAt(0).toUpperCase())
    .join("")
  return letters || "?"
}

/** A person: Lorelei art, varied by their user id so it follows them everywhere. */
export function personIdentity(userId: string, name: string): AvatarIdentity {
  return { id: userId, name, initials: initialsOf(name), kind: "user" }
}

/** What a person is called in a greeting: the first word of their name, or all of an email address. */
export function firstName(name: string): string {
  const trimmed = name.trim()
  if (trimmed.includes("@")) return trimmed
  return trimmed.split(/\s+/)[0] || trimmed
}
