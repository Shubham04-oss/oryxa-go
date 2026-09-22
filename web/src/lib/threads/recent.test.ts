import { describe, expect, it } from "vitest";
import {
	recentThreads,
	SIDEBAR_THREAD_LIMIT,
	type Thread,
	threadTitle,
} from "./client";

function thread(id: string, updated_at: string, title = ""): Thread {
	return { id, title, updated_at };
}

describe("recentThreads", () => {
	const threads = [
		thread("old", "2026-09-01T00:00:00Z"),
		thread("newest", "2026-09-20T00:00:00Z"),
		thread("middle", "2026-09-10T00:00:00Z"),
		thread("older", "2026-09-05T00:00:00Z"),
	];

	it("puts the most recently touched conversation on top", () => {
		expect(recentThreads(threads).map((item) => item.id)).toEqual([
			"newest",
			"middle",
			"older",
		]);
	});

	it("shows no more than the sidebar has room for", () => {
		expect(recentThreads(threads)).toHaveLength(SIDEBAR_THREAD_LIMIT);
		expect(recentThreads(threads, 2)).toHaveLength(2);
	});

	it("leaves the caller's array alone", () => {
		const order = threads.map((item) => item.id);
		recentThreads(threads);
		expect(threads.map((item) => item.id)).toEqual(order);
	});

	it("keeps the conversation being read in view, in the last slot", () => {
		expect(recentThreads(threads, 3, "old").map((item) => item.id)).toEqual([
			"newest",
			"middle",
			"old",
		]);
	});

	it("does not move a conversation that is already in view", () => {
		expect(recentThreads(threads, 3, "middle").map((item) => item.id)).toEqual([
			"newest",
			"middle",
			"older",
		]);
	});

	it("ignores a conversation it has never heard of", () => {
		expect(recentThreads(threads, 3, "ghost").map((item) => item.id)).toEqual([
			"newest",
			"middle",
			"older",
		]);
	});

	it("copes with nothing to show", () => {
		expect(recentThreads(undefined)).toEqual([]);
		expect(recentThreads([])).toEqual([]);
		expect(recentThreads(threads, 0)).toEqual([]);
		expect(recentThreads(threads, -1)).toEqual([]);
		expect(recentThreads(threads, 0, "old")).toEqual([]);
	});
});

describe("threadTitle", () => {
	it("names an untitled conversation", () => {
		expect(threadTitle({ title: "" })).toBe("New conversation");
		expect(threadTitle({ title: "   " })).toBe("New conversation");
	});

	it("keeps a title it has", () => {
		expect(threadTitle({ title: "March reconciliation" })).toBe(
			"March reconciliation",
		);
	});
});
