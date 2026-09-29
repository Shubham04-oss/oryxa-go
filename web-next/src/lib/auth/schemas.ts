import { z } from "zod"

// Trimmed before the format is checked: Zod runs a string format's own check
// ahead of `.trim()` on the same schema, so `z.email().trim()` refused an
// address with a stray space (from a paste, or a phone's keyboard) instead of
// cleaning it.
const emailMessage = "Enter a valid email"
const email = z.string(emailMessage).trim().pipe(z.email(emailMessage))
// Cognito's allowed special characters, plus a space (which Cognito also
// accepts as long as it isn't leading or trailing — checked separately below).
const passwordSymbol = /[$^*.[\]{}()?"!@#%&/\\,><':;|_~`=+ -]/
const password = z
  .string()
  .min(12, "Use at least 12 characters")
  .regex(/[a-z]/, "Add a lowercase letter")
  .regex(/[A-Z]/, "Add an uppercase letter")
  .regex(/[0-9]/, "Add a number")
  .regex(passwordSymbol, "Add a symbol")
  .refine(
    (value) => !/^\s|\s$/.test(value),
    "Remove spaces at the start or end"
  )

export const signInSchema = z.object({
  email,
  password: z.string().min(1, "Enter your password"),
})

export const signUpSchema = z.object({
  displayName: z.string().trim().min(2, "Enter your name").max(80),
  email,
  password,
})

export const emailSchema = z.object({ email })
export const resetPasswordSchema = z.object({ password })
export const invitationSchema = z.object({
  email,
  role: z.enum(["admin", "member"]),
})

export type SignInInput = z.infer<typeof signInSchema>
export type SignUpInput = z.infer<typeof signUpSchema>

export function fieldErrors(
  issues: Array<{ path: PropertyKey[]; message: string }>
): Record<string, string> {
  const errors: Record<string, string> = {}
  for (const issue of issues) {
    const field = issue.path[0]
    if (typeof field === "string" && !Object.hasOwn(errors, field)) {
      errors[field] = issue.message
    }
  }
  return errors
}

export function safeRedirect(value: unknown): string | undefined {
  if (typeof value !== "string" || !value.startsWith("/")) return undefined
  const base = "http://app.invalid"
  try {
    const url = new URL(value, base)
    if (url.origin !== base) return undefined
    const path = `${url.pathname}${url.search}${url.hash}`
    // URL normalisation can turn inputs like "/.//evil.com" into a
    // protocol-relative path; reject anything that still looks like one.
    return path.startsWith("//") || path.startsWith("/\\") ? undefined : path
  } catch {
    return undefined
  }
}

export function searchToken(value: unknown): string | undefined {
  return typeof value === "string" && value.trim() ? value : undefined
}
