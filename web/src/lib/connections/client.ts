import { queryOptions } from "@tanstack/react-query";
import { apiPath, apiRequest } from "../api/client";

export type CredentialType =
	| "oauth2"
	| "oauth2_client_credentials"
	| "installation"
	| "api_key"
	| "bearer"
	| "basic";

export type ConnectionScope = "member" | "team" | "organization";

export interface App {
	/** Only organization admins see it; no one sees the secret. */
	client_id?: string;
	created_at: string;
	created_by: string;
	credential_type: CredentialType;
	id: string;
	organization_id: string;
	provider: string;
	scopes: string[];
	settings?: {
		domain?: string;
		github_app_id?: string;
		github_app_slug?: string;
	};
	status: "active" | "disabled";
	updated_at: string;
}

export interface Connection {
	app_id: string;
	/** Set on a live connection whose app is disabled. */
	blocked?: string;
	connected_at: string;
	created_by: string;
	credential_type: CredentialType;
	default: boolean;
	health: { checked_at?: string; state: "unknown" | "healthy" | "degraded" };
	id: string;
	label?: string;
	last_used_at?: string;
	member_id?: string;
	organization_id: string;
	provider: string;
	provider_account: { id: string; name?: string };
	reason?: string;
	scope: ConnectionScope;
	scopes: string[];
	status: "active" | "needs_reconnect" | "disconnected";
	status_changed_at: string;
	team_id?: string;
	updated_at: string;
	usage: "members" | "admins";
}

export interface Authorization {
	authorization_url: string;
	expires_at: string;
}

export interface CredentialInput {
	api_key?: string;
	password?: string;
	tenant?: string;
	token?: string;
	username?: string;
}

export interface CreateAppInput {
	client_id?: string;
	client_secret?: string;
	credential_type: CredentialType;
	domain?: string;
	github_app_id?: string;
	github_app_slug?: string;
	private_key?: string;
	provider: string;
	scopes?: string[];
}

/** What an app's domain must look like, where its provider needs one. */
export interface DomainRule {
	/** What an app that names no domain gets. */
	default?: string;
	example: string;
	required: boolean;
}

/** One way to enable a provider, as connections describes it. */
export interface CredentialTypeDescription {
	/** Every scope an app may allow. Empty where the type carries none. */
	allowed_scopes?: string[];
	/** Members connect through a provider redirect rather than a secret. */
	authorizes: boolean;
	/** The app carries a client the organization registers with the provider. */
	client: boolean;
	credential_type: CredentialType;
	domain?: DomainRule;
	/** Scopes that may be allowed only on their own. */
	exclusive_scopes?: string[];
	/** The credential fields a connection supplies, such as username and password. */
	fields?: string[];
	required_scopes?: string[];
}

export interface ProviderDescription {
	/** The ways to enable it, the first being the default. */
	credential_types: CredentialTypeDescription[];
	provider: string;
}

export interface Placement {
	scope: ConnectionScope;
	team_id?: string;
}

function appsPath(organizationId: string): string {
	return apiPath`/v1/organizations/${organizationId}/apps`;
}

function connectionsPath(organizationId: string): string {
	return apiPath`/v1/organizations/${organizationId}/connections`;
}

export async function listApps(organizationId: string): Promise<App[]> {
	const { apps } = await apiRequest<{ apps: App[] }>(appsPath(organizationId));
	return apps;
}

export function createApp(
	organizationId: string,
	input: CreateAppInput,
): Promise<App> {
	return apiRequest<App>(appsPath(organizationId), { body: input });
}

export function setAppEnabled(
	organizationId: string,
	appId: string,
	enabled: boolean,
): Promise<App> {
	return apiRequest<App>(
		`${appsPath(organizationId)}${apiPath`/${appId}`}/${enabled ? "enable" : "disable"}`,
		{ method: "POST" },
	);
}

/** Removes the app and disconnects every connection made through it. */
export async function deleteApp(
	organizationId: string,
	appId: string,
): Promise<void> {
	await apiRequest(`${appsPath(organizationId)}${apiPath`/${appId}`}`, {
		method: "DELETE",
	});
}

export async function listConnections(
	organizationId: string,
): Promise<Connection[]> {
	const { connections } = await apiRequest<{ connections: Connection[] }>(
		connectionsPath(organizationId),
	);
	return connections;
}

export function authorizeConnection(
	organizationId: string,
	input: Placement & { app_id: string; return_uri: string },
): Promise<Authorization> {
	return apiRequest<Authorization>(
		`${connectionsPath(organizationId)}/authorize`,
		{ body: input },
	);
}

export function completeConnection(
	organizationId: string,
	sessionId: string,
): Promise<Connection> {
	return apiRequest<Connection>(`${connectionsPath(organizationId)}/complete`, {
		body: { session_id: sessionId },
	});
}

export function createKeyConnection(
	organizationId: string,
	input: Placement & {
		app_id: string;
		credential: CredentialInput;
		label?: string;
	},
): Promise<Connection> {
	return apiRequest<Connection>(connectionsPath(organizationId), {
		body: input,
	});
}

export function updateConnection(
	organizationId: string,
	connectionId: string,
	input: { default?: true; label?: string },
): Promise<Connection> {
	return apiRequest<Connection>(
		`${connectionsPath(organizationId)}${apiPath`/${connectionId}`}`,
		{ body: input, method: "PATCH" },
	);
}

export function reconnectConnection(
	organizationId: string,
	connectionId: string,
	returnUri: string,
): Promise<Authorization> {
	return apiRequest<Authorization>(
		`${connectionsPath(organizationId)}${apiPath`/${connectionId}`}/reconnect`,
		{ body: { return_uri: returnUri } },
	);
}

