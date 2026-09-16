import { afterEach, describe, expect, it, vi } from "vitest";
import { ApiError, apiErrorMessage, apiPath, apiRequest } from "./client";

function json(status: number, body: unknown): Response {
	return new Response(JSON.stringify(body), {
		headers: { "Content-Type": "application/json" },
		status,
	});
}

function empty(status: number): Response {
	return new Response(null, { status });
}

const expired = () => json(401, { error: "access token is required" });

afterEach(() => {
	vi.unstubAllGlobals();
});

describe("apiRequest", () => {
	it("posts JSON with the session cookie and returns the answer", async () => {
		const fetchMock = vi.fn().mockResolvedValue(json(201, { id: "org_1" }));
		vi.stubGlobal("fetch", fetchMock);

		await expect(
			apiRequest("/v1/organizations", { body: { name: "Odd Works" } }),
		).resolves.toEqual({ id: "org_1" });
		expect(fetchMock).toHaveBeenCalledWith(
			"/v1/organizations",
			expect.objectContaining({
				body: '{"name":"Odd Works"}',
				credentials: "include",
				method: "POST",
			}),
		);
	});

	it("sends form data as it is, for the browser to frame", async () => {
		const fetchMock = vi.fn().mockResolvedValue(json(201, { id: "file_1" }));
		vi.stubGlobal("fetch", fetchMock);
		const form = new FormData();
		form.set("scope", "member");

		await apiRequest("/v1/organizations/org_1/files", { body: form });
		expect(fetchMock).toHaveBeenCalledWith(
			"/v1/organizations/org_1/files",
			expect.objectContaining({
				body: form,
				headers: undefined,
				method: "POST",
			}),
		);
	});

	it("returns nothing for an empty answer", async () => {
		vi.stubGlobal("fetch", vi.fn().mockResolvedValue(empty(204)));

		await expect(
			apiRequest("/v1/auth/password/verify", { body: { token: "token" } }),
		).resolves.toBeUndefined();
	});

	it("reads both error shapes the services answer with", async () => {
		vi.stubGlobal(
			"fetch",
			vi
				.fn()
				.mockResolvedValueOnce(
					json(402, { error: "organization has reached its member limit" }),
				)
				.mockResolvedValueOnce(
					json(409, {
						error: { code: "already_connected", message: "already connected" },
					}),
				),
		);

		await expect(
			apiRequest("/v1/invitations/accept", { body: { token: "token" } }),
		).rejects.toMatchObject({
			message: "organization has reached its member limit",
			status: 402,
		});
		await expect(
			apiRequest("/v1/organizations/org_1/connections", { body: {} }),
		).rejects.toMatchObject({ code: "already_connected", status: 409 });
	});

	it("refreshes an expired session once and retries", async () => {
		const fetchMock = vi
			.fn()
			.mockResolvedValueOnce(expired())
			.mockResolvedValueOnce(empty(204))
			.mockResolvedValueOnce(json(200, { organizations: [] }));
		vi.stubGlobal("fetch", fetchMock);

		await expect(apiRequest("/v1/organizations")).resolves.toEqual({
			organizations: [],
		});
		expect(fetchMock.mock.calls.map(([url]) => url)).toEqual([
			"/v1/organizations",
			"/v1/auth/refresh",
			"/v1/organizations",
		]);
	});

	it("shares one refresh between requests that expire together", async () => {
		const attempts = new Map<string, number>();
		const fetchMock = vi.fn(async (url: string) => {
			if (url === "/v1/auth/refresh") {
				return new Promise<Response>((resolve) =>
					setTimeout(() => resolve(empty(204)), 10),
				);
			}
			const attempt = (attempts.get(url) ?? 0) + 1;
			attempts.set(url, attempt);
			return attempt === 1 ? expired() : json(200, { url });
		});
		vi.stubGlobal("fetch", fetchMock);

		await Promise.all([
			apiRequest("/v1/auth/me"),
			apiRequest("/v1/organizations"),
		]);
		expect(
			fetchMock.mock.calls.filter(([url]) => url === "/v1/auth/refresh"),
		).toHaveLength(1);
	});

	it("does not refresh when a 401 is the answer", async () => {
		const fetchMock = vi
			.fn()
			.mockResolvedValue(json(401, { error: "authentication failed" }));
		vi.stubGlobal("fetch", fetchMock);

		await expect(
			apiRequest("/v1/auth/password/login", {
				body: {},
				refreshSession: false,
			}),
		).rejects.toBeInstanceOf(ApiError);
		expect(fetchMock).toHaveBeenCalledTimes(1);
	});

	it("gives up when the session cannot be refreshed", async () => {
		const fetchMock = vi
			.fn()
			.mockResolvedValueOnce(expired())
			.mockResolvedValueOnce(
				json(401, { error: "refresh session is missing" }),
			);
		vi.stubGlobal("fetch", fetchMock);

		await expect(apiRequest("/v1/auth/me")).rejects.toMatchObject({
			status: 401,
		});
		expect(fetchMock).toHaveBeenCalledTimes(2);
	});
});

describe("apiPath", () => {
	it("encodes each value as one path segment", () => {
		expect(apiPath`/v1/organizations/${"org 1"}/members/${"a/b"}`).toBe(
			"/v1/organizations/org%201/members/a%2Fb",
		);
	});
});

describe("apiErrorMessage", () => {
	it("keeps what services explain and words the rest plainly", () => {
		expect(
			apiErrorMessage(
				new ApiError("organization slug must contain 2-63 characters", 400),
				"Could not save",
			),
		).toBe("Organization slug must contain 2-63 characters.");
		expect(
			apiErrorMessage(
				new ApiError("operation is not allowed", 403),
				"Could not save",
			),
		).toBe("You do not have permission to do that.");
		expect(
			apiErrorMessage(
				new ApiError("resource state conflicts with the request", 409),
				"Could not save",
			),
		).toBe("Something changed in the meantime. Refresh and try again.");
		expect(
			apiErrorMessage(
				new ApiError("internal server error", 500),
				"Could not save",
			),
		).toBe("Could not save");
		expect(apiErrorMessage(new Error("offline"), "Could not save")).toBe(
			"Could not save",
		);
	});
});
