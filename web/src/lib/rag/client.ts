import { infiniteQueryOptions, queryOptions } from "@tanstack/react-query";
import { apiPath, apiRequest } from "../api/client";

/** Mirrors files: where a collection hangs decides who may search or change it. */
export type CorpusScope = "member" | "team" | "organization";

export interface Corpus {
	chunk_count: number;
	created_at: string;
	document_count: number;
	id: string;
	member_id?: string;
	/** `metadata.origin === "knowledge"` marks a collection built from a member's connected apps. */
	metadata?: Record<string, string>;
	name: string;
	organization_id: string;
	scope: CorpusScope;
	slug: string;
	team_id?: string;
	updated_at: string;
}

export type DocumentStatus = "pending" | "ingesting" | "ready" | "failed";
export type DocumentSourceKind = "inline" | "file" | "external";

export interface DocumentSource {
	kind: DocumentSourceKind;
	/** The files object id for `file`, the URI for `external`; absent for `inline`. */
	ref?: string;
}

export interface CorpusDocument {
	byte_size: number;
	chunk_count: number;
	content_type: string;
	created_at: string;
	failure_reason?: string;
	id: string;
	ingested_at?: string;
	source: DocumentSource;
	status: DocumentStatus;
	title?: string;
	updated_at: string;
}

export interface DocumentPage {
	documents: CorpusDocument[];
	/** Absent on the last page; pass back as the next request's cursor to continue. */
	next_cursor?: string;
}

function corporaPath(organizationId: string): string {
	return apiPath`/v1/organizations/${organizationId}/corpora`;
}

function corpusPath(organizationId: string, corpusId: string): string {
	return `${corporaPath(organizationId)}${apiPath`/${corpusId}`}`;
}

/** The collections the member may search by default: their own and the organization's. */
export async function listCorpora(organizationId: string): Promise<Corpus[]> {
	const { corpora } = await apiRequest<{ corpora: Corpus[] }>(
		corporaPath(organizationId),
	);
	return corpora;
}

export function getCorpus(
	organizationId: string,
	corpusId: string,
): Promise<Corpus> {
	return apiRequest<Corpus>(corpusPath(organizationId, corpusId));
}

export interface DocumentsFilter {
	cursor?: string;
	limit?: number;
}

export function listDocuments(
	organizationId: string,
	corpusId: string,
	filter: DocumentsFilter = {},
): Promise<DocumentPage> {
	const search = new URLSearchParams();
	if (filter.cursor) {
		search.set("cursor", filter.cursor);
	}
	if (filter.limit) {
		search.set("limit", String(filter.limit));
	}
	const query = search.toString();
	const base = `${corpusPath(organizationId, corpusId)}/documents`;
	return apiRequest<DocumentPage>(query ? `${base}?${query}` : base);
}

export function corporaQueryKey(organizationId: string) {
	return ["rag", "corpora", organizationId] as const;
}

export function corporaQueryOptions(organizationId: string) {
	return queryOptions({
		queryFn: () => listCorpora(organizationId),
		queryKey: corporaQueryKey(organizationId),
	});
}

export function corpusQueryOptions(organizationId: string, corpusId: string) {
	return queryOptions({
		queryFn: () => getCorpus(organizationId, corpusId),
		queryKey: [...corporaQueryKey(organizationId), corpusId],
	});
}

/** How many documents a page of the infinite list asks for. */
export const documentsPageSize = 50;

export function documentsQueryKey(organizationId: string, corpusId: string) {
	return ["rag", "documents", organizationId, corpusId] as const;
}

export function documentsInfiniteQueryOptions(
	organizationId: string,
	corpusId: string,
) {
	return infiniteQueryOptions({
		getNextPageParam: (lastPage: DocumentPage) => lastPage.next_cursor,
		initialPageParam: undefined as string | undefined,
		queryFn: ({ pageParam }) =>
			listDocuments(organizationId, corpusId, {
				cursor: pageParam,
				limit: documentsPageSize,
			}),
		queryKey: documentsQueryKey(organizationId, corpusId),
	});
}

/** A human label for where a collection sits, mirroring files' own scopes. */
export function scopeLabel(scope: CorpusScope): string {
	switch (scope) {
		case "member":
			return "Just you";
		case "team":
			return "Team";
		default:
			return "Organization";
	}
}

export function documentCountLabel(count: number): string {
	return `${count} document${count === 1 ? "" : "s"}`;
}

export function chunkCountLabel(count: number): string {
	return `${count} chunk${count === 1 ? "" : "s"}`;
}

/** Whether a collection was built from the member's connected apps, not added by hand. */
export function isFromConnectedApps(corpus: Pick<Corpus, "metadata">): boolean {
	return corpus.metadata?.origin === "knowledge";
}

/** A document's source link, when it is safe to show as one: external and http(s). */
export function externalSourceLink(source: DocumentSource): string | undefined {
	if (source.kind !== "external" || !source.ref) {
		return undefined;
	}
	try {
		const url = new URL(source.ref);
		return url.protocol === "http:" || url.protocol === "https:"
			? source.ref
			: undefined;
	} catch {
		return undefined;
	}
}
