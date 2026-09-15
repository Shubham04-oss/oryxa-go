import type {
	InviteMemberInput,
	OrganizationInput,
	SignInInput,
	SignUpInput,
} from "./schemas";

interface ErrorBody {
	error?: string;
}

export class AuthRequestError extends Error {
	readonly status: number;

	constructor(message: string, status: number) {
		super(message);
		this.name = "AuthRequestError";
		this.status = status;
	}
}

function authEndpoint(path: string): string {
	const configuredBase = import.meta.env.VITE_AUTH_API_URL;
	const base =
		configuredBase ?? (import.meta.env.DEV ? "http://localhost:8080" : "");
	return `${base.replace(/\/$/, "")}${path}`;
}

async function post(path: string, body: unknown): Promise<Response> {
	const response = await fetch(authEndpoint(path), {
		method: "POST",
		headers: { "Content-Type": "application/json" },
		credentials: "include",
		body: JSON.stringify(body),
	});

	if (!response.ok) {
		let errorBody: ErrorBody = {};
		try {
			errorBody = (await response.json()) as ErrorBody;
		} catch {
			// The status still gives callers a stable failure when the body is unavailable.
		}
		throw new AuthRequestError(
			errorBody.error ?? "Request failed",
			response.status,
		);
	}

	return response;
}

export async function signIn(input: SignInInput): Promise<void> {
	await post("/v1/auth/password/login", input);
}

export async function signUp(input: SignUpInput): Promise<void> {
	await post("/v1/auth/password/signup", {
		display_name: input.displayName,
		email: input.email,
		password: input.password,
	});
}

export interface Organization {
	id: string;
	name: string;
	slug: string;
}

export async function createOrganization(
	input: OrganizationInput,
): Promise<Organization> {
	const response = await post("/v1/organizations", {
		name: input.name,
		slug: "",
	});
	return (await response.json()) as Organization;
}

export async function inviteMember(
	organizationId: string,
	input: InviteMemberInput,
): Promise<void> {
	await post(
		`/v1/organizations/${encodeURIComponent(organizationId)}/invitations`,
		input,
	);
}
