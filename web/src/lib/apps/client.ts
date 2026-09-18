import { queryOptions } from "@tanstack/react-query";
import { ApiError, apiErrorMessage, apiPath, apiRequest } from "../api/client";

export interface CatalogCategory {
	id: string;
	name: string;
}

/** An app in Composio's catalog, as a member browses it. */
export interface Toolkit {
	auth_schemes: string[];
	categories: CatalogCategory[];
	/** A member can connect it with what this project holds. */
	connectable: boolean;
	description: string;
	logo?: string;
	name: string;
	no_auth?: boolean;
	toolkit: string;
	tools_count: number;
	triggers_count: number;
}

/** The event a subscription watches for, as its own kind of catalog entry. */
export interface TriggerType {
	/** A JSON Schema of what a subscription takes, when it takes anything. */
	config_schema?: unknown;
	description: string;
	instructions?: string;
	/** "webhook" when the app pushes the event, "poll" when Composio polls. */
	kind: string;
	/** A webhook URL has to be registered with the app by hand first. */
	manual_setup?: boolean;
	name: string;
	toolkit: string;
	trigger: string;
}

export type ConnectionStatus =
	| "active"
	| "expired"
	| "failed"
	| "pending"
	| "removed";

export interface Connection {
	composio_account_id?: string;
	connected_at?: string;
	created_at: string;
	id: string;
	label?: string;
	member_id: string;
	removed_at?: string;
	status: ConnectionStatus;
	status_reason?: string;
	toolkit: string;
	updated_at: string;
}

export type TriggerStatus = "active" | "disabled" | "removed";

export interface Trigger {
	config: Record<string, unknown>;
	connection_id: string;
	created_at: string;
	id: string;
	member_id: string;
	status: TriggerStatus;
	status_reason?: string;
	toolkit: string;
	trigger: string;
	updated_at: string;
}

export interface CatalogQuery {
	category?: string;
	cursor?: string;
	limit?: number;
	search?: string;
}

export interface CatalogPage {
	nextCursor?: string;
	toolkits: Toolkit[];
}

export interface TriggerTypePage {
	nextCursor?: string;
	triggers: TriggerType[];
}

function catalogPath(): string {
	return "/v1/apps/catalog";
}

function connectionsPath(organizationId: string): string {
	return apiPath`/v1/organizations/${organizationId}/apps/connections`;
}

function triggersPath(organizationId: string): string {
	return apiPath`/v1/organizations/${organizationId}/apps/triggers`;
}

function withQuery(
	path: string,
	params: Record<string, boolean | number | string | undefined>,
): string {
	const search = new URLSearchParams();
	for (const [key, value] of Object.entries(params)) {
		if (value !== undefined && value !== "") {
			search.set(key, String(value));
		}
	}
	const query = search.toString();
	return query ? `${path}?${query}` : path;
}

export async function listCatalog(
	query: CatalogQuery = {},
): Promise<CatalogPage> {
	const { next_cursor, toolkits } = await apiRequest<{
		next_cursor?: string;
		toolkits: Toolkit[];
	}>(
		withQuery(catalogPath(), {
			category: query.category,
			cursor: query.cursor,
			limit: query.limit,
			search: query.search,
		}),
	);
	return { nextCursor: next_cursor, toolkits };
}

export function getCatalogToolkit(toolkit: string): Promise<Toolkit> {
	return apiRequest<Toolkit>(`${catalogPath()}${apiPath`/${toolkit}`}`);
}

export async function listCatalogTriggerTypes(
	toolkit: string,
	query: { cursor?: string; limit?: number } = {},
): Promise<TriggerTypePage> {
	const { next_cursor, triggers } = await apiRequest<{
		next_cursor?: string;
		triggers: TriggerType[];
	}>(
		withQuery(`${catalogPath()}${apiPath`/${toolkit}`}/triggers`, {
			cursor: query.cursor,
			limit: query.limit,
		}),
	);
	return { nextCursor: next_cursor, triggers };
}

export interface ConnectInput {
	alias?: string;
	returnTo: string;
	toolkit: string;
}

export interface ConnectResult {
	connection: Connection;
	redirect_url: string;
}

export function connect(
	organizationId: string,
	input: ConnectInput,
): Promise<ConnectResult> {
	return apiRequest<ConnectResult>(connectionsPath(organizationId), {
		body: {
			alias: input.alias,
			return_to: input.returnTo,
			toolkit: input.toolkit,
		},
	});
}

export interface ConnectionFilter {
	scope?: "organization";
	toolkit?: string;
}

export async function listConnections(
	organizationId: string,
	filter: ConnectionFilter = {},
): Promise<Connection[]> {
	const { connections } = await apiRequest<{ connections: Connection[] }>(
		withQuery(connectionsPath(organizationId), {
			scope: filter.scope,
			toolkit: filter.toolkit,
		}),
	);
	return connections;
}

