import {
	type UseQueryResult,
	useMutation,
	useQuery,
} from "@tanstack/react-query";
import { createFileRoute } from "@tanstack/react-router";
import { ArrowUpRight, CalendarClock, CircleAlert } from "lucide-react";
import { type ReactNode, useId, useState } from "react";
import { AppAlert, QueryError } from "@/components/app/app-alert";
import {
	SettingsListEmpty,
	SettingsListSkeleton,
	SettingsPageBody,
	SettingsRow,
	SettingsSection,
	StatusBadge,
} from "@/components/app/organization-settings";
import { WorkspacePage } from "@/components/app/workspace-page";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { ApiError, apiErrorMessage } from "@/lib/api/client";
import { useWorkspace } from "@/lib/auth/session";
import {
	type BillingSummary,
	billingSummaryQueryOptions,
	checkoutRange,
	hasOpenSubscription,
	memberAllowance,
	openBillingPortal,
	type Plan,
	type PlanCatalog,
	planAllowance,
	planName,
	plansQueryOptions,
	type Subscription,
	startCheckout,
	subscriptionStatusNames,
} from "@/lib/billing/client";
import { formatDate } from "@/lib/format";

export const Route = createFileRoute("/app/settings/organization/billing")({
	component: OrganizationBillingPage,
	head: () => ({ meta: [{ title: "Billing · The Weirdly ODD" }] }),
});

function OrganizationBillingPage() {
	const { organization } = useWorkspace();
	const summary = useQuery(billingSummaryQueryOptions(organization.id));
	const plans = useQuery(plansQueryOptions);

	return (
		<WorkspacePage
			description={`Plan, member allowance, and who manages billing for ${organization.name}.`}
			title="Billing"
		>
			<SettingsPageBody>
				{summary.isPending ? (
					<SettingsSection
						description="The plan this organization is on."
						id="billing-plan"
						title="Current plan"
					>
						<SettingsListSkeleton />
					</SettingsSection>
				) : null}
				{summary.isError ? (
					<SettingsSection
						description="The plan this organization is on."
						id="billing-plan"
						title="Current plan"
					>
						<div className="settings-section-alert">
							<QueryError
								error={summary.error}
								fallback="Could not load billing"
								onRetry={summary.refetch}
							/>
						</div>
					</SettingsSection>
				) : null}
				{summary.data ? (
					<>
						<CurrentPlan summary={summary.data} />
						<PlanChoices plans={plans} summary={summary.data} />
					</>
				) : null}
				<SettingsSection
					description="Receipts, invoices, and billing notices."
					id="billing-contact"
					title="Billing contact"
				>
					<SettingsRow
						description="Dodo Payments sends receipts and invoices to the billing contact, and billing notices go there too. Owners change it in the billing portal."
						title="Where billing email goes"
					>
						<StatusBadge tone="neutral">Set in the portal</StatusBadge>
					</SettingsRow>
				</SettingsSection>
			</SettingsPageBody>
		</WorkspacePage>
	);
}

const entitlementStatuses: Record<
	string,
	{ name: string; tone: "active" | "neutral" | "warning" }
> = {
	active: { name: "Active", tone: "active" },
	past_due: { name: "Payment overdue", tone: "warning" },
	unpaid: { name: "No paid plan", tone: "neutral" },
};

function CurrentPlan({ summary }: { summary: BillingSummary }) {
	const { organization } = useWorkspace();
	const portal = useMutation({
		mutationFn: () => openBillingPortal(organization.id),
		onSuccess: (url) => window.location.assign(url),
	});
	const { entitlements, subscription } = summary;
	const status = entitlementStatuses[entitlements.status] ?? {
		name: entitlements.status,
		tone: "neutral",
	};

	return (
		<SettingsSection
			description={
				summary.can_manage
					? "Owners change the plan and how it is paid."
					: "Only organization owners can change billing."
			}
			footer={
				summary.can_manage && hasOpenSubscription(summary) ? (
					<>
						<span>
							Change plans, update payment, or cancel in the billing portal.
						</span>
						<Button
							disabled={portal.isPending}
							onClick={() => portal.mutate()}
							type="button"
						>
							{portal.isPending ? "Opening…" : "Manage billing"}
							<ArrowUpRight data-icon="inline-end" />
						</Button>
					</>
				) : undefined
			}
			id="billing-plan"
			title="Current plan"
		>
			{portal.isError ? (
				<div className="settings-section-alert">
					<AppAlert>
						{billingFailure(portal.error, "Could not open the billing portal")}
					</AppAlert>
				</div>
			) : null}
			<div className="settings-plan">
				<div>
					<span className="settings-plan-name">
						{planName(entitlements.plan)}
					</span>
					<StatusBadge tone={status.tone}>{status.name}</StatusBadge>
				</div>
				<p>{memberAllowance(entitlements.member_limit)}</p>
				<ul>
					<li>
						<CalendarClock />
						{subscription
							? subscriptionLine(subscription)
							: "No subscription yet"}
					</li>
					{entitlements.grace_ends_at ? (
						<li>
							<CircleAlert />
							The plan stays in place until{" "}
							{formatDate(entitlements.grace_ends_at)} while payment is retried
						</li>
					) : null}
				</ul>
			</div>
		</SettingsSection>
	);
}

