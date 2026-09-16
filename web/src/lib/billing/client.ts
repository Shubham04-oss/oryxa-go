import { queryOptions } from "@tanstack/react-query";
import { apiPath, apiRequest } from "../api/client";

export interface Plan {
	key: string;
	max_quantity?: number;
	member_limit: number | null;
	member_limit_per_unit?: number;
	min_quantity?: number;
}

export interface PlanCatalog {
	plans: Plan[];
	unpaid: { member_limit: number | null };
}

export type SubscriptionStatus =
	| "pending"
	| "active"
	| "past_due"
	| "on_hold"
	| "paused"
	| "cancelled"
	| "failed"
	| "expired";

export interface Subscription {
	ends_at: string | null;
	grace_ends_at: string | null;
	plan: string;
	quantity: number;
	renews_at: string | null;
	status: SubscriptionStatus;
}

export interface BillingSummary {
	/** Whether the caller may start checkout or open the portal: owners only. */
	can_manage: boolean;
	entitlements: {
		grace_ends_at: string | null;
		member_limit: number | null;
		plan: string;
		status: string;
	};
	organization_id: string;
	subscription: Subscription | null;
}

export function getPlans(): Promise<PlanCatalog> {
	return apiRequest<PlanCatalog>("/v1/billing/plans");
}

function billingPath(organizationId: string): string {
	return apiPath`/v1/billing/organizations/${organizationId}`;
}

export function getBillingSummary(
	organizationId: string,
): Promise<BillingSummary> {
	return apiRequest<BillingSummary>(billingPath(organizationId));
}

/** Starts a Dodo checkout and answers where to send the browser. */
export async function startCheckout(
	organizationId: string,
	plan: string,
	quantity: number,
): Promise<string> {
	const { checkout_url } = await apiRequest<{ checkout_url: string }>(
		`${billingPath(organizationId)}/checkout`,
		{ body: { plan, quantity } },
	);
	return checkout_url;
}

/** Opens the Dodo customer portal and answers where to send the browser. */
export async function openBillingPortal(
	organizationId: string,
): Promise<string> {
	const { portal_url } = await apiRequest<{ portal_url: string }>(
		`${billingPath(organizationId)}/portal`,
		{ method: "POST" },
	);
	return portal_url;
}

export const plansQueryOptions = queryOptions({
	queryFn: getPlans,
	queryKey: ["billing", "plans"],
	staleTime: 5 * 60_000,
});

export function billingSummaryQueryOptions(organizationId: string) {
	return queryOptions({
		queryFn: () => getBillingSummary(organizationId),
		queryKey: ["billing", "organizations", organizationId],
	});
}

export function planName(key: string): string {
	const words = key.replace(/[-_]+/g, " ").trim();
	if (words === "" || words === "unpaid") {
		return "Unpaid";
	}
	return `${words[0].toUpperCase()}${words.slice(1)}`;
}

export function memberAllowance(limit: number | null): string {
	if (limit === null) {
		return "Unlimited members";
	}
	return limit === 1 ? "Up to 1 member" : `Up to ${limit} members`;
}

export function planAllowance(plan: Plan): string {
	const perUnit = plan.member_limit_per_unit;
	if (!perUnit) {
		return memberAllowance(plan.member_limit);
	}
	const perSeat =
		perUnit === 1 ? "1 member per seat" : `${perUnit} members per seat`;
	return plan.min_quantity
		? `${perSeat}, from ${plan.min_quantity} seats`
		: perSeat;
}

/** The seats a per-seat plan can be bought with; fixed plans have none. */
export function checkoutRange(
	plan: Plan,
): { max?: number; min: number } | undefined {
	if (!plan.member_limit_per_unit) {
		return undefined;
	}
	return {
		max: plan.max_quantity || undefined,
		min: Math.max(1, plan.min_quantity ?? 1),
	};
}

const openStatuses: ReadonlySet<SubscriptionStatus> = new Set([
	"active",
	"past_due",
	"on_hold",
	"paused",
]);

/** An open subscription is changed in the portal; billing refuses a second checkout. */
export function hasOpenSubscription(summary: BillingSummary): boolean {
	return (
		summary.subscription !== null &&
		openStatuses.has(summary.subscription.status)
	);
}

export const subscriptionStatusNames: Record<SubscriptionStatus, string> = {
	active: "Active",
	cancelled: "Cancelled",
	expired: "Expired",
	failed: "Payment failed",
	on_hold: "On hold",
	past_due: "Past due",
	paused: "Paused",
	pending: "Pending",
};
