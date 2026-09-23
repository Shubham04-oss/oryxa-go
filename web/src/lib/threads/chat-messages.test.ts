import { describe, expect, it } from "vitest";
import {
	chatMessagesToItems,
	itemPart,
	itemsToChatMessages,
	reconcileChatMessages,
	type ThreadChatMessage,
	withoutSupersededDeltas,
} from "./chat-messages";
import type { Item } from "./client";
import { buildTimeline } from "./timeline";

describe("Durable Object transcript", () => {
	it("imports an older conversation without losing tools or artifacts", () => {
		const items = [
			{
				type: "message" as const,
				id: "user-1",
				author: "member",
				text: "Make a chart",
			},
			{
				type: "tool_call" as const,
				id: "tool-1",
				call_id: "call-1",
				tool: "make_chart",
			},
			{
				type: "tool_result" as const,
				id: "tool-2",
				call_id: "call-1",
				tool: "make_chart",
			},
			{
				type: "artifact" as const,
				id: "file-1",
				artifact: "chart.png",
				version: 1,
			},
			{
				type: "message" as const,
				id: "answer-1",
				author: "krude",
				text: "Done",
			},
		];
		expect(chatMessagesToItems(itemsToChatMessages(items))).toEqual(items);
	});

	it("keeps the latest approval state in the original card", () => {
		const messages = itemsToChatMessages([
			{
				type: "confirmation",
				call_id: "approve-1",
				tool: "send_email",
				status: "pending",
				arguments: { to: "a@example.com" },
			},
		]);
		messages.push({
			id: "approval",
			role: "assistant",
			parts: [
				{
					type: "data-oryxa",
					data: {
						type: "confirmation",
						call_id: "approve-1",
						status: "approved",
					},
				},
			],
		});
		expect(chatMessagesToItems(messages)).toEqual([
			{
				type: "confirmation",
				call_id: "approve-1",
				tool: "send_email",
				status: "approved",
				arguments: { to: "a@example.com" },
			},
		]);
	});

	it("imports later Go events once without duplicating the member's text", () => {
		const stored = itemsToChatMessages([
			{ type: "message", id: "old-user", author: "member", text: "Hello" },
			{ type: "message", id: "old-answer", author: "krude", text: "Hi" },
		]);
		const backend = [
			{
				type: "message" as const,
				id: "go-user",
				author: "member",
				text: "Hello",
			},
			{
				type: "message" as const,
				id: "old-answer",
				author: "krude",
				text: "Hi",
			},
			{
				type: "message" as const,
				id: "app-update",
				author: "apps",
				text: "New calendar event",
			},
		];
		const merged = reconcileChatMessages(stored, backend);
		expect(chatMessagesToItems(merged).map((item) => item.text)).toEqual([
			"Hello",
			"Hi",
			"New calendar event",
		]);
		expect(reconcileChatMessages(merged, backend)).toBe(merged);
		expect(reconcileChatMessages([], [])).toEqual([]);
	});
});

describe("withoutSupersededDeltas", () => {
	function answer(items: Item[]): ThreadChatMessage {
		return { id: "answer", role: "assistant", parts: items.map(itemPart) };
	}

	it("stores a finished reply without the deltas that drafted it", () => {
		const streamed = answer([
			{ type: "run_started", run_id: "run-1" },
			{ type: "delta", text: "Rotation " },
			{ type: "delta", text: "is every " },
			{ type: "delta", text: "ninety days." },
			{
				type: "message",
				id: "reply-1",
				author: "krude",
				text: "Rotation is every ninety days.",
			},
			{ type: "run_completed", run_id: "run-1" },
		]);
		const stored = withoutSupersededDeltas(streamed);
		expect(chatMessagesToItems([stored]).map((item) => item.type)).toEqual([
			"run_started",
			"message",
			"run_completed",
		]);
		// What the conversation shows is unchanged by leaving them out.
		expect(buildTimeline(chatMessagesToItems([stored]))).toEqual(
			buildTimeline(chatMessagesToItems([streamed])),
		);
	});

	it("keeps the deltas of a reply still being written", () => {
		const streamed = answer([
			{ type: "delta", text: "Looking" },
			{ type: "message", id: "reply-1", author: "krude", text: "Looking." },
			{ type: "tool_call", id: "call-1", call_id: "call-1", tool: "search" },
			{ type: "delta", text: "Found " },
			{ type: "delta", text: "it" },
		]);
		expect(
			chatMessagesToItems([withoutSupersededDeltas(streamed)]).map(
				(item) => item.text ?? item.type,
			),
		).toEqual(["Looking.", "tool_call", "Found ", "it"]);
	});

	it("leaves a message with nothing to drop as it was", () => {
		const member: ThreadChatMessage = {
			id: "member",
			role: "user",
			parts: [{ type: "text", text: "Hello" }],
		};
		expect(withoutSupersededDeltas(member)).toBe(member);
	});
});
