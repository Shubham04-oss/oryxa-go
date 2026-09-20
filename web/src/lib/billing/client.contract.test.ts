import { afterEach, describe, expect, it, vi } from "vitest";
import {
	getBillingSummary,
	getPlans,
	openBillingPortal,
	startCheckout,
} from "./client";

function json(body: unknown): Response {
	return new Response(JSON.stringify(body), {
		headers: { "Content-Type": "application/json" },
		status: 200,
	});
}

afterEach(() => vi.unstubAllGlobals());

describe("billing API contracts", () => {
	it("preserves catalog, summary, checkout, and portal endpoints", async () => {
		const fetchMock = vi
			.fn()
			.mockResolvedValueOnce(json({ plans: [], unpaid: { member_limit: 1 } }))
			.mockResolvedValueOnce(json({ organization_id: "org_1" }))
			.mockResolvedValueOnce(json({ checkout_url: "https://checkout.test" }))
			.mockResolvedValueOnce(json({ portal_url: "https://portal.test" }));
		vi.stubGlobal("fetch", fetchMock);

		await getPlans();
		await getBillingSummary("org / one");
		await expect(startCheckout("org_1", "team", 3)).resolves.toBe(
			"https://checkout.test",
		);
		await expect(openBillingPortal("org_1")).resolves.toBe(
			"https://portal.test",
		);

		expect(
			fetchMock.mock.calls.map(([url, init]) => [url, init.method]),
		).toEqual([
			["/v1/billing/plans", "GET"],
			["/v1/billing/organizations/org%20%2F%20one", "GET"],
			["/v1/billing/organizations/org_1/checkout", "POST"],
			["/v1/billing/organizations/org_1/portal", "POST"],
		]);
	});
});
