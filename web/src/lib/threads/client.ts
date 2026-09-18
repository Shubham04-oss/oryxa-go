import { queryOptions } from "@tanstack/react-query";
import { apiFetch, apiPath, apiRequest, requestError } from "../api/client";
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
	at?: string;
	author?: string;
	/** tool_call/tool_result pairing; on a confirmation, the id an answer must name. */
	call_id?: string;
	/** On a message: why the model stopped without answering. On tool_result: the tool's error. On run_failed: what to tell the member. */
	error?: string;
	failed?: boolean;
	hint?: string;
	/** Stored event id (history and streamed finished items); deltas and run_* items have none. */
	id?: string;
	run_id?: string;
	status?: "pending" | "approved" | "declined";
	text?: string;
	tool?: string;
	type: ItemType;
	usage?: ItemUsage;
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
	await apiRequest(threadPath(organizationId, threadId), { method: "DELETE" });
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