export function replaceConnectionCredential(
	organizationId: string,
	connectionId: string,
	credential: CredentialInput,
): Promise<Connection> {
	return apiRequest<Connection>(
		`${connectionsPath(organizationId)}${apiPath`/${connectionId}`}/credential`,
		{ body: { credential }, method: "PUT" },
	);
}

export async function disconnectConnection(
	organizationId: string,
	connectionId: string,
): Promise<void> {
	await apiRequest(
		`${connectionsPath(organizationId)}${apiPath`/${connectionId}`}`,
		{ method: "DELETE" },
	);
}

/** The prefix of everything cached from connections about one organization. */
export function connectionsQueryKey(organizationId: string) {
	return ["connections", organizationId] as const;
}

export function appsQueryOptions(organizationId: string) {
	return queryOptions({
		queryFn: () => listApps(organizationId),
		queryKey: [...connectionsQueryKey(organizationId), "apps"],
	});
}

export function connectionsQueryOptions(organizationId: string) {
	return queryOptions({
		queryFn: () => listConnections(organizationId),
		queryKey: [...connectionsQueryKey(organizationId), "list"],
	});
}

export async function listProviders(): Promise<ProviderDescription[]> {
	const { providers } = await apiRequest<{ providers: ProviderDescription[] }>(
		"/v1/providers",
	);
	return providers;
}

/**
 * The catalogue changes only when connections is deployed, so it is kept for
 * the session rather than refetched with every tools page.
 */
export function providersQueryOptions() {
	return queryOptions({
		queryFn: listProviders,
		queryKey: ["connections", "providers"] as const,
		staleTime: Number.POSITIVE_INFINITY,
	});
}

export type ConnectMethod = "consent" | "key";

/** Key-based tools take a secret the member pastes; the rest go to the provider. */
export function connectMethod(credentialType: CredentialType): ConnectMethod {
	return credentialType === "api_key" ||
		credentialType === "bearer" ||
		credentialType === "basic"
		? "key"
		: "consent";
}

/** A pasted secret, placed where connections expects it for the tool's type. */
export function keyCredential(
	credentialType: CredentialType,
	secret: string,
	username = "",
): CredentialInput {
	switch (credentialType) {
		case "api_key":
			return { api_key: secret };
		case "basic":
			return { password: secret, username };
		default:
			return { token: secret };
	}
}

/** Where the provider sends the browser back, naming the organization it was for. */
export function connectReturnUri(
	origin: string,
	organizationId: string,
): string {
	const url = new URL("/app/connections", origin);
	url.searchParams.set("organization", organizationId);
	return url.toString();
}

const failures: Record<string, string> = {
	installation_pending:
		"The GitHub App installation is waiting for an owner of that GitHub organization to approve it.",
	provider_denied: "The provider did not allow the connection.",
	session_expired: "That connection attempt expired. Start again.",
};

/** Why a connect flow the provider sent back did not finish. */
export function connectFailure(code: string): string {
	return failures[code] ?? "The connection did not finish. Start again.";
}

const reasons: Record<string, string> = {
	account_changed: "The provider returned a different account.",
	app_deleted: "Its tool was removed from the organization.",
	client_changed: "The tool's OAuth app changed.",
	credential_rejected: "The provider rejected the key.",
	installation_removed: "The GitHub App was uninstalled.",
	member_left: "Its owner left the organization.",
	organization_deleted: "The organization was deleted.",
	refresh_rejected: "The provider stopped accepting its access.",
	team_deleted: "Its team was deleted.",
	unauthorized: "The provider revoked its access.",
};

/** What stops `connection` from working, if anything. */
export function connectionProblem(connection: Connection): string | undefined {
	if (connection.status === "active") {
		return connection.blocked === "app_disabled"
			? "Its tool is disabled for the organization."
			: undefined;
	}
	const reason = connection.reason ? reasons[connection.reason] : undefined;
	if (reason) {
		return reason;
	}
	return connection.status === "needs_reconnect"
		? "Reconnect it to keep using it."
		: "It was disconnected.";
}

export interface ConnectionViewer {
	isOrgAdmin: boolean;
	manageableTeams: { id: string; name: string }[];
	userId: string;
}

export interface PlacementOption extends Placement {
	label: string;
	value: string;
}

/** Where the viewer may put a new connection to a tool of this type. */
export function placementOptions(
	credentialType: CredentialType,
	viewer: ConnectionViewer,
): PlacementOption[] {
	const options: PlacementOption[] = [];
	// No person consents to client credentials, so they cannot belong to a member.
	if (credentialType !== "oauth2_client_credentials") {
		options.push({ label: "Just me", scope: "member", value: "member" });
	}
	for (const team of viewer.manageableTeams) {
		options.push({
			label: `Team: ${team.name}`,
			scope: "team",
			team_id: team.id,
			value: `team:${team.id}`,
		});
	}
	if (viewer.isOrgAdmin) {
		options.push({
			label: "The whole organization",
			scope: "organization",
			value: "organization",
		});
	}
	return options;
}

function changesConnection(
	connection: Connection,
	viewer: ConnectionViewer,
): boolean {
	switch (connection.scope) {
		case "member":
			return connection.member_id === viewer.userId;
		case "team":
			return (
				viewer.isOrgAdmin ||
				viewer.manageableTeams.some((team) => team.id === connection.team_id)
			);
		default:
			return viewer.isOrgAdmin;
	}
}

/** What connections lets `viewer` do with `connection`; the service checks again. */
export function connectionAccess(
	connection: Connection,
	viewer: ConnectionViewer,
): { change: boolean; disconnect: boolean } {
	const change = changesConnection(connection, viewer);
	// Organization admins may disconnect a member's connection, never use it.
	return {
		change,
		disconnect: change || (connection.scope === "member" && viewer.isOrgAdmin),
	};
}
