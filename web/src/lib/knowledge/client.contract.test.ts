import { afterEach, describe, expect, it, vi } from "vitest";
import {
	listProviders,
	listRuns,
	listSources,
	registerSource,
	removeSource,
} from "./client";

function json(body: unknown): Response {
	return new Response(JSON.stringify(body), {
		headers: { "Content-Type": "application/json" },
		status: 200,
	});
}

afterEach(() => vi.unstubAllGlobals());

describe("knowledge source API contracts", () => {
	it("preserves provider, source, and synchronization endpoints", async () => {
		const fetchMock = vi
			.fn()
			.mockImplementation(() =>
				Promise.resolve(json({ providers: [], runs: [], sources: [] })),
			);
		vi.stubGlobal("fetch", fetchMock);

		await expect(listProviders()).resolves.toEqual([]);
		await expect(
			listSources("org_1", {
				connectionId: "connection_1",
				provider: "github",
				scope: "organization",
			}),
		).resolves.toEqual([]);
		await registerSource("org_1", {
			connectionId: "connection_1",
			intervalSeconds: 3600,
			provider: "github",
			target: { repository: "odd/api" },
		});
		await removeSource("org_1", "source / one");
		await expect(listRuns("org_1", "source_1", 10)).resolves.toEqual([]);

		expect(
			fetchMock.mock.calls.map(([url, init]) => [url, init.method]),
		).toEqual([
			["/v1/sources/providers", "GET"],
			[
				"/v1/organizations/org_1/sources?connection_id=connection_1&provider=github&scope=organization",
				"GET",
			],
			["/v1/organizations/org_1/sources", "POST"],
			["/v1/organizations/org_1/sources/source%20%2F%20one", "DELETE"],
			["/v1/organizations/org_1/sources/source_1/runs?limit=10", "GET"],
		]);
	});
});
