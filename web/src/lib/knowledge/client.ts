import { queryOptions } from "@tanstack/react-query";
import { apiPath, apiRequest } from "../api/client";

export type SourceStatus = "active" | "paused" | "removed";

/** One thing an organization asked its agents to index, such as a repository. */
export interface Source {
	connection_id?: string;
	created_at: string;
	created_by: string;
	id: string;
	interval_seconds: number;
	last_synced_at?: string;
	member_id?: string;
	provider: string;
	reason?: string;
	scope: "member" | "organization" | "team";
	status: SourceStatus;
	sync_at?: string;
	/** The kinds of document this source indexes; absent means the default kinds. */
	include_kinds?: string[];
	/** What to index at the provider, in the provider's own terms. */
	target: Record<string, string>;
	team_id?: string;
	updated_at: string;
}

export type RunStatus = "completed" | "failed" | "running";

export interface SyncRun {
	/** What the run judged not worth indexing: promotions, notifications, empty files. */
	documents_excluded?: number;
	documents_published: number;
	documents_removed: number;
	documents_seen: number;
	error?: string;
	finished_at?: string;
	id: string;
	kind: string;
	organization_id: string;
	retryable?: boolean;
	source_id: string;
	started_at: string;
	status: RunStatus;
}

function providersPath(): string {
	return "/v1/sources/providers";
}

function kindsPath(): string {
	return "/v1/sources/kinds";
}

