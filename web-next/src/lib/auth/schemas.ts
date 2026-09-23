import { z } from "zod"

const email = z.email("Enter a valid email").trim()
const password = z
  .string()
  .min(12, "Use at least 12 characters")
  .regex(/[a-z]/, "Add a lowercase letter")
  .regex(/[A-Z]/, "Add an uppercase letter")
  .regex(/[0-9]/, "Add a number")
  .regex(/[^A-Za-z0-9]/, "Add a symbol")

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
    return url.origin === base
      ? `${url.pathname}${url.search}${url.hash}`
      : undefined
  } catch {
    return undefined
  }
}

export function searchToken(value: unknown): string | undefined {
  return typeof value === "string" && value.trim() ? value : undefined
}
