import { afterEach, describe, expect, it, vi } from "vitest";
import {
	catchUpOnRoom,
	readRoom,
	roomQueryKey,
	roomQueryOptions,
	sayInRoom,
} from "./client";

function json(status: number, body: unknown): Response {
	return new Response(JSON.stringify(body), {
		headers: { "Content-Type": "application/json" },
		status,
	});
}

afterEach(() => {
	vi.unstubAllGlobals();
});

describe("Room API paths", () => {
	it("reads, says and catches up on a teamspace's conversation", async () => {
		const fetchMock = vi
			.fn()
			.mockResolvedValueOnce(
				json(200, {
					messages: [{ body: "morning", seq: 1 }],
					seats: [{ handle: "ada", kind: "person", present: true }],
				}),
			)
			.mockResolvedValueOnce(
				json(201, { messages: [{ body: "morning all", seq: 2 }] }),
			)
			.mockResolvedValueOnce(
				json(200, { brief: { revision: 3 }, messages: [{ seq: 2 }] }),
			);
		vi.stubGlobal("fetch", fetchMock);

		await expect(readRoom("org / one", "tsp / one")).resolves.toEqual({
			messages: [{ body: "morning", seq: 1 }],
			seats: [{ handle: "ada", kind: "person", present: true }],
		});
		await expect(
			sayInRoom("org / one", "tsp / one", "morning all"),
		).resolves.toEqual([{ body: "morning all", seq: 2 }]);
		await expect(catchUpOnRoom("org / one", "tsp / one", 1)).resolves.toEqual({
			brief: { revision: 3 },
			messages: [{ seq: 2 }],
		});

		expect(fetchMock.mock.calls.map(([url]) => url)).toEqual([
			"/v1/organizations/org%20%2F%20one/teamspaces/tsp%20%2F%20one/messages?limit=200",
			"/v1/organizations/org%20%2F%20one/teamspaces/tsp%20%2F%20one/messages",
			"/v1/organizations/org%20%2F%20one/teamspaces/tsp%20%2F%20one/catchup?after=1",
		]);
		expect(fetchMock.mock.calls[1]?.[1]).toEqual(
			expect.objectContaining({
				body: JSON.stringify({ body: "morning all" }),
				method: "POST",
			}),
		);
	});

	// A room that answers without the fields the page renders is a room the
	// page must still be able to open.
	it("reads an answer that leaves out what it has none of", async () => {
		vi.stubGlobal("fetch", vi.fn().mockResolvedValue(json(200, {})));

		await expect(readRoom("org_1", "tsp_1")).resolves.toEqual({
			messages: [],
			seats: [],
		});
	});

	it("keeps one cache entry per room and re-reads it while it is open", () => {
		expect(roomQueryKey("org_1", "tsp_1")).toEqual([
			"teamspaces",
			"org_1",
			"room",
			"tsp_1",
		]);
		const options = roomQueryOptions("org_1", "tsp_1");
		expect(options.queryKey).toEqual(roomQueryKey("org_1", "tsp_1"));
		// Reading is what tells the room somebody is in it; a page that stops
		// reading is a member their own agent will start answering for.
		expect(options.refetchInterval).toBe(5_000);
	});
});
