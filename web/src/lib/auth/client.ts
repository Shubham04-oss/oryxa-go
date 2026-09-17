import { ApiError, apiPath, apiRequest } from "../api/client";
import type {
	InviteMemberInput,
	OrganizationInput,
	SignInInput,
	SignUpInput,
} from "./schemas";

export type Role = "owner" | "admin" | "member";

export interface User {
	avatar_url?: string;
	created_at: string;
	display_name?: string;
	email: string;
	id: string;
	updated_at: string;
}

export interface Organization {
	created_at: string;
	created_by: string;
	id: string;
	name: string;
	slug: string;
	updated_at: string;
}

export interface Membership {
	display_name?: string;
	email: string;
	joined_at: string;
	organization_id: string;
	role: Role;
	user_id: string;
}

export interface Invitation {
	accepted_at?: string;
	created_at: string;
	email: string;
	expires_at: string;
	id: string;
	invited_by: string;
	organization_id: string;
	role: Role;
	status: "pending" | "accepted" | "revoked" | "expired";
}

/** A pending invitation, as the person it was sent to sees it. */
export interface ReceivedInvitation {
	created_at: string;
	expires_at: string;
	id: string;
	organization_id: string;
	organization_name: string;
	role: Role;
}

export interface Session {
	organizations: Organization[];
	user: User;
}

export interface OrganizationDetail {
	membership: Membership;
	organization: Organization;
}

/** Owners and admins manage members, though admins only ever manage members. */
export function managesMembers(role: Role | undefined): boolean {
	return role === "owner" || role === "admin";
}

// A 401 from these calls is their answer, such as a wrong password, rather
// than an expired session worth refreshing.
const publicAuth = { refreshSession: false } as const;

/** Auth's message when it suits a person; the fallback when it cannot. */
export function authErrorMessage(error: unknown, fallback: string): string {
	if (!(error instanceof ApiError) || error.status >= 500) {
		return fallback;
	}
	if (error.status === 429) {
		return "Too many attempts. Try again in a minute.";
	}
	return error.message;
}

export async function signIn(input: SignInInput): Promise<void> {
	await apiRequest("/v1/auth/password/login", { ...publicAuth, body: input });
}

export async function signUp(input: SignUpInput): Promise<void> {
	await apiRequest("/v1/auth/password/signup", {
		...publicAuth,
		body: {
			display_name: input.displayName,
			email: input.email,
			password: input.password,
		},
	});
}

export async function signOut(): Promise<void> {
	await apiRequest("/v1/auth/logout", { ...publicAuth, method: "POST" });
}

export async function verifyEmail(token: string): Promise<void> {
	await apiRequest("/v1/auth/password/verify", {
		...publicAuth,
		body: { token },
	});
}

export async function resendVerification(email: string): Promise<void> {
	await apiRequest("/v1/auth/password/resend-verification", {
		...publicAuth,
		body: { email },
	});
}

export async function requestPasswordReset(email: string): Promise<void> {
	await apiRequest("/v1/auth/password/forgot", {
		...publicAuth,
		body: { email },
	});
}

export async function resetPassword(
	token: string,
	password: string,
): Promise<void> {
	await apiRequest("/v1/auth/password/reset", {
		...publicAuth,
		body: { password, token },
	});
}

export function getSession(): Promise<Session> {
	return apiRequest<Session>("/v1/auth/me");
}

export function createOrganization(
	input: OrganizationInput,
): Promise<Organization> {
	return apiRequest<Organization>("/v1/organizations", {
		body: { name: input.name, slug: "" },
	});
}

function organizationPath(organizationId: string): string {
	return apiPath`/v1/organizations/${organizationId}`;
}

export function getOrganization(
	organizationId: string,
): Promise<OrganizationDetail> {
	return apiRequest<OrganizationDetail>(organizationPath(organizationId));
}

export function updateOrganization(
	organizationId: string,
	input: { name: string; slug: string },
): Promise<Organization> {
	return apiRequest<Organization>(organizationPath(organizationId), {
		body: input,
		method: "PATCH",
	});
}

export async function deleteOrganization(
	organizationId: string,
): Promise<void> {
	await apiRequest(organizationPath(organizationId), { method: "DELETE" });
}

export async function listMembers(
	organizationId: string,
): Promise<Membership[]> {
	const { members } = await apiRequest<{ members: Membership[] }>(
		`${organizationPath(organizationId)}/members`,
	);
	return members;
}

export function changeMemberRole(
	organizationId: string,
	userId: string,
	role: Role,
): Promise<Membership> {
	return apiRequest<Membership>(
		`${organizationPath(organizationId)}${apiPath`/members/${userId}`}`,
		{ body: { role }, method: "PATCH" },
	);
}

/** Removes a member, or leaves the organization when `userId` is the caller. */
export async function removeMember(
	organizationId: string,
	userId: string,
): Promise<void> {
	await apiRequest(
		`${organizationPath(organizationId)}${apiPath`/members/${userId}`}`,
		{ method: "DELETE" },
	);
}

export async function listInvitations(
	organizationId: string,
): Promise<Invitation[]> {
	const { invitations } = await apiRequest<{ invitations: Invitation[] }>(
		`${organizationPath(organizationId)}/invitations`,
	);
	return invitations;
}

export function inviteMember(
	organizationId: string,
	input: InviteMemberInput,
): Promise<Invitation> {
	return apiRequest<Invitation>(
		`${organizationPath(organizationId)}/invitations`,
		{ body: input },
	);
}

export async function revokeInvitation(
	organizationId: string,
	invitationId: string,
): Promise<void> {
	await apiRequest(
		`${organizationPath(organizationId)}${apiPath`/invitations/${invitationId}`}`,
		{ method: "DELETE" },
	);
}

export function acceptInvitation(token: string): Promise<Membership> {
	return apiRequest<Membership>("/v1/invitations/accept", {
		body: { token },
	});
}

/**
 * Invitations sent to the signed-in member's own email, from organizations
 * they have not joined, whether or not they ever opened the emailed link.
 */
export async function listReceivedInvitations(): Promise<ReceivedInvitation[]> {
	const { invitations } = await apiRequest<{
		invitations: ReceivedInvitation[];
	}>("/v1/invitations");
	return invitations;
}

export function acceptReceivedInvitation(
	invitationId: string,
): Promise<Membership> {
	return apiRequest<Membership>(
		apiPath`/v1/invitations/${invitationId}/accept`,
		{ method: "POST" },
	);
}
