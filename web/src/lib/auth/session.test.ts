import { QueryClient } from "@tanstack/react-query";
import { isRedirect } from "@tanstack/react-router";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { Organization } from "./client";
import { ensureSession, resolveOrganization, safeRedirect } from "./session";

function organization(id: string): Organization {
	return {
		created_at: "2026-09-15T00:00:00Z",
		created_by: "user-1",
		id,
		name: id,
		slug: id,
		updated_at: "2026-09-15T00:00:00Z",
	};
}

function answer(status: number, body: unknown) {
	return async () =>
		new Response(JSON.stringify(body), {
			headers: { "Content-Type": "application/json" },
			status,
		});
}

afterEach(() => {
	vi.unstubAllGlobals();
});

describe("resolveOrganization", () => {
	const organizations = [organization("org_1"), organization("org_2")];

	it("keeps the organization the member chose", () => {
		expect(resolveOrganization(organizations, "org_2")?.id).toBe("org_2");
	});

	it("falls back to the first organization when there is no choice to keep", () => {
		expect(resolveOrganization(organizations, "org_left")?.id).toBe("org_1");
		expect(resolveOrganization(organizations, null)?.id).toBe("org_1");
		expect(resolveOrganization([], "org_1")).toBeUndefined();
	});
});

describe("safeRedirect", () => {
	it("keeps a path on this app, with its query", () => {
		expect(safeRedirect("/invitations/accept?token=abc")).toBe(
			"/invitations/accept?token=abc",
		);
	});

	it("refuses anything that leaves the app", () => {
		for (const value of [
			"https://evil.example/app",
			"//evil.example",
			"/\\evil.example",
			"/\t/evil.example",
			"app",
			"",
			undefined,
			42,
		]) {
			expect(safeRedirect(value)).toBeUndefined();
		}
	});
});

describe("ensureSession", () => {
	it("resolves the signed-in member's session", async () => {
		const session = {
			organizations: [organization("org_1")],
			user: { email: "asha@example.com", id: "user-1" },
		};
		vi.stubGlobal("fetch", vi.fn(answer(200, session)));

		await expect(ensureSession(new QueryClient(), "/app")).resolves.toEqual(
			session,
		);
	});

	it("sends a signed-out visitor to sign in, and back afterwards", async () => {
		vi.stubGlobal(
			"fetch",
			vi.fn(answer(401, { error: "access token is required" })),
		);

		const outcome = await ensureSession(new QueryClient(), "/app/teams").catch(
			(error: unknown) => error,
		);
		expect(isRedirect(outcome)).toBe(true);
		expect(outcome).toMatchObject({
			options: { search: { redirect: "/app/teams" }, to: "/auth/sign-in" },
		});
	});
});
