import type { UIMessage } from "ai";
import type { Item } from "./client";

/** The Durable Object tells the page its socket's cookie has expired. */
export const sessionExpiredCode = "session_expired";

/** The UI message shape kept in the thread's Durable Object SQLite store. */
export type ThreadChatMessage = UIMessage<
	{ oryxaConfirmation?: boolean },
	{ oryxa: Item }
>;

export function threadAgentName(
	organizationId: string,
	threadId: string,
): string {
	return `${organizationId}~${threadId}`;
}

export function itemPart(item: Item) {
	return { type: "data-oryxa" as const, data: item };
}

function partItem(part: UIMessage["parts"][number]): Item | undefined {
	return part.type === "data-oryxa" ? (part.data as Item) : undefined;
}

/**
 * Leaves out the `delta` parts a finished `message` part has made redundant.
 * Go streams a reply as deltas and then sends it whole, so once it lands the
 * deltas are only weight in storage — a long answer is hundreds of them. The
 * timeline swaps a draft for the next finished message, so the deltas before
 * one go; those after the last are a reply still being written, and stay.
 */
export function withoutSupersededDeltas<Message extends UIMessage>(
	message: Message,
): Message {
	const superseded = new Set<number>();
	let drafting: number[] = [];
	message.parts.forEach((part, index) => {
		const item = partItem(part);
		if (item?.type === "delta") {
			drafting.push(index);
		} else if (item?.type === "message") {
			for (const draftIndex of drafting) superseded.add(draftIndex);
			drafting = [];
		}
	});
	if (superseded.size === 0) return message;
	return {
		...message,
		parts: message.parts.filter((_, index) => !superseded.has(index)),
	};
}

/** Convert pre-Durable-Object conversations into the same visible transcript. */
export function itemsToChatMessages(items: Item[]): ThreadChatMessage[] {
	const messages: ThreadChatMessage[] = [];
	let assistant: ThreadChatMessage | undefined;
	for (const item of items) {
		if (item.type === "message" && item.author === "member") {
			messages.push({
				id: item.id ?? crypto.randomUUID(),
				role: "user",
				parts: [{ type: "text", text: item.text ?? "" }],
			});
			assistant = undefined;
			continue;
		}
		if (item.type === "run_started" || item.type === "run_completed") continue;
		if (!assistant) {
			assistant = { id: crypto.randomUUID(), role: "assistant", parts: [] };
			messages.push(assistant);
		}
		assistant.parts.push(itemPart(item));
	}
	return messages;
}

/** Keep the existing rich timeline while the AI SDK owns transport and storage. */
export function chatMessagesToItems(messages: ThreadChatMessage[]): Item[] {
	const items: Item[] = [];
	const confirmationIndex = new Map<string, number>();
	for (const message of messages) {
		if (message.role === "user") {
			if (message.metadata?.oryxaConfirmation) continue;
			const text = message.parts
				.filter((part) => part.type === "text")
				.map((part) => part.text)
				.join("");
			if (!text.trim()) continue;
			items.push({ type: "message", id: message.id, author: "member", text });
			continue;
		}
		for (const part of message.parts) {
			if (part.type !== "data-oryxa") continue;
			const item = part.data;
			if (item.type === "confirmation" && item.call_id) {
				const previous = confirmationIndex.get(item.call_id);
				if (previous !== undefined) {
					items[previous] = { ...items[previous], ...item };
					continue;
				}
				confirmationIndex.set(item.call_id, items.length);
			}
			items.push(item);
		}
	}
	return items;
}

function storedItemKey(item: Item): string {
	return [
		item.id ?? "",
		item.type,
		item.call_id ?? "",
		item.artifact ?? "",
		item.version ?? "",
	].join("\u0000");
}

/** Go can append app updates or agent work to a thread outside this WebSocket. */
export function reconcileChatMessages(
	stored: ThreadChatMessage[],
	backendItems: Item[],
): ThreadChatMessage[] {
	if (stored.length === 0)
		return backendItems.length > 0 ? itemsToChatMessages(backendItems) : stored;
	const visible = chatMessagesToItems(stored);
	const byKey = new Map<string, Item>();
	const memberCounts = new Map<string, number>();
	for (const item of visible) {
		if (item.type === "message" && item.author === "member") {
			const text = item.text ?? "";
			memberCounts.set(text, (memberCounts.get(text) ?? 0) + 1);
		} else if (item.id) {
			byKey.set(storedItemKey(item), item);
		}
	}
	const missing: Item[] = [];
	for (const item of backendItems) {
		if (item.type === "message" && item.author === "member") {
			const text = item.text ?? "";
			const count = memberCounts.get(text) ?? 0;
			if (count > 0) memberCounts.set(text, count - 1);
			else missing.push(item);
			continue;
		}
		const existing = item.id ? byKey.get(storedItemKey(item)) : undefined;
		if (
			existing &&
			item.type === "confirmation" &&
			existing.status !== item.status
		) {
			missing.push({
				type: "confirmation",
				call_id: item.call_id,
				status: item.status,
			});
		} else if (!existing) {
			missing.push(item);
		}
	}
	return missing.length > 0
		? [...stored, ...itemsToChatMessages(missing)]
		: stored;
}
