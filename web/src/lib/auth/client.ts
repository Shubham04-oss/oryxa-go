import { ApiError, apiRequest } from "../api/client";
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

export interface Session {
	organizations: Organization[];
	user: User;
}

export interface OrganizationDetail {
	membership: Membership;
	organization: Organization;
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

export function getOrganization(
	organizationId: string,
): Promise<OrganizationDetail> {
	return apiRequest<OrganizationDetail>(
		`/v1/organizations/${encodeURIComponent(organizationId)}`,
	);
}

export async function inviteMember(
	organizationId: string,
	input: InviteMemberInput,
): Promise<void> {
	await apiRequest(
		`/v1/organizations/${encodeURIComponent(organizationId)}/invitations`,
		{ body: input },
	);
}

export function acceptInvitation(token: string): Promise<Membership> {
	return apiRequest<Membership>("/v1/invitations/accept", {
		body: { token },
	});
}
