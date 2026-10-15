import { apiRequest } from "@/lib/api/client"

const orgPath = (id: string) => `/v1/organizations/${encodeURIComponent(id)}`
const teamPath = (orgId: string, teamId: string) =>
  `${orgPath(orgId)}/teams/${encodeURIComponent(teamId)}`
const billingPath = (id: string) =>
  `/v1/billing/organizations/${encodeURIComponent(id)}`

export type Plan = {
  key: string
  member_limit: number | null
  member_limit_per_unit?: number
  min_quantity?: number
  max_quantity?: number
}
export type BillingSummary = {
  can_manage: boolean
  organization_id: string
  entitlements: {
    plan: string
    status: string
    member_limit: number | null
    grace_ends_at: string | null
  }
  subscription: {
    plan: string
    status: string
    quantity: number
    renews_at: string | null
    ends_at: string | null
  } | null
}
export const getPlans = () =>
  apiRequest<{ plans: Plan[]; unpaid: { member_limit: number | null } }>(
    "/v1/billing/plans"
  )
export const getBillingSummary = (id: string) =>
  apiRequest<BillingSummary>(billingPath(id))
export async function startCheckout(
  id: string,
  plan: string,
  quantity: number
) {
  const result = await apiRequest<{ checkout_url: string }>(
    `${billingPath(id)}/checkout`,
    { body: { plan, quantity } }
  )
  return result.checkout_url
}
export async function openBillingPortal(id: string) {
  const result = await apiRequest<{ portal_url: string }>(
    `${billingPath(id)}/portal`,
    { method: "POST" }
  )
  return result.portal_url
}

export type Team = {
  id: string
  name: string
  organization_id: string
  path?: string
  role?: "admin" | "member"
  can_manage: boolean
}
export type TeamMembership = {
  user_id: string
  email: string
  display_name?: string
  role: "admin" | "member"
  notify_email: boolean
}
export type TeamDetail = {
  team: Team
  membership: TeamMembership | null
  can_manage: boolean
}
/** A team a person is in, as the directory lists it. */
export type DirectoryTeamRef = {
  id: string
  name: string
  path?: string
  /** Set when the membership comes from a team nested inside. */
  member_path?: string
  role: "admin" | "member"
}
export type DirectoryPerson = {
  user_id: string
  email: string
  display_name?: string
  org_role: string
  teams: DirectoryTeamRef[] | null
}
export type Directory = {
  organization: { id: string; name: string }
  teams: Team[] | null
  members: DirectoryPerson[] | null
}
/**
 * `GET …/directory` (Teams): the organisation's visible team tree and its
 * people with the teams each is in, in one call instead of one per team.
 */
export function getDirectory(id: string) {
  return apiRequest<Directory>(`${orgPath(id)}/directory`)
}

export async function listTeams(id: string) {
  const result = await apiRequest<{ teams: Team[] }>(`${orgPath(id)}/teams`)
  return result.teams
}
export function getTeam(orgId: string, teamId: string) {
  return apiRequest<TeamDetail>(teamPath(orgId, teamId))
}
export async function listTeamMembers(orgId: string, teamId: string) {
  const result = await apiRequest<{ members: TeamMembership[] }>(
    `${teamPath(orgId, teamId)}/members`
  )
  return result.members
}
export function createTeam(orgId: string, name: string, parentId?: string) {
  return apiRequest<Team>(`${orgPath(orgId)}/teams`, {
    body: parentId ? { name, parent_id: parentId } : { name },
  })
}
export function renameTeam(orgId: string, teamId: string, name: string) {
  return apiRequest<Team>(teamPath(orgId, teamId), {
    method: "PATCH",
    body: { name },
  })
}
export function deleteTeam(orgId: string, teamId: string) {
  return apiRequest(teamPath(orgId, teamId), { method: "DELETE" })
}
export function addTeamMember(
  orgId: string,
  teamId: string,
  userId: string,
  role: "admin" | "member"
) {
  return apiRequest<TeamMembership>(`${teamPath(orgId, teamId)}/members`, {
    body: { user_id: userId, role },
  })
}
export function changeTeamMemberRole(
  orgId: string,
  teamId: string,
  userId: string,
  role: "admin" | "member"
) {
  return apiRequest<TeamMembership>(
    `${teamPath(orgId, teamId)}/members/${encodeURIComponent(userId)}`,
    { method: "PATCH", body: { role } }
  )
}
export function removeTeamMember(
  orgId: string,
  teamId: string,
  userId: string
) {
  return apiRequest(
    `${teamPath(orgId, teamId)}/members/${encodeURIComponent(userId)}`,
    { method: "DELETE" }
  )
}
export function setTeamEmailNotices(
  orgId: string,
  teamId: string,
  enabled: boolean
) {
  return apiRequest<TeamMembership>(`${teamPath(orgId, teamId)}/notification`, {
    method: "PATCH",
    body: { notify_email: enabled },
  })
}

export type Connection = {
  id: string
  toolkit: string
  member_id: string
  status: "active" | "pending" | "expired" | "failed" | "removed"
}
export async function listOrganizationConnections(id: string) {
  const result = await apiRequest<{ connections: Connection[] }>(
    `${orgPath(id)}/apps/connections?scope=organization`
  )
  return result.connections
}
