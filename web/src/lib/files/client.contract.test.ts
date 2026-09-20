import { afterEach, describe, expect, it, vi } from "vitest";
import { deleteFile, getFile, listFiles, uploadFile } from "./client";

function json(body: unknown): Response {
	return new Response(JSON.stringify(body), {
		headers: { "Content-Type": "application/json" },
		status: 200,
	});
}

afterEach(() => vi.unstubAllGlobals());

describe("files API contracts", () => {
	it("preserves list, detail, upload, and delete endpoints", async () => {
		const fetchMock = vi
			.fn()
			.mockImplementation(() =>
				Promise.resolve(json({ files: [], id: "file_1" })),
			);
		vi.stubGlobal("fetch", fetchMock);

		await expect(
			listFiles("org / one", { scope: "team", teamId: "team / one" }),
		).resolves.toEqual([]);
		await getFile("org_1", "file / one");
		await uploadFile("org_1", {
			file: new File(["hello"], "notes.txt", { type: "text/plain" }),
			retentionDays: 30,
			scope: "team",
			teamId: "team_1",
		});
		await deleteFile("org_1", "file_1");

		expect(
			fetchMock.mock.calls.map(([url, init]) => [url, init.method]),
		).toEqual([
			[
				"/v1/organizations/org%20%2F%20one/files?scope=team&team_id=team+%2F+one",
				"GET",
			],
			["/v1/organizations/org_1/files/file%20%2F%20one", "GET"],
			["/v1/organizations/org_1/files", "POST"],
			["/v1/organizations/org_1/files/file_1", "DELETE"],
		]);
		expect(fetchMock.mock.calls[2]?.[1]?.body).toBeInstanceOf(FormData);
	});
});
