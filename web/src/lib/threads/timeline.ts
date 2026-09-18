import type { Item } from "./client";

export interface MessageEntry {
	author: string;
	error?: string;
	id: string;
	kind: "message";
	/** True for the draft bubble built live from `delta` items, before its finished `message` arrives. */
	pending: boolean;
	text: string;
}

export interface ToolEntry {
	arguments?: Record<string, unknown>;
	callId: string;
	errorText?: string;
	kind: "tool";
	resultText?: string;
	state: "running" | "done" | "failed";
	tool: string;
}

export interface ConfirmationEntry {
	arguments?: Record<string, unknown>;
	callId: string;
	hint?: string;
	kind: "confirmation";
	status: "pending" | "approved" | "declined";
	tool?: string;
}

export type TimelineEntry = MessageEntry | ToolEntry | ConfirmationEntry;

/**
 * Groups a run's raw items — stored history, a live run's streamed items, or
 * both concatenated — into the blocks the conversation renders: messages
 * (with in-progress deltas folded into a growing draft bubble), tool calls
 * paired with their results by `call_id`, and confirmations. `run_started`,
 * `run_completed` and `run_failed` are stream lifecycle markers, not blocks,
 * so they produce nothing here. Item order in, block order out.
 */
export function buildTimeline(items: Item[]): TimelineEntry[] {
	const entries: TimelineEntry[] = [];
	const toolEntryByCallId = new Map<string, ToolEntry>();
	let draft: MessageEntry | undefined;
	let draftIndex = -1;

	for (const item of items) {
		switch (item.type) {
			case "message": {
				const finished: MessageEntry = {
					author: item.author ?? "krude",
					error: item.error,
					id: item.id ?? `message-${entries.length}`,
					kind: "message",
					pending: false,
					text: item.text ?? "",
				};
				// A finished message replaces its in-progress draft in place,
				// rather than leaving the draft bubble behind as a duplicate.
				if (draft) {
					entries[draftIndex] = finished;
					draft = undefined;
					draftIndex = -1;
				} else {
					entries.push(finished);
				}
				break;
			}
			case "delta": {
				if (!draft) {
					draft = {
						author: "krude",
						id: "draft",
						kind: "message",
						pending: true,
						text: "",
					};
					draftIndex = entries.length;
					entries.push(draft);
				}
				draft.text += item.text ?? "";
				break;
			}
			case "tool_call": {
				const callId = item.call_id ?? `call-${entries.length}`;
				const entry: ToolEntry = {
					arguments: item.arguments,
					callId,
					kind: "tool",
					state: "running",
					tool: item.tool ?? "tool",
				};
				toolEntryByCallId.set(callId, entry);
				entries.push(entry);
				break;
			}
			case "tool_result": {
				const existing = item.call_id
					? toolEntryByCallId.get(item.call_id)
					: undefined;
				if (existing) {
					existing.state = item.failed ? "failed" : "done";
					existing.resultText = item.text;
					existing.errorText = item.error;
				} else {
					// A result with no call in this window (history could start
					// mid-call): show it standalone rather than dropping it.
					entries.push({
						callId: item.call_id ?? `result-${entries.length}`,
						errorText: item.error,
						kind: "tool",
						resultText: item.text,
						state: item.failed ? "failed" : "done",
						tool: item.tool ?? "tool",
					});
				}
				break;
			}
			case "confirmation": {
				entries.push({
					arguments: item.arguments,
					callId: item.call_id ?? `confirmation-${entries.length}`,
					hint: item.hint,
					kind: "confirmation",
					status: item.status ?? "pending",
					tool: item.tool,
				});
				break;
			}
			default:
				break;
		}
	}

	return entries;
}

/** A tool's name in words: "sandbox_execute_code" -> "Sandbox execute code". */
export function humanizeToolName(tool: string): string {
	const spaced = tool.replace(/_/g, " ").trim();
	return spaced === ""
		? tool
		: spaced.charAt(0).toUpperCase() + spaced.slice(1);
}