function sourcesPath(organizationId: string): string {
	return apiPath`/v1/organizations/${organizationId}/sources`;
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

/** What this build of knowledge can extract from, by provider key. */
export async function listProviders(): Promise<string[]> {
	const { providers } = await apiRequest<{ providers: string[] }>(
		providersPath(),
	);
	return providers;
}

/** A kind of document a member can choose to index, as knowledge names it. */
export interface SourceKind {
	/** Whether a source indexes it unless its member chooses. */
	default: boolean;
	hint: string;
	label: string;
	name: string;
}

/** The kinds of document a member chooses among, in the order they are shown. */
export async function listKinds(): Promise<SourceKind[]> {
	const { kinds } = await apiRequest<{ kinds: SourceKind[] }>(kindsPath());
	return kinds;
}

/** What a source indexes: its member's choice, or else the default kinds. */
export function effectiveKinds(
	kinds: readonly SourceKind[],
	chosen: readonly string[] | undefined,
): string[] {
	if (chosen) {
		return [...chosen];
	}
	return kinds.filter((kind) => kind.default).map((kind) => kind.name);
}

/** How a source's choice of kinds reads in its row. */
export function kindsSummary(
	kinds: readonly SourceKind[],
	chosen: readonly string[] | undefined,
): string {
	if (!chosen) {
		return "Default kinds";
	}
	const defaults = new Set(effectiveKinds(kinds, undefined));
	const picked = new Set(chosen);
	const added = kinds.filter(
		(kind) => picked.has(kind.name) && !defaults.has(kind.name),
	);
	const dropped = kinds.filter(
		(kind) => !picked.has(kind.name) && defaults.has(kind.name),
	);
	if (added.length === 0 && dropped.length === 0) {
		return "Default kinds";
	}
	const parts = [
		...added.map((kind) => `+ ${kind.label}`),
		...dropped.map((kind) => `− ${kind.label}`),
	];
	return parts.join(", ");
}

/** Records what kinds of document a source indexes; null returns it to the default. */
export function setSourceKinds(
	organizationId: string,
	sourceId: string,
	include: readonly string[] | null,
): Promise<Source> {
	return apiRequest<Source>(
		`${sourcesPath(organizationId)}${apiPath`/${sourceId}`}/kinds`,
		{ body: { include_kinds: include }, method: "PUT" },
	);
}

export interface SourceFilter {
	connectionId?: string;
	provider?: string;
	scope?: "organization";
}

export async function listSources(
	organizationId: string,
	filter: SourceFilter = {},
): Promise<Source[]> {
	const { sources } = await apiRequest<{ sources: Source[] }>(
		withQuery(sourcesPath(organizationId), {
			connection_id: filter.connectionId,
			provider: filter.provider,
			scope: filter.scope,
		}),
	);
	return sources;
}

export interface RegisterSourceInput {
	connectionId: string;
	intervalSeconds: number;
	provider: string;
	target: Record<string, string>;
}

export function registerSource(
	organizationId: string,
	input: RegisterSourceInput,
): Promise<Source> {
	return apiRequest<Source>(sourcesPath(organizationId), {
		body: {
			connection_id: input.connectionId,
			interval_seconds: input.intervalSeconds,
			provider: input.provider,
			target: input.target,
		},
	});
}

export async function removeSource(
	organizationId: string,
	sourceId: string,
): Promise<void> {
	await apiRequest(`${sourcesPath(organizationId)}${apiPath`/${sourceId}`}`, {
		method: "DELETE",
	});
}

export async function listRuns(
	organizationId: string,
	sourceId: string,
	limit?: number,
): Promise<SyncRun[]> {
	const { runs } = await apiRequest<{ runs: SyncRun[] }>(
		withQuery(`${sourcesPath(organizationId)}${apiPath`/${sourceId}`}/runs`, {
			limit,
		}),
	);
	return runs;
}

/** The prefix of everything cached from knowledge about one organization. */
export function knowledgeQueryKey(organizationId: string) {
	return ["knowledge", organizationId] as const;
}

/**
 * The catalogue changes only when knowledge is deployed, so it is kept for
 * the session rather than refetched with every connections page.
 */
export function providersQueryOptions() {
	return queryOptions({
		queryFn: listProviders,
		queryKey: ["knowledge", "providers"] as const,
		staleTime: Number.POSITIVE_INFINITY,
	});
}

/** Like the providers, the kinds change only when knowledge is deployed. */
export function kindsQueryOptions() {
	return queryOptions({
		queryFn: listKinds,
		queryKey: ["knowledge", "kinds"] as const,
		staleTime: Number.POSITIVE_INFINITY,
	});
}

export function sourcesQueryOptions(
	organizationId: string,
	filter: SourceFilter = {},
) {
	return queryOptions({
		queryFn: () => listSources(organizationId, filter),
		queryKey: [
			...knowledgeQueryKey(organizationId),
			"sources",
			filter.connectionId ?? "",
			filter.provider ?? "",
			filter.scope ?? "mine",
		] as const,
	});
}

export function sourceRunsQueryOptions(
	organizationId: string,
	sourceId: string,
) {
	return queryOptions({
		queryFn: () => listRuns(organizationId, sourceId, 10),
		queryKey: [...knowledgeQueryKey(organizationId), "runs", sourceId] as const,
	});
}

/** The providers a connection can register a source under; `files` is not offered here. */
export const connectableSourceProviders = [
	"github",
	"notion",
	"slack",
	"googledrive",
	"gmail",
] as const;

export type SourceProvider = (typeof connectableSourceProviders)[number];

function isConnectableProvider(provider: string): provider is SourceProvider {
	return (connectableSourceProviders as readonly string[]).includes(provider);
}

/** Which of the connectable providers this deployment actually extracts from. */
export function availableSourceProviders(
	providers: readonly string[],
): SourceProvider[] {
	const known = new Set(providers);
	return connectableSourceProviders.filter((provider) => known.has(provider));
}

/** Whether `provider` both names a connection-based source and is mounted here. */
export function supportsSourceProvider(
	providers: readonly string[],
	provider: string,
): provider is SourceProvider {
	return isConnectableProvider(provider) && providers.includes(provider);
}

export interface SourceTargetField {
	help: string;
	label: string;
	placeholder: string;
	required: boolean;
}

const targetFields: Record<SourceProvider, SourceTargetField> = {
	github: {
		help: "Indexes its issues and pull requests, oldest updated first.",
		label: "Repository",
		placeholder: "owner/name",
		required: true,
	},
	notion: {
		help: "One database's rows, or every page this connection can see.",
		label: "Database ID",
		placeholder: "Leave blank to index every page this connection can see",
		required: false,
	},
	slack: {
		help: "One channel's message history. Use its ID, not its name.",
		label: "Channel ID",
		placeholder: "C0123456789",
		required: true,
	},
	googledrive: {
		help: "Docs, Sheets, Slides, PDFs and Office files: the whole Drive, or one folder and everything under it.",
		label: "Drive or folder",
		placeholder: "A folder's ID; leave blank for the whole Drive",
		required: false,
	},
	gmail: {
		help: "One label's mail, the Inbox unless you choose another. The first pass reads the last ninety days; attachments are left out.",
		label: "Label",
		placeholder: "INBOX, SENT, or a label's ID",
		required: false,
	},
};

export function sourceTargetField(provider: SourceProvider): SourceTargetField {
	return targetFields[provider];
}

/** The `target` knowledge expects for `provider`, from what a member typed. */
export function sourceTargetPayload(
	provider: SourceProvider,
	rawValue: string,
): Record<string, string> {
	const value = rawValue.trim();
	switch (provider) {
		case "github":
			return { repository: value };
		case "slack":
			return { channel: value };
		case "notion":
			return value === "" ? {} : { database: value };
		case "googledrive":
			return value === "" ? {} : { folder: value };
		case "gmail":
			return value === "" ? {} : { label: value };
		default:
			return {};
	}
}

/** Why knowledge would refuse this target, checked before registering it. */
export function sourceTargetProblem(
	provider: SourceProvider,
	rawValue: string,
): string | undefined {
	const value = rawValue.trim();
	if (provider === "github" && !/^[^\s/]+\/[^\s/]+$/.test(value)) {
		return "Enter a repository as owner/name.";
	}
	if (provider === "slack" && (value === "" || /\s/.test(value))) {
		return "Enter the channel's ID, such as C0123456789.";
	}
	if (provider === "googledrive" && /\s/.test(value)) {
		return "Enter the folder's ID from its link, or leave this blank for the whole Drive.";
	}
	if (provider === "gmail" && /\s/.test(value)) {
		return "Enter the label's ID, such as INBOX or SENT, not its name.";
	}
	return undefined;
}

/** How a source's `target` reads, for the sources list. */
export function sourceTargetLabel(
	provider: string,
	target: Record<string, string>,
): string {
	switch (provider) {
		case "github":
			return target.repository ?? "Repository";
		case "slack":
			return target.channel ? `#${target.channel}` : "Channel";
		case "notion":
			return target.database ? `Database ${target.database}` : "All pages";
		case "googledrive":
			return target.folder ? `Folder ${target.folder}` : "My Drive";
		case "gmail":
			return gmailLabelName(target.label ?? "INBOX");
		default: {
			const values = Object.values(target);
			return values.length > 0 ? values.join(", ") : "—";
		}
	}
}

/** Gmail's own names for the labels it is most often indexed by. */
function gmailLabelName(label: string): string {
	switch (label) {
		case "INBOX":
			return "Inbox";
		case "SENT":
			return "Sent";
		default:
			return `Label ${label}`;
	}
}

/** Knowledge takes 5 minutes to 7 days; a source left blank defaults to an hour. */
export const minSourceIntervalSeconds = 300;
export const maxSourceIntervalSeconds = 604_800;
export const defaultSourceIntervalSeconds = 3600;

export const sourceIntervalChoices: readonly {
	label: string;
	seconds: number;
}[] = [
	{ label: "Every 5 minutes", seconds: 300 },
	{ label: "Every 15 minutes", seconds: 900 },
	{ label: "Every hour", seconds: 3600 },
	{ label: "Every 6 hours", seconds: 21_600 },
	{ label: "Every day", seconds: 86_400 },
	{ label: "Every week", seconds: 604_800 },
];

export function intervalLabel(seconds: number): string {
	const preset = sourceIntervalChoices.find(
		(choice) => choice.seconds === seconds,
	);
	if (preset) {
		return preset.label;
	}
	if (seconds % 86_400 === 0) {
		return `Every ${seconds / 86_400} days`;
	}
	if (seconds % 3600 === 0) {
		return `Every ${seconds / 3600} hours`;
	}
	if (seconds % 60 === 0) {
		return `Every ${seconds / 60} minutes`;
	}
	return `Every ${seconds} seconds`;
}

export function sourceStatusLabel(status: SourceStatus): string {
	switch (status) {
		case "active":
			return "Active";
		case "paused":
			return "Needs reconnecting";
		case "removed":
			return "Removed";
		default:
			return status;
	}
}

export function sourceStatusTone(
	status: SourceStatus,
): "active" | "danger" | "neutral" | "warning" {
	switch (status) {
		case "active":
			return "active";
		case "paused":
			return "warning";
		default:
			return "neutral";
	}
}

/** What one pass did, as the runs list reads it. */
export function runSummary(
	run: Pick<
		SyncRun,
		| "documents_excluded"
		| "documents_published"
		| "documents_removed"
		| "documents_seen"
	>,
): string {
	const parts = [`${run.documents_published} published`];
	if (run.documents_excluded) {
		parts.push(`${run.documents_excluded} skipped as noise`);
	}
	parts.push(`${run.documents_removed} removed of ${run.documents_seen} seen`);
	return parts.join(" · ");
}

export function runStatusLabel(status: RunStatus): string {
	switch (status) {
		case "running":
			return "Running";
		case "completed":
			return "Completed";
		case "failed":
			return "Failed";
		default:
			return status;
	}
}

export function runStatusTone(
	status: RunStatus,
): "active" | "danger" | "neutral" | "warning" {
	switch (status) {
		case "completed":
			return "active";
		case "failed":
			return "danger";
		default:
			return "neutral";
	}
}