export function getConnection(
	organizationId: string,
	connectionId: string,
): Promise<Connection> {
	return apiRequest<Connection>(
		`${connectionsPath(organizationId)}${apiPath`/${connectionId}`}`,
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

export async function listTriggers(
	organizationId: string,
	connectionId?: string,
): Promise<Trigger[]> {
	const { triggers } = await apiRequest<{ triggers: Trigger[] }>(
		withQuery(triggersPath(organizationId), { connection_id: connectionId }),
	);
	return triggers;
}

export interface CreateTriggerInput {
	config?: Record<string, unknown>;
	connectionId: string;
	trigger: string;
}

export function createTrigger(
	organizationId: string,
	input: CreateTriggerInput,
): Promise<Trigger> {
	return apiRequest<Trigger>(triggersPath(organizationId), {
		body: {
			config: input.config ?? {},
			connection_id: input.connectionId,
			trigger: input.trigger,
		},
	});
}

export async function removeTrigger(
	organizationId: string,
	triggerId: string,
): Promise<void> {
	await apiRequest(`${triggersPath(organizationId)}${apiPath`/${triggerId}`}`, {
		method: "DELETE",
	});
}

/** The prefix of everything cached from apps about one organization. */
export function appsQueryKey(organizationId: string) {
	return ["apps", organizationId] as const;
}

export function catalogQueryOptions(query: CatalogQuery = {}) {
	return queryOptions({
		queryFn: () => listCatalog(query),
		queryKey: [
			"apps",
			"catalog",
			query.search ?? "",
			query.category ?? "",
			query.cursor ?? "",
			query.limit ?? 0,
		] as const,
	});
}

/** A single app's catalog entry, cached: the catalog changes rarely. */
export function catalogToolkitQueryOptions(toolkit: string) {
	return queryOptions({
		queryFn: () => getCatalogToolkit(toolkit),
		queryKey: ["apps", "catalog", "toolkit", toolkit] as const,
		staleTime: 5 * 60_000,
	});
}

export function catalogTriggerTypesQueryOptions(toolkit: string) {
	return queryOptions({
		queryFn: () => listCatalogTriggerTypes(toolkit, { limit: 50 }),
		queryKey: ["apps", "catalog", "triggers", toolkit] as const,
		staleTime: 5 * 60_000,
	});
}

export function connectionsQueryOptions(
	organizationId: string,
	filter: ConnectionFilter = {},
) {
	return queryOptions({
		queryFn: () => listConnections(organizationId, filter),
		queryKey: [
			...appsQueryKey(organizationId),
			"connections",
			filter.scope ?? "mine",
			filter.toolkit ?? "",
		] as const,
	});
}

export function triggersQueryOptions(
	organizationId: string,
	connectionId: string,
) {
	return queryOptions({
		queryFn: () => listTriggers(organizationId, connectionId),
		queryKey: [
			...appsQueryKey(organizationId),
			"triggers",
			connectionId,
		] as const,
	});
}

export interface ConnectReturn {
	connectionId: string;
	status: string;
}

/**
 * The connection Composio's callback sent the member's browser back with, if
 * this load is that return trip. `search` is the route's own search params,
 * carrying `app_connection` and `status` exactly as the callback set them.
 */
export function parseConnectReturn(search: {
	app_connection?: string;
	status?: string;
}): ConnectReturn | undefined {
	if (!search.app_connection || !search.status) {
		return undefined;
	}
	return { connectionId: search.app_connection, status: search.status };
}

/** Where Composio's hosted flow sends the member's browser back to. */
export function connectReturnTo(origin: string): string {
	return `${origin}/app/connections`;
}

export interface ConnectNotice {
	text: string;
	tone: "danger" | "info" | "success";
}

/** What to tell the member once their browser is back from connecting. */
export function connectReturnNotice(value: ConnectReturn): ConnectNotice {
	switch (value.status) {
		case "active":
			return { text: "Connected. It's ready to use.", tone: "success" };
		case "pending":
			return {
				text: "Still finishing up — check back in a moment.",
				tone: "info",
			};
		default:
			return {
				text: "The connection did not finish. Try again.",
				tone: "danger",
			};
	}
}

export function connectionStatusLabel(status: ConnectionStatus): string {
	switch (status) {
		case "active":
			return "Active";
		case "pending":
			return "Connecting…";
		case "expired":
			return "Expired";
		case "failed":
			return "Failed";
		case "removed":
			return "Removed";
		default:
			return status;
	}
}

export function connectionStatusTone(
	status: ConnectionStatus,
): "active" | "danger" | "neutral" | "warning" {
	switch (status) {
		case "active":
			return "active";
		case "expired":
			return "warning";
		case "failed":
			return "danger";
		default:
			return "neutral";
	}
}

/** Only an expired connection is offered a plain reconnect: the others need no action, or none that starting over fixes. */
export function canReconnect(status: ConnectionStatus): boolean {
	return status === "expired";
}

/** Why `toolkit` cannot be connected here, for a disabled catalog row. */
export function nonConnectableReason(toolkit: Toolkit): string | undefined {
	if (toolkit.connectable) {
		return undefined;
	}
	return toolkit.no_auth
		? "This app needs no connection."
		: "Connecting this app isn't set up yet.";
}

export interface ConnectionGroup {
	activeCount: number;
	attentionCount: number;
	connections: Connection[];
	toolkit: string;
}

/**
 * The organization's connections to each app, for an admin's summary.
 * Removed connections are dropped: the view is who is connected now, not
 * everyone who ever was.
 */
export function groupConnectionsByToolkit(
	connections: Connection[],
): ConnectionGroup[] {
	const groups = new Map<string, Connection[]>();
	for (const connection of connections) {
		if (connection.status === "removed") {
			continue;
		}
		const list = groups.get(connection.toolkit) ?? [];
		list.push(connection);
		groups.set(connection.toolkit, list);
	}
	return [...groups.entries()]
		.map(([toolkit, list]) => ({
			activeCount: list.filter((connection) => connection.status === "active")
				.length,
			attentionCount: list.filter(
				(connection) =>
					connection.status === "expired" || connection.status === "failed",
			).length,
			connections: list,
			toolkit,
		}))
		.sort((a, b) => a.toolkit.localeCompare(b.toolkit));
}

export interface TriggerConfigField {
	default?: boolean | number | string;
	description?: string;
	name: string;
	required: boolean;
	title: string;
	type: "boolean" | "number" | "string";
}

export type TriggerConfigCapability =
	| { fields: TriggerConfigField[]; kind: "form" }
	| { kind: "none" }
	| { kind: "unsupported" };

interface JsonSchemaProperty {
	default?: boolean | number | string;
	description?: string;
	title?: string;
	type?: string;
}

interface JsonSchemaObject {
	properties?: Record<string, JsonSchemaProperty>;
	required?: string[];
}

function normalizedFieldType(
	type: string | undefined,
): "boolean" | "number" | "string" | undefined {
	if (type === "integer") {
		return "number";
	}
	return type === "boolean" || type === "number" || type === "string"
		? type
		: undefined;
}

function titleCase(name: string): string {
	const spaced = name.replace(/[_-]+/g, " ").trim();
	if (spaced === "") {
		return name;
	}
	return spaced
		.split(" ")
		.map((word) => word.charAt(0).toUpperCase() + word.slice(1))
		.join(" ");
}

/**
 * What a trigger's `config_schema` asks a member for: no config to collect,
 * a flat form of string/number/boolean fields, or a shape too rich to render
 * here — in which case the caller shows it disabled, "needs setup".
 */
export function triggerConfigCapability(
	configSchema: unknown,
): TriggerConfigCapability {
	if (
		configSchema === undefined ||
		configSchema === null ||
		typeof configSchema !== "object"
	) {
		return { kind: "none" };
	}
	const schema = configSchema as JsonSchemaObject;
	const properties = schema.properties ?? {};
	const names = Object.keys(properties);
	if (names.length === 0) {
		return { kind: "none" };
	}
	const required = new Set(schema.required ?? []);
	for (const name of required) {
		if (normalizedFieldType(properties[name]?.type) === undefined) {
			return { kind: "unsupported" };
		}
	}
	const fields: TriggerConfigField[] = [];
	for (const name of names) {
		const property = properties[name];
		const type = normalizedFieldType(property?.type);
		if (type === undefined) {
			continue;
		}
		fields.push({
			default: property?.default,
			description: property?.description,
			name,
			required: required.has(name),
			title: property?.title ?? titleCase(name),
			type,
		});
	}
	if (fields.length === 0) {
		return required.size > 0 ? { kind: "unsupported" } : { kind: "none" };
	}
	return { fields, kind: "form" };
}

const appsErrorCopy: Partial<Record<string, string>> = {
	choose_connection: "More than one connection could answer this. Choose one.",
	conflict: "That didn't go through because something changed. Try again.",
	needs_reconnect: "Reconnect this app to keep using it.",
	not_connectable: "This app can't be connected here yet.",
	not_connected: "Connect this app first.",
	rate_limited: "Too many requests. Try again in a minute.",
	unavailable: "The app catalog or Composio did not answer. Try again shortly.",
};

/** Apps' own error codes in words a member can act on; the generic wording otherwise. */
export function appsErrorMessage(error: unknown, fallback: string): string {
	if (error instanceof ApiError && error.code) {
		const copy = appsErrorCopy[error.code];
		if (copy) {
			return copy;
		}
	}
	return apiErrorMessage(error, fallback);
}