function subscriptionLine(subscription: Subscription): string {
	const parts = [
		`${planName(subscription.plan)} subscription, ${subscriptionStatusNames[subscription.status].toLowerCase()}`,
	];
	if (subscription.quantity > 1) {
		parts.push(`${subscription.quantity} seats`);
	}
	if (subscription.ends_at) {
		parts.push(`ends ${formatDate(subscription.ends_at)}`);
	} else if (subscription.renews_at) {
		parts.push(`renews ${formatDate(subscription.renews_at)}`);
	}
	return parts.join(" · ");
}

interface PlanChoicesProps {
	plans: UseQueryResult<PlanCatalog>;
	summary: BillingSummary;
}

function PlanChoices({ plans, summary }: PlanChoicesProps) {
	const canCheckout = summary.can_manage && !hasOpenSubscription(summary);

	let content: ReactNode;
	if (plans.isPending) {
		content = <SettingsListSkeleton />;
	} else if (plans.isError) {
		content = (
			<div className="settings-section-alert">
				<QueryError
					error={plans.error}
					fallback="Could not load plans"
					onRetry={plans.refetch}
				/>
			</div>
		);
	} else if (plans.data.plans.length === 0) {
		content = (
			<SettingsListEmpty>No paid plans are offered yet.</SettingsListEmpty>
		);
	} else {
		content = (
			<div className="settings-list">
				{plans.data.plans.map((plan) => (
					<PlanRow
						canCheckout={canCheckout}
						current={summary.entitlements.plan === plan.key}
						key={plan.key}
						plan={plan}
					/>
				))}
			</div>
		);
	}

	return (
		<SettingsSection
			description={plansDescription(summary)}
			id="billing-plans"
			title="Plans"
		>
			{content}
		</SettingsSection>
	);
}

function plansDescription(summary: BillingSummary): string {
	if (hasOpenSubscription(summary)) {
		return "The organization already has a subscription. Change it in the billing portal.";
	}
	return summary.can_manage
		? "Checkout happens on Dodo Payments, which handles tax and invoices."
		: "An organization owner can choose a plan.";
}

interface PlanRowProps {
	canCheckout: boolean;
	current: boolean;
	plan: Plan;
}

function PlanRow({ canCheckout, current, plan }: PlanRowProps) {
	const { organization } = useWorkspace();
	const seatsId = useId();
	const range = checkoutRange(plan);
	const [seats, setSeats] = useState(String(range?.min ?? 1));
	const checkout = useMutation({
		mutationFn: (quantity: number) =>
			startCheckout(organization.id, plan.key, quantity),
		onSuccess: (url) => window.location.assign(url),
	});
	const quantity = range ? Number(seats) : 1;
	const seatsValid =
		!range ||
		(Number.isInteger(quantity) &&
			quantity >= range.min &&
			(range.max === undefined || quantity <= range.max));

	return (
		<div className="settings-list-row plan-row">
			<div className="settings-list-copy">
				<strong>{planName(plan.key)}</strong>
				<small>{planAllowance(plan)}</small>
				{checkout.isError ? (
					<small className="settings-list-error">
						{billingFailure(checkout.error, "Could not start checkout")}
					</small>
				) : null}
			</div>
			{current ? <StatusBadge tone="active">Current</StatusBadge> : null}
			{canCheckout && range ? (
				<div className="plan-seats">
					<Label className="sr-only" htmlFor={seatsId}>
						Seats for {planName(plan.key)}
					</Label>
					<Input
						id={seatsId}
						inputMode="numeric"
						max={range.max}
						min={range.min}
						onChange={(event) => setSeats(event.target.value)}
						type="number"
						value={seats}
					/>
					<span aria-hidden="true">seats</span>
				</div>
			) : null}
			{canCheckout ? (
				<Button
					disabled={!seatsValid || checkout.isPending}
					onClick={() => checkout.mutate(quantity)}
					size="sm"
					type="button"
				>
					{checkout.isPending ? "Opening…" : "Choose"}
				</Button>
			) : null}
		</div>
	);
}

function billingFailure(error: unknown, fallback: string): string {
	if (error instanceof ApiError && error.status === 503) {
		return "Billing is not set up in this environment yet.";
	}
	if (error instanceof ApiError && error.status === 404) {
		return "There is no billing account yet. Choose a plan first.";
	}
	return apiErrorMessage(error, fallback);
}
