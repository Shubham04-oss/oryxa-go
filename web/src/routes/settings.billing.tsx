import { createFileRoute, redirect } from "@tanstack/react-router";

// Billing's emails and Dodo's checkout return link to BILLING_URL, which the
// service's example configuration points at this path.
export const Route = createFileRoute("/settings/billing")({
	beforeLoad: () => {
		throw redirect({ to: "/app/settings/organization/billing" });
	},
});
