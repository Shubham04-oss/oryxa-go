import { queryOptions } from "@tanstack/react-query";
import {
	apiFetch,
	apiPath,
	apiRequest,
	apiUrl,
	requestError,
} from "../api/client";
import { createEventStreamReader } from "./event-stream";

export interface Thread {
	created_at?: string;
	id: string;
	title: string;
	updated_at: string;
}

export interface ThreadDetail extends Thread {
	items: Item[];
	pending_confirmations: string[];
}

export type ItemType =
	| "message"
	| "delta"
	| "tool_call"
	| "tool_result"
	| "confirmation"
	| "artifact"
	| "run_started"
	| "run_completed"
	| "run_failed";

export interface ItemUsage {
	cached_tokens?: number;
	input_tokens: number;
	output_tokens: number;
	total_tokens: number;
}

export interface Item {
	/** tool_call (omitted when very large) and confirmation (always present). */
	arguments?: Record<string, unknown>;
	/** artifact: the file's name; encode each "/"-separated segment to fetch it. */
	artifact?: string;
	at?: string;
	/** "member", the agent's name, or "apps" for a batched wake-up from the member's connected apps. */
	author?: string;
	/** tool_call/tool_result pairing; on a confirmation, the id an answer must name. */
	call_id?: string;
	/** run_failed from the chat Durable Object: what the page should do about it ("session_expired"). Go never sets it. */
	code?: string;
	/** On a message: why the model stopped without answering. On tool_result: the tool's error. On run_failed: what to tell the member. */
	error?: string;
	failed?: boolean;
	/** confirmation: words for the member; empty when ADK left only its developer-facing default. */
	hint?: string;
	/** Stored event id (history and streamed finished items); deltas and run_* items have none. */
	id?: string;
	run_id?: string;
	status?: "pending" | "approved" | "declined";
	text?: string;
	tool?: string;
	/** confirmation: the tool_call item it holds back; call_id above is still what the member's answer names. */
	tool_call_id?: string;
	type: ItemType;
	usage?: ItemUsage;
	/** artifact: which version this is. */
	version?: number;
}

export interface ConfirmationAnswer {
	confirmed: boolean;
	id: string;
}

export type RunInput =
	| { message: string }
	| { confirmations: ConfirmationAnswer[] };

function threadsPath(organizationId: string): string {
	return apiPath`/v1/organizations/${organizationId}/threads`;
}

function threadPath(organizationId: string, threadId: string): string {
	return `${threadsPath(organizationId)}${apiPath`/${threadId}`}`;
}

/**
 * Where the browser fetches an artifact's bytes, through the session cookie.
 * A name can contain "/" (kept as path structure) and a "user:" prefix, so
 * each segment is encoded on its own rather than encoding the whole name.
 */
export function artifactContentUrl(
	organizationId: string,
	threadId: string,
	name: string,
	version: number,
): string {
	const encodedName = name
		.split("/")
		.map((segment) => encodeURIComponent(segment))
		.join("/");
	return apiUrl(
		`${threadPath(organizationId, threadId)}/artifacts/${encodedName}?version=${version}`,
	);
}

/** Every thread in the organization, most recently active first. */
export async function listThreads(organizationId: string): Promise<Thread[]> {
	const { threads } = await apiRequest<{ threads: Thread[] }>(
		threadsPath(organizationId),
	);
	return threads;
}

export function createThread(organizationId: string): Promise<Thread> {
	return apiRequest<Thread>(threadsPath(organizationId), { method: "POST" });
}

export function getThread(
	organizationId: string,
	threadId: string,
): Promise<ThreadDetail> {
	return apiRequest<ThreadDetail>(threadPath(organizationId, threadId));
}

export async function deleteThread(
	organizationId: string,
	threadId: string,
): Promise<void> {
	await apiRequest(
		`/agents/thread-chat-agent/${encodeURIComponent(`${organizationId}~${threadId}`)}`,
		{ method: "DELETE" },
	);
}

/** How many conversations the sidebar shows before it defers to search. */
export const SIDEBAR_THREAD_LIMIT = 3;

