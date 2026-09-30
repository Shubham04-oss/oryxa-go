import { createFileRoute, redirect } from "@tanstack/react-router"

// Where billing sends a member back, as BILLING_URL: from Dodo's checkout
// and customer portal, and from billing's emails, naming the organization
// as organization_id. It opens that workspace with Settings on Billing.
export const Route = createFileRoute("/settings/billing")({
  validateSearch: (search: Record<string, unknown>) => ({
    organization_id:
      typeof search.organization_id === "string"
        ? search.organization_id
        : undefined,
  }),
  beforeLoad: ({ search }) => {
    throw redirect({
      to: "/",
      search: { org: search.organization_id, view: "billing" },
      replace: true,
    })
  },
})
