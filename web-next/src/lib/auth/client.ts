import { ApiError, apiRequest, apiUrl } from "@/lib/api/client"
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

export function createOrganization(name: string): Promise<Organization> {
  return apiRequest<Organization>("/v1/organizations", {
    body: { name, slug: "" },
  })
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
