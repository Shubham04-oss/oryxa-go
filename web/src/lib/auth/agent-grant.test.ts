import { afterEach, describe, expect, it, vi } from "vitest";
import {
	authorizeAgentGrant,
	getAgentGrant,
	revokeAgentGrant,
} from "./agent-grant";

function json(status: number, body: unknown): Response {
	return new Response(JSON.stringify(body), {
		headers: { "Content-Type": "application/json" },
		status,
	});
}

function empty(status: number): Response {
	return new Response(null, { status });
}

afterEach(() => {
	vi.unstubAllGlobals();
});

describe("getAgentGrant", () => {
	it("reads whether the member has granted agent access", async () => {
		const fetchMock = vi
			.fn()
			.mockResolvedValue(
				json(200, { created_at: "2026-09-01T00:00:00Z", granted: true }),
			);
		vi.stubGlobal("fetch", fetchMock);

		await expect(getAgentGrant()).resolves.toEqual({
			created_at: "2026-09-01T00:00:00Z",
			granted: true,
		});
		expect(fetchMock).toHaveBeenCalledWith(
			"/v1/agent-grant",
			expect.objectContaining({ method: "GET" }),
		);
	});

	it("throws a 404 ApiError when agent grants are not configured on the server", async () => {
		vi.stubGlobal(
			"fetch",
			vi.fn().mockResolvedValue(json(404, { error: "not found" })),
		);

		await expect(getAgentGrant()).rejects.toMatchObject({ status: 404 });
	});
});

describe("authorizeAgentGrant", () => {
	it("posts the return_to URL and gets back where to send the member", async () => {
		const fetchMock = vi
			.fn()
			.mockResolvedValue(
				json(200, { authorization_url: "https://auth.example/authorize" }),
			);
		vi.stubGlobal("fetch", fetchMock);

		await expect(
			authorizeAgentGrant("https://app.example/app/settings/profile"),
		).resolves.toEqual({ authorization_url: "https://auth.example/authorize" });
		expect(fetchMock).toHaveBeenCalledWith(
			"/v1/agent-grant/authorize",
			expect.objectContaining({
				body: '{"return_to":"https://app.example/app/settings/profile"}',
				method: "POST",
			}),
		);
	});
});

describe("revokeAgentGrant", () => {
	it("deletes the grant", async () => {
		const fetchMock = vi.fn().mockResolvedValue(empty(204));
		vi.stubGlobal("fetch", fetchMock);

		await expect(revokeAgentGrant()).resolves.toBeUndefined();
		expect(fetchMock).toHaveBeenCalledWith(
			"/v1/agent-grant",
			expect.objectContaining({ method: "DELETE" }),
		);
	});
});
