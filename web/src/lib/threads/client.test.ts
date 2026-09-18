import { afterEach, describe, expect, it, vi } from "vitest";
import {
	createThread,
	deleteThread,
	getThread,
	type Item,
	listThreads,
	startRun,
} from "./client";

function json(status: number, body: unknown): Response {
	return new Response(JSON.stringify(body), {
		headers: { "Content-Type": "application/json" },
		status,
	});
}

function empty(status: number): Response {
	return new Response(null, { status });
}

const encoder = new TextEncoder();

/** A `text/event-stream` response body split across several chunks, as the network would deliver it. */
function streamResponse(chunks: string[]): Response {
	const stream = new ReadableStream<Uint8Array>({
		start(controller) {
			for (const chunk of chunks) {
				controller.enqueue(encoder.encode(chunk));
			}
			controller.close();
		},
	});
	return new Response(stream, {
		headers: { "Content-Type": "text/event-stream" },
		status: 200,
	});
}

function sse(type: string, payload: Record<string, unknown>): string {
	return `event: ${type}\ndata: ${JSON.stringify({ type, ...payload })}\n\n`;
}

afterEach(() => {
	vi.unstubAllGlobals();
});

describe("listThreads", () => {
	it("gets the organization's threads", async () => {
		const fetchMock = vi
			.fn()
			.mockResolvedValue(json(200, { threads: [{ id: "th_1" }] }));
		vi.stubGlobal("fetch", fetchMock);

		await expect(listThreads("org_1")).resolves.toEqual([{ id: "th_1" }]);
		expect(fetchMock).toHaveBeenCalledWith(
			"/v1/organizations/org_1/threads",
			expect.objectContaining({ method: "GET" }),
		);
	});
});

describe("createThread", () => {
	it("posts with no body to create one", async () => {
		const fetchMock = vi.fn().mockResolvedValue(json(201, { id: "th_1" }));
		vi.stubGlobal("fetch", fetchMock);

		await expect(createThread("org_1")).resolves.toEqual({ id: "th_1" });
		// apiRequest only defaults to POST when a body is given, so a
		// body-less create must say POST itself or it would silently GET.
		expect(fetchMock).toHaveBeenCalledWith(
			"/v1/organizations/org_1/threads",
			expect.objectContaining({ body: undefined, method: "POST" }),
		);
	});
});

describe("getThread", () => {
	it("gets one thread's detail", async () => {
		const fetchMock = vi
			.fn()
			.mockResolvedValue(
				json(200, { id: "th_1", items: [], pending_confirmations: [] }),
			);
		vi.stubGlobal("fetch", fetchMock);

		await getThread("org_1", "th_1");
		expect(fetchMock).toHaveBeenCalledWith(
			"/v1/organizations/org_1/threads/th_1",
			expect.objectContaining({ method: "GET" }),
		);
	});
});

describe("deleteThread", () => {
	it("deletes a thread", async () => {
		const fetchMock = vi.fn().mockResolvedValue(empty(204));
		vi.stubGlobal("fetch", fetchMock);

		await expect(deleteThread("org_1", "th_1")).resolves.toBeUndefined();
		expect(fetchMock).toHaveBeenCalledWith(
			"/v1/organizations/org_1/threads/th_1",
			expect.objectContaining({ method: "DELETE" }),
		);
	});
});

describe("startRun", () => {
	it("streams items to onItem in order, split across several chunks", async () => {
		const whole = [
			sse("run_started", { run_id: "run_1" }),
			sse("message", { author: "member", id: "item_1", text: "hi" }),
			sse("delta", { text: "Hel" }),
			sse("delta", { text: "lo" }),
			sse("message", { author: "krude", id: "item_2", text: "Hello" }),
			sse("run_completed", { run_id: "run_1" }),
		].join("");
		// Cut the whole payload into three chunks at arbitrary points, not
		// aligned to event boundaries.
		const cut1 = Math.floor(whole.length / 3);
		const cut2 = Math.floor((whole.length * 2) / 3);
		const fetchMock = vi
			.fn()
			.mockResolvedValue(
				streamResponse([
					whole.slice(0, cut1),
					whole.slice(cut1, cut2),
					whole.slice(cut2),
				]),
			);
		vi.stubGlobal("fetch", fetchMock);

		const items: Item[] = [];
		await startRun(
			"org_1",
			"th_1",
			{ message: "hi" },
			{
				onItem: (item) => items.push(item),
			},
		);

		expect(items.map((item) => item.type)).toEqual([
			"run_started",
			"message",
			"delta",
			"delta",
			"message",
			"run_completed",
		]);
		expect(items[1]).toMatchObject({ author: "member", text: "hi" });
		expect(items[4]).toMatchObject({ author: "krude", text: "Hello" });
		expect(fetchMock).toHaveBeenCalledWith(
			"/v1/organizations/org_1/threads/th_1/runs",
			expect.objectContaining({
				body: '{"message":"hi"}',
				credentials: "include",
				method: "POST",
			}),
		);
	});

	it("sends confirmation answers instead of a message", async () => {
		const fetchMock = vi
			.fn()
			.mockResolvedValue(streamResponse([sse("run_completed", {})]));
		vi.stubGlobal("fetch", fetchMock);

		await startRun(
			"org_1",
			"th_1",
			{ confirmations: [{ confirmed: true, id: "call_1" }] },
			{ onItem: () => undefined },
		);

		expect(fetchMock).toHaveBeenCalledWith(
			"/v1/organizations/org_1/threads/th_1/runs",
			expect.objectContaining({
				body: '{"confirmations":[{"confirmed":true,"id":"call_1"}]}',
			}),
		);
	});

	it("passes the abort signal through to fetch", async () => {
		const fetchMock = vi
			.fn()
			.mockResolvedValue(streamResponse([sse("run_completed", {})]));
		vi.stubGlobal("fetch", fetchMock);
		const controller = new AbortController();

		await startRun(
			"org_1",
			"th_1",
			{ message: "hi" },
			{
				onItem: () => undefined,
				signal: controller.signal,
			},
		);

		expect(fetchMock).toHaveBeenCalledWith(
			expect.any(String),
			expect.objectContaining({ signal: controller.signal }),
		);
	});

	it("throws ApiError on a refusal, and never calls onItem", async () => {
		vi.stubGlobal(
			"fetch",
			vi.fn().mockResolvedValue(
				json(409, {
					error: {
						code: "run_in_progress",
						message: "a run is already in progress",
					},
				}),
			),
		);
		const onItem = vi.fn();

		await expect(
			startRun("org_1", "th_1", { message: "hi" }, { onItem }),
		).rejects.toMatchObject({
			code: "run_in_progress",
			message: "a run is already in progress",
			status: 409,
		});
		expect(onItem).not.toHaveBeenCalled();
	});

	it("refreshes an expired session once, then retries and streams", async () => {
		const runsPath = "/v1/organizations/org_1/threads/th_1/runs";
		const fetchMock = vi
			.fn()
			.mockResolvedValueOnce(json(401, { error: "access token is required" }))
			.mockResolvedValueOnce(empty(204))
			.mockResolvedValueOnce(streamResponse([sse("run_completed", {})]));
		vi.stubGlobal("fetch", fetchMock);

		const onItem = vi.fn();
		await startRun("org_1", "th_1", { message: "hi" }, { onItem });

		expect(onItem).toHaveBeenCalledWith(
			expect.objectContaining({ type: "run_completed" }),
		);
		expect(fetchMock.mock.calls.map(([url]) => url)).toEqual([
			runsPath,
			"/v1/auth/refresh",
			runsPath,
		]);
	});
});