/**
 * The most recently touched conversations, newest first, capped at `limit`.
 *
 * `keepId` is the conversation being read: an older one you opened from
 * search would otherwise fall outside the cap, and a sidebar that cannot show
 * you where you are is worse than one showing a conversation less. It takes
 * the last slot rather than the first, so the recency order above it holds
 * still while you read.
 */
export function recentThreads(
	threads: readonly Thread[] | undefined,
	limit = SIDEBAR_THREAD_LIMIT,
	keepId?: string,
): Thread[] {
	const room = Math.max(0, limit);
	const newest = [...(threads ?? [])].sort((a, b) =>
		b.updated_at.localeCompare(a.updated_at),
	);
	const shown = newest.slice(0, room);
	if (!keepId || room === 0 || shown.some((item) => item.id === keepId)) {
		return shown;
	}
	const reading = newest.find((item) => item.id === keepId);
	return reading ? [...shown.slice(0, room - 1), reading] : shown;
}

/** What a conversation is called when it has not earned a title yet. */
export function threadTitle(thread: Pick<Thread, "title">): string {
	return thread.title.trim() || "New conversation";
}

export function threadsQueryKey(organizationId: string) {
	return ["threads", organizationId] as const;
}

export function threadsQueryOptions(organizationId: string) {
	return queryOptions({
		queryFn: () => listThreads(organizationId),
		queryKey: threadsQueryKey(organizationId),
	});
}

export function threadQueryKey(organizationId: string, threadId: string) {
	return [...threadsQueryKey(organizationId), threadId] as const;
}

export function threadQueryOptions(organizationId: string, threadId: string) {
	return queryOptions({
		queryFn: () => getThread(organizationId, threadId),
		queryKey: threadQueryKey(organizationId, threadId),
	});
}

/** What a member chose about Krude, in one organization. */
export interface KrudePreferences {
	/** Whether Krude wakes up to tell the member what their connected apps report. On until they turn it off. */
	app_updates: boolean;
}

function preferencesPath(organizationId: string): string {
	return apiPath`/v1/organizations/${organizationId}/preferences`;
}

export function getKrudePreferences(
	organizationId: string,
): Promise<KrudePreferences> {
	return apiRequest<KrudePreferences>(preferencesPath(organizationId));
}

/** Turning app updates off also drops what was waiting to be told. */
export function setAppUpdates(
	organizationId: string,
	appUpdates: boolean,
): Promise<KrudePreferences> {
	return apiRequest<KrudePreferences>(preferencesPath(organizationId), {
		body: { app_updates: appUpdates },
		method: "PUT",
	});
}

export function krudePreferencesQueryOptions(organizationId: string) {
	return queryOptions({
		queryFn: () => getKrudePreferences(organizationId),
		queryKey: ["krude-preferences", organizationId] as const,
	});
}

export interface StartRunOptions {
	/** Called for every item the stream carries, in order, until it ends. */
	onItem: (item: Item) => void;
	signal?: AbortSignal;
}

/**
 * Starts a run and reads its answer as it streams back. Throws `ApiError`
 * when the service refuses the run outright (e.g. `run_in_progress`,
 * `confirmation_pending`); a run that starts but later fails instead reports
 * itself through a `run_failed` item, same as any other item.
 */
export async function startRun(
	organizationId: string,
	threadId: string,
	input: RunInput,
	{ onItem, signal }: StartRunOptions,
): Promise<void> {
	const response = await apiFetch(
		`${threadPath(organizationId, threadId)}/runs`,
		{
			body: input,
			method: "POST",
			signal,
		},
	);
	if (!response.ok) {
		throw await requestError(response);
	}
	const body = response.body;
	if (!body) {
		return;
	}

	const reader = body.getReader();
	const parser = createEventStreamReader();
	try {
		for (;;) {
			const { done, value } = await reader.read();
			if (done) {
				for (const event of parser.end()) {
					deliver(event, onItem);
				}
				return;
			}
			for (const event of parser.push(value)) {
				deliver(event, onItem);
			}
		}
	} finally {
		reader.releaseLock();
	}
}

function deliver(event: { data: string }, onItem: (item: Item) => void): void {
	if (event.data === "") {
		return;
	}
	onItem(JSON.parse(event.data) as Item);
}
