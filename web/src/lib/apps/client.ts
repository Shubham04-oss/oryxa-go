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

/** One thing inside a connection that can be indexed, as the app itself lists it. */
export interface ConnectionTarget {
	description?: string;
	id: string;
	/** "repository", "channel" or "database" — what the app calls it. */
	kind: string;
	/** What a member reads: "#engineering" rather than C0123456789. */
	name: string;
	/** The `target` knowledge registers a source with, ready to pass on. */
	target: Record<string, string>;
}

async function listConnectionTargets(
	organizationId: string,
	connectionId: string,
): Promise<ConnectionTarget[]> {
	const answer = await apiRequest<{ targets: ConnectionTarget[] }>(
		apiPath`${connectionsPath(organizationId)}/${connectionId}/targets`,
	);
	return answer.targets ?? [];
}

/**
 * What a connection holds, read from the app itself, so a member picks a
 * repository or a channel instead of knowing its id. An app nothing can be
 * indexed for answers an empty list, which is a fact rather than a failure.
 */
export function connectionTargetsQueryOptions(
	organizationId: string,
	connectionId: string,
) {
	return queryOptions({
		queryFn: () => listConnectionTargets(organizationId, connectionId),
		queryKey: [
			...appsQueryKey(organizationId),
			"connections",
			connectionId,
			"targets",
		] as const,
		// The app is asked once per visit: repositories and channels do not
		// come and go while a member is choosing one.
		staleTime: 60_000,
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
	/** The values a "select" field takes, in the order the catalog listed them. */
	options?: string[];
	required: boolean;
	title: string;
	/** "select" is an enum; "strings" is a list the member types one per line. */
	type: "boolean" | "number" | "select" | "string" | "strings";
}

export type TriggerConfigCapability =
	| { fields: TriggerConfigField[]; kind: "form" }
	| { kind: "none" }
	| { kind: "unsupported" };

interface JsonSchemaProperty {
	allOf?: JsonSchemaProperty[];
	anyOf?: JsonSchemaProperty[];
	default?: boolean | number | string;
	description?: string;
	enum?: unknown[];
	items?: JsonSchemaProperty;
	oneOf?: JsonSchemaProperty[];
	title?: string;
	/** JSON Schema writes one name, or a list of them for a nullable field. */
	type?: string | string[];
}

interface JsonSchemaObject {
	properties?: Record<string, JsonSchemaProperty>;
	required?: string[];
}

/**
 * The type names a property declares, with "null" dropped: a nullable string
 * is still a string to type into.
 */
function declaredTypes(type: string | string[] | undefined): string[] {
	if (typeof type === "string") {
		return [type];
	}
	if (Array.isArray(type)) {
		return type.filter((name) => name !== "null");
	}
	return [];
}

/**
 * How a property is collected, or undefined when it honestly cannot be.
 *
 * Composio's catalog is written by as many teams as it has apps, so a
 * property arrives as a plain type, an enum, a list, a nullable pair, or one
 * of several shapes. Everything that can be turned into a field a member
 * fills in is; what is left — an object, a list of objects, a reference this
 * does not follow — is what "unsupported" honestly means.
 */
function fieldShape(
	property: JsonSchemaProperty | undefined,
): Pick<TriggerConfigField, "options" | "type"> | undefined {
	if (property === undefined) {
		return undefined;
	}
	// An enum is a choice, whatever its declared type says.
	const options = enumOptions(property.enum);
	if (options !== undefined) {
		return { options, type: "select" };
	}
	const types = declaredTypes(property.type);
	if (types.includes("array")) {
		// A list of scalars is typed one per line; a list of objects is not
		// something a form can collect.
		const itemTypes = declaredTypes(property.items?.type);
		const itemOptions = enumOptions(property.items?.enum);
		if (itemOptions !== undefined) {
			return { options: itemOptions, type: "strings" };
		}
		if (itemTypes.length === 0 || itemTypes.some(isScalarType)) {
			return { type: "strings" };
		}
		return undefined;
	}
	for (const name of types) {
		const scalar = scalarField(name);
		if (scalar !== undefined) {
			return { type: scalar };
		}
	}
	// A property written as one of several shapes is collected as the first
	// of them anything can be typed into.
	for (const branch of [
		...(property.anyOf ?? []),
		...(property.oneOf ?? []),
		...(property.allOf ?? []),
	]) {
		const shape = fieldShape(branch);
		if (shape !== undefined) {
			return shape;
		}
	}
	return undefined;
}

function isScalarType(name: string): boolean {
	return scalarField(name) !== undefined;
}

function scalarField(
	name: string,
): "boolean" | "number" | "string" | undefined {
	if (name === "integer") {
		return "number";
	}
	return name === "boolean" || name === "number" || name === "string"
		? name
		: undefined;
}

/** An enum's values as strings, when every one of them is a scalar. */
function enumOptions(values: unknown[] | undefined): string[] | undefined {
	if (!Array.isArray(values) || values.length === 0) {
		return undefined;
	}
	const options: string[] = [];
	for (const value of values) {
		if (
			typeof value !== "string" &&
			typeof value !== "number" &&
			typeof value !== "boolean"
		) {
			return undefined;
		}
		options.push(String(value));
	}
	return options;
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
	// A required field nothing can be typed into is the whole trigger: there
	// is no subscribing to it without one.
	for (const name of required) {
		if (fieldShape(properties[name]) === undefined) {
			return { kind: "unsupported" };
		}
	}
	const fields: TriggerConfigField[] = [];
	for (const name of names) {
		const property = properties[name];
		const shape = fieldShape(property);
		if (shape === undefined) {
			// Optional and uncollectable: left out rather than refusing the
			// trigger, since Composio takes the subscription without it.
			continue;
		}
		fields.push({
			default: property?.default,
			description: property?.description,
			name,
			options: shape.options,
			required: required.has(name),
			title: property?.title ?? titleCase(name),
			type: shape.type,
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
