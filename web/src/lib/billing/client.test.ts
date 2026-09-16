import { describe, expect, it } from "vitest";
import {
	type BillingSummary,
	checkoutRange,
	hasOpenSubscription,
	memberAllowance,
	planAllowance,
	planName,
	type SubscriptionStatus,
} from "./client";

function summary(status: SubscriptionStatus | null): BillingSummary {
	return {
		can_manage: true,
		entitlements: {
			grace_ends_at: null,
			member_limit: 3,
			plan: "unpaid",
			status: "unpaid",
		},
		organization_id: "org_1",
		subscription:
			status === null
				? null
				: {
						ends_at: null,
						grace_ends_at: null,
						plan: "team",
						quantity: 1,
						renews_at: null,
						status,
					},
	};
}

describe("plans", () => {
	it("names plans and what they allow", () => {
		expect(planName("unpaid")).toBe("Unpaid");
		expect(planName("team")).toBe("Team");
		expect(planName("growth_plus")).toBe("Growth plus");
		expect(memberAllowance(null)).toBe("Unlimited members");
		expect(memberAllowance(1)).toBe("Up to 1 member");
		expect(planAllowance({ key: "team", member_limit: 25 })).toBe(
			"Up to 25 members",
		);
		expect(
			planAllowance({
				key: "seats",
				member_limit: null,
				member_limit_per_unit: 1,
				min_quantity: 5,
			}),
		).toBe("1 member per seat, from 5 seats");
	});

	it("bounds seats only for per-seat plans", () => {
		expect(checkoutRange({ key: "team", member_limit: 25 })).toBeUndefined();
		expect(
			checkoutRange({
				key: "seats",
				member_limit: null,
				member_limit_per_unit: 1,
				min_quantity: 5,
			}),
		).toEqual({ min: 5 });
	});
});

describe("hasOpenSubscription", () => {
	it("sends an organization with a subscription it can still fix to the portal", () => {
		expect(hasOpenSubscription(summary(null))).toBe(false);
		expect(hasOpenSubscription(summary("active"))).toBe(true);
		expect(hasOpenSubscription(summary("on_hold"))).toBe(true);
		expect(hasOpenSubscription(summary("cancelled"))).toBe(false);
	});
});
