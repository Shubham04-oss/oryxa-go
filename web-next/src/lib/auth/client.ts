import {
  ApiError,
  apiRequest,
  apiUrl,
  resumeSessionAfterSignIn,
} from "@/lib/api/client"
import type { SignInInput, SignUpInput } from "./schemas"

export type Role = "owner" | "admin" | "member"

export type User = {
  id: string
  email: string
  display_name?: string
  avatar_url?: string
}

export type Organization = {
  id: string
  name: string
  slug: string
  /** The organisation's own email domains; Go always sends them, older caches may not. */
  domains?: string[]
  timezone?: string
  guests_allowed?: boolean
  /** The user id of whoever created it. */
  created_by?: string
  created_at?: string
}

export type Session = {
  user: User
  organizations: Organization[]
}

type AuthConfig = { identity_providers?: string[] }

const publicRequest = { refreshSession: false } as const

export function authErrorMessage(error: unknown, fallback: string): string {
  if (!(error instanceof ApiError) || error.status >= 500) return fallback
  if (error.status === 429) return "Too many attempts. Try again in a minute."
  return error.message
}

export function getAuthConfig(): Promise<AuthConfig> {
  return apiRequest<AuthConfig>("/v1/auth/config", publicRequest)
}

export async function signIn(input: SignInInput): Promise<void> {
  await apiRequest("/v1/auth/password/login", {
    ...publicRequest,
    body: input,
  })
  resumeSessionAfterSignIn()
}

export async function signUp(input: SignUpInput): Promise<void> {
  await apiRequest("/v1/auth/password/signup", {
    ...publicRequest,
    body: {
      display_name: input.displayName,
      email: input.email,
      password: input.password,
    },
  })
}

export async function verifyEmail(token: string): Promise<void> {
  await apiRequest("/v1/auth/password/verify", {
    ...publicRequest,
    body: { token },
  })
}

export async function resendVerification(email: string): Promise<void> {
  await apiRequest("/v1/auth/password/resend-verification", {
    ...publicRequest,
    body: { email },
  })
}

export async function requestPasswordReset(email: string): Promise<void> {
  await apiRequest("/v1/auth/password/forgot", {
    ...publicRequest,
    body: { email },
  })
}

export async function resetPassword(
  token: string,
  password: string
): Promise<void> {
  await apiRequest("/v1/auth/password/reset", {
    ...publicRequest,
    body: { token, password },
  })
}

export function getSession(): Promise<Session> {
  return apiRequest<Session>("/v1/auth/me")
}

// Mirrors the server's slugify (auth/internal/application/service.go): lowercase,
// collapse runs of non "a-z0-9" into a single "-", then trim leading/trailing "-".
function slugify(value: string): string {
  return value
    .trim()
    .toLowerCase()
    .replace(/[^a-z0-9]+/g, "-")
    .replace(/^-+|-+$/g, "")
}

function organizationSlugBase(name: string): string {
  const slug = slugify(name).slice(0, 63).replace(/-+$/, "")
  return slug.length >= 2 ? slug : "workspace"
}

const slugSuffixAlphabet = "abcdefghijklmnopqrstuvwxyz0123456789"

// crypto.randomUUID() requires a secure context, which a plain-http LAN
// address does not have; getRandomValues works everywhere.
function randomSlugSuffix(): string {
  const bytes = new Uint8Array(6)
  crypto.getRandomValues(bytes)
  return Array.from(
    bytes,
    (byte) => slugSuffixAlphabet[byte % slugSuffixAlphabet.length]
  ).join("")
}

export async function createOrganization(name: string): Promise<Organization> {
  const base = organizationSlugBase(name)
  let slug = base
  for (let attempt = 0; ; attempt += 1) {
    try {
      return await apiRequest<Organization>("/v1/organizations", {
        body: { name, slug },
      })
    } catch (error) {
      if (
        !(error instanceof ApiError) ||
        error.status !== 409 ||
        attempt >= 2
      ) {
        throw error
      }
      slug = `${base.slice(0, 56)}-${randomSlugSuffix()}`
    }
  }
}

export function getOrganizationRole(organizationId: string): Promise<Role> {
  return apiRequest<{ membership: { role: Role } }>(
    `/v1/organizations/${encodeURIComponent(organizationId)}`
  ).then((result) => result.membership.role)
}

export async function signOut(): Promise<void> {
  await apiRequest("/v1/auth/logout", {
    ...publicRequest,
    method: "POST",
  })
}

export function googleSignIn(returnTo: string): void {
  const login = new URL(apiUrl("/v1/auth/login"), window.location.origin)
  login.searchParams.set(
    "return_to",
    new URL(returnTo, window.location.origin).toString()
  )
  window.location.assign(login.toString())
}
