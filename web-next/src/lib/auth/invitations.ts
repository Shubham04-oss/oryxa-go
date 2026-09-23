import { apiRequest } from "@/lib/api/client"
import type { Role } from "./client"

export type Member = {
  organization_id: string
  user_id: string
  email: string
  display_name?: string
  role: Role
  joined_at: string
}

export type Invitation = {
  id: string
  organization_id: string
  email: string
  role: "admin" | "member"
  status: "pending" | "accepted" | "revoked" | "expired"
  created_at: string
  expires_at: string
}

export type ReceivedInvitation = {
  id: string
  organization_id: string
  organization_name: string
  role: "admin" | "member"
  created_at: string
  expires_at: string
}

const organizationPath = (organizationId: string) =>
  `/v1/organizations/${encodeURIComponent(organizationId)}`

export async function getMembers(organizationId: string): Promise<Member[]> {
  const result = await apiRequest<{ members: Member[] }>(
    `${organizationPath(organizationId)}/members`
  )
  return result.members
}

export async function getInvitations(
  organizationId: string
): Promise<Invitation[]> {
  const result = await apiRequest<{ invitations: Invitation[] }>(
    `${organizationPath(organizationId)}/invitations`
  )
  return result.invitations
}

export function sendInvitation(
  organizationId: string,
  email: string,
  role: "admin" | "member"
): Promise<Invitation> {
  return apiRequest(`${organizationPath(organizationId)}/invitations`, {
    body: { email, role },
  })
}

export function revokeInvitation(
  organizationId: string,
  invitationId: string
): Promise<void> {
  return apiRequest(
    `${organizationPath(organizationId)}/invitations/${encodeURIComponent(invitationId)}`,
    { method: "DELETE" }
  )
}

export function changeMemberRole(
  organizationId: string,
  userId: string,
  role: Role
): Promise<Member> {
  return apiRequest(
    `${organizationPath(organizationId)}/members/${encodeURIComponent(userId)}`,
    { method: "PATCH", body: { role } }
  )
}

export function removeOrganizationMember(
  organizationId: string,
  userId: string
): Promise<void> {
  return apiRequest(
    `${organizationPath(organizationId)}/members/${encodeURIComponent(userId)}`,
    { method: "DELETE" }
  )
}

export async function getReceivedInvitations(): Promise<ReceivedInvitation[]> {
  const result = await apiRequest<{ invitations: ReceivedInvitation[] }>(
    "/v1/invitations"
  )
  return result.invitations
}

export function acceptInvitationToken(token: string): Promise<Member> {
  return apiRequest("/v1/invitations/accept", { body: { token } })
}

export function acceptReceivedInvitation(
  invitationId: string
): Promise<Member> {
  return apiRequest(
    `/v1/invitations/${encodeURIComponent(invitationId)}/accept`,
    { method: "POST" }
  )
}
