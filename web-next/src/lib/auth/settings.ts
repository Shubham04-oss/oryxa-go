import { z } from "zod"
import { apiRequest } from "@/lib/api/client"
import type { Organization } from "./client"

function organizationPath(id: string) {
  return `/v1/organizations/${encodeURIComponent(id)}`
}

function slugify(value: string) {
  return value
    .trim()
    .toLowerCase()
    .replace(/[^a-z0-9]+/g, "-")
    .replace(/^-+|-+$/g, "")
}

export const organizationProfileSchema = z
  .object({
    name: z.string().trim().min(1, "Enter an organisation name.").max(120),
    slug: z.string(),
  })
  .transform(({ name, slug }) => ({ name, slug: slugify(slug || name) }))
  .refine(({ slug }) => slug.length >= 2 && slug.length <= 63, {
    message: "Use 2 to 63 letters or numbers.",
    path: ["slug"],
  })

export type OrganizationProfile = z.output<typeof organizationProfileSchema>

/** Name and slug are always sent; `domains`, when given, replaces the list. */
export function updateOrganization(
  id: string,
  input: OrganizationProfile & { domains?: string[] }
) {
  return apiRequest<Organization>(organizationPath(id), {
    method: "PATCH",
    body: input,
  })
}

export function deleteOrganization(id: string) {
  return apiRequest(organizationPath(id), { method: "DELETE" })
}

export type AppPreferences = { app_updates: boolean }

export function getAppPreferences(id: string) {
  return apiRequest<AppPreferences>(`${organizationPath(id)}/preferences`)
}

export function setAppUpdates(id: string, enabled: boolean) {
  return apiRequest<AppPreferences>(`${organizationPath(id)}/preferences`, {
    method: "PUT",
    body: { app_updates: enabled },
  })
}

export type AgentGrant = { granted: boolean; created_at?: string }

export function getAgentGrant() {
  return apiRequest<AgentGrant>("/v1/agent-grant")
}

export function authorizeAgentGrant(returnTo: string) {
  return apiRequest<{ authorization_url: string }>(
    "/v1/agent-grant/authorize",
    { body: { return_to: returnTo } }
  )
}

export function revokeAgentGrant() {
  return apiRequest("/v1/agent-grant", { method: "DELETE" })
}
