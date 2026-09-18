import { describe, expect, it } from "vitest";
import type { Item } from "./client";
import { buildTimeline, humanizeToolName } from "./timeline";

describe("buildTimeline", () => {
	it("renders the member's message and Krude's finished message in order", () => {
		const items: Item[] = [
			{ author: "member", id: "i1", text: "hi", type: "message" },
			{ author: "krude", id: "i2", text: "hello", type: "message" },
		];

		expect(buildTimeline(items)).toEqual([
			{
				author: "member",
				error: undefined,
				id: "i1",
				kind: "message",
				pending: false,
				text: "hi",
			},
			{
				author: "krude",
				error: undefined,
				id: "i2",
				kind: "message",
				pending: false,
				text: "hello",
			},
		]);
	});

	it("folds deltas into a single growing draft bubble", () => {
		const items: Item[] = [
			{ text: "Hel", type: "delta" },
			{ text: "lo", type: "delta" },
		];

		expect(buildTimeline(items)).toEqual([
			{
				author: "krude",
				id: "draft",
				kind: "message",
				pending: true,
				text: "Hello",
			},
		]);
	});

	it("replaces the draft with the finished message instead of duplicating it", () => {
		const items: Item[] = [
			{ text: "Hel", type: "delta" },
			{ text: "lo", type: "delta" },
			{ author: "krude", id: "i1", text: "Hello", type: "message" },
		];

		const entries = buildTimeline(items);
		expect(entries).toHaveLength(1);
		expect(entries[0]).toMatchObject({
			id: "i1",
			pending: false,
			text: "Hello",
		});
	});

	it("shows a message's error as part of the entry", () => {
		const items: Item[] = [
			{
				author: "krude",
				error: "the model stopped early",
				id: "i1",
				text: "",
				type: "message",
			},
		];

		expect(buildTimeline(items)[0]).toMatchObject({
			error: "the model stopped early",
		});
	});

	it("pairs a tool_call with its later tool_result by call_id", () => {
		const items: Item[] = [
			{
				arguments: { path: "a.txt" },
				call_id: "call_1",
				tool: "sandbox_execute_code",
				type: "tool_call",
			},
			{ call_id: "call_1", text: "42", type: "tool_result" },
		];

		expect(buildTimeline(items)).toEqual([
			{
				arguments: { path: "a.txt" },
				callId: "call_1",
				errorText: undefined,
				kind: "tool",
				resultText: "42",
				state: "done",
				tool: "sandbox_execute_code",
			},
		]);
	});

	it("marks a failed tool_result on its call", () => {
		const items: Item[] = [
			{ call_id: "call_1", tool: "researcher", type: "tool_call" },
			{
				call_id: "call_1",
				error: "timed out",
				failed: true,
				type: "tool_result",
			},
		];

		expect(buildTimeline(items)[0]).toMatchObject({
			errorText: "timed out",
			state: "failed",
		});
	});

	it("still shows a tool_result that arrives with no matching call", () => {
		const items: Item[] = [
			{ call_id: "call_9", text: "done", type: "tool_result" },
		];

		expect(buildTimeline(items)).toEqual([
			{
				arguments: undefined,
				callId: "call_9",
				errorText: undefined,
				kind: "tool",
				resultText: "done",
				state: "done",
				tool: "tool",
			},
		]);
	});

	it("keeps multiple concurrent tool calls separate by call_id", () => {
		const items: Item[] = [
			{ call_id: "call_1", tool: "a", type: "tool_call" },
			{ call_id: "call_2", tool: "b", type: "tool_call" },
			{ call_id: "call_2", text: "b done", type: "tool_result" },
		];

		const entries = buildTimeline(items);
		expect(entries).toHaveLength(2);
		expect(entries[0]).toMatchObject({ callId: "call_1", state: "running" });
		expect(entries[1]).toMatchObject({ callId: "call_2", state: "done" });
	});

	it("renders a confirmation with its hint and arguments", () => {
		const items: Item[] = [
			{
				arguments: { channel: "#general" },
				call_id: "call_1",
				hint: "This posts a message visible to the channel.",
				status: "pending",
				tool: "slack_post_message",
				type: "confirmation",
			},
		];

		expect(buildTimeline(items)).toEqual([
			{
				arguments: { channel: "#general" },
				callId: "call_1",
				hint: "This posts a message visible to the channel.",
				kind: "confirmation",
				status: "pending",
				tool: "slack_post_message",
			},
		]);
	});

	it("defaults a confirmation with no status to pending", () => {
		const items: Item[] = [{ call_id: "call_1", type: "confirmation" }];

		expect(buildTimeline(items)[0]).toMatchObject({ status: "pending" });
	});

	it("produces nothing for run lifecycle markers", () => {
		const items: Item[] = [
			{ run_id: "run_1", type: "run_started" },
			{ run_id: "run_1", type: "run_completed" },
			{ error: "boom", type: "run_failed" },
		];

		expect(buildTimeline(items)).toEqual([]);
	});
});

describe("humanizeToolName", () => {
	it("turns a snake_case tool name into a sentence-cased phrase", () => {
		expect(humanizeToolName("sandbox_execute_code")).toBe(
			"Sandbox execute code",
		);
		expect(humanizeToolName("apps_run_write_tool")).toBe("Apps run write tool");
		expect(humanizeToolName("researcher")).toBe("Researcher");
	});
});
