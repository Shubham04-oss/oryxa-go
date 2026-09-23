import { useState } from "react"
import { useMutation, useQuery } from "@tanstack/react-query"
import { ArrowUpRight } from "lucide-react"
import { ApiError } from "@/lib/api/client"
import type { Organization } from "@/lib/auth/client"
import { authErrorMessage } from "@/lib/auth/client"
import {
  getBillingSummary,
  getPlans,
  openBillingPortal,
  startCheckout,
} from "@/lib/settings/client"
import type { Plan } from "@/lib/settings/client"

const planName = (key: string) =>
  key === "unpaid"
    ? "Unpaid"
    : key.replace(/[-_]/g, " ").replace(/^./, (c) => c.toUpperCase())
const allowance = (limit: number | null) =>
  limit === null
    ? "Unlimited members"
    : `Up to ${limit} ${limit === 1 ? "member" : "members"}`
const openStatuses = new Set(["active", "past_due", "on_hold", "paused"])

function PlanRow({
  plan,
  orgId,
  canCheckout,
  current,
}: {
  plan: Plan
  orgId: string
  canCheckout: boolean
  current: boolean
}) {
  const min = Math.max(1, plan.min_quantity ?? 1)
  const [seats, setSeats] = useState(String(min))
  const quantity = plan.member_limit_per_unit ? Number(seats) : 1
  const valid =
    Number.isInteger(quantity) &&
    quantity >= min &&
    (plan.max_quantity === undefined || quantity <= plan.max_quantity)
  const checkout = useMutation({
    mutationFn: () => startCheckout(orgId, plan.key, quantity),
    onSuccess: (url) => window.location.assign(url),
  })
  return (
    <div className="workspace-settings-row workspace-settings-plan-row">
      <div>
        <strong>
          {planName(plan.key)}{" "}
          {current && <span className="workspace-settings-tag">Current</span>}
        </strong>
        <small>
          {plan.member_limit_per_unit
            ? `${plan.member_limit_per_unit} members per seat`
            : allowance(plan.member_limit)}
        </small>
      </div>
      {canCheckout && (
        <div className="workspace-settings-plan-actions">
          {plan.member_limit_per_unit && (
            <label>
              Seats
              <input
                type="number"
                min={min}
                max={plan.max_quantity}
                value={seats}
                onChange={(event) => setSeats(event.target.value)}
              />
            </label>
          )}
          <button
            type="button"
            disabled={!valid || checkout.isPending}
            onClick={() => checkout.mutate()}
          >
            {checkout.isPending ? "Opening…" : "Choose"}
          </button>
        </div>
      )}
      {checkout.isError && (
        <p className="workspace-settings-error" role="alert">
          {authErrorMessage(checkout.error, "Could not start checkout.")}
        </p>
      )}
    </div>
  )
}

export function WorkspaceBillingSettings({
  organization,
  preview,
}: {
  organization?: Organization
  preview: boolean
}) {
  const summary = useQuery({
    queryKey: ["billing", "summary", organization?.id],
    queryFn: () => getBillingSummary(organization!.id),
    enabled: !preview && Boolean(organization),
    retry: false,
  })
  const plans = useQuery({
    queryKey: ["billing", "plans"],
    queryFn: getPlans,
    enabled: !preview && Boolean(organization),
    staleTime: 5 * 60_000,
    retry: false,
  })
  const portal = useMutation({
    mutationFn: () => openBillingPortal(organization!.id),
    onSuccess: (url) => window.location.assign(url),
  })

  if (preview || !organization)
    return (
      <div className="workspace-settings-account">
        <div className="workspace-settings-section">
          <div className="workspace-settings-section-title">
            <h3>Current plan</h3>
            <p>
              Layout preview · Sign in to load the real plan and subscription.
            </p>
          </div>
          <div className="workspace-settings-billing-plan">
            <strong>Unpaid</strong>
            <span className="workspace-settings-tag">Example</span>
          </div>
          <p className="workspace-settings-hint">
            The plan, member allowance, and renewal date appear here.
          </p>
        </div>
        <div className="workspace-settings-section">
          <div className="workspace-settings-section-title">
            <h3>Plans</h3>
            <p>
              Available plans and seat limits load from Billing. Owners can
              start checkout or open the billing portal.
            </p>
          </div>
        </div>
        <div className="workspace-settings-section">
          <div className="workspace-settings-section-title">
            <h3>Billing contact</h3>
            <p>
              Receipts and invoices go to the contact set in the billing portal.
            </p>
          </div>
        </div>
      </div>
    )

  if (summary.isPending)
    return (
      <div className="workspace-settings-account">
        <p role="status">Loading billing…</p>
      </div>
    )
  if (summary.isError)
    return (
      <div className="workspace-settings-account">
        <div className="workspace-settings-section">
          <p className="workspace-settings-error" role="alert">
            {summary.error instanceof ApiError && summary.error.status === 404
              ? "Billing is not available in this environment."
              : authErrorMessage(summary.error, "Could not load billing.")}
          </p>
          <button type="button" onClick={() => void summary.refetch()}>
            Retry
          </button>
        </div>
      </div>
    )

  const data = summary.data
  const hasSubscription = Boolean(
    data.subscription && openStatuses.has(data.subscription.status)
  )
  const canCheckout = data.can_manage && !hasSubscription
  return (
    <div className="workspace-settings-account">
      <section className="workspace-settings-section" aria-label="Current plan">
        <div className="workspace-settings-section-title">
          <h3>Current plan</h3>
          <p>
            {data.can_manage
              ? "Owners manage the plan and payment."
              : "Only owners can manage billing."}
          </p>
        </div>
        <div className="workspace-settings-billing-plan">
          <strong>{planName(data.entitlements.plan)}</strong>
          <span className="workspace-settings-tag">
            {data.entitlements.status.replace(/_/g, " ")}
          </span>
        </div>
        <p className="workspace-settings-hint">
          {allowance(data.entitlements.member_limit)}
        </p>
        {data.subscription && (
          <p className="workspace-settings-hint">
            {planName(data.subscription.plan)} subscription ·{" "}
            {data.subscription.status.replace(/_/g, " ")}
            {data.subscription.quantity > 1
              ? ` · ${data.subscription.quantity} seats`
              : ""}
            {data.subscription.renews_at
              ? ` · renews ${new Date(data.subscription.renews_at).toLocaleDateString()}`
              : ""}
          </p>
        )}
        {data.entitlements.grace_ends_at && (
          <p className="workspace-settings-error">
            Payment is being retried until{" "}
            {new Date(data.entitlements.grace_ends_at).toLocaleDateString()}.
          </p>
        )}
        {data.can_manage && hasSubscription && (
          <button
            className="workspace-settings-action"
            type="button"
            disabled={portal.isPending}
            onClick={() => portal.mutate()}
          >
            {portal.isPending ? "Opening…" : "Manage billing"}{" "}
            <ArrowUpRight size={14} />
          </button>
        )}
        {portal.isError && (
          <p className="workspace-settings-error" role="alert">
            {authErrorMessage(
              portal.error,
              "Could not open the billing portal."
            )}
          </p>
        )}
      </section>
      <section className="workspace-settings-section" aria-label="Plans">
        <div className="workspace-settings-section-title">
          <h3>Plans</h3>
          <p>
            {hasSubscription
              ? "Change plans in the billing portal."
              : data.can_manage
                ? "Checkout opens securely with the payment provider."
                : "An organisation owner can choose a plan."}
          </p>
        </div>
        {plans.isPending && <p role="status">Loading plans…</p>}
        {plans.isError && (
          <p className="workspace-settings-error" role="alert">
            {authErrorMessage(plans.error, "Could not load plans.")}{" "}
            <button type="button" onClick={() => void plans.refetch()}>
              Retry
            </button>
          </p>
        )}
        {plans.data?.plans.length === 0 && (
          <p className="workspace-settings-hint">
            No paid plans are available yet.
          </p>
        )}
        {plans.data?.plans.map((plan) => (
          <PlanRow
            key={plan.key}
            plan={plan}
            orgId={organization.id}
            canCheckout={canCheckout}
            current={data.entitlements.plan === plan.key}
          />
        ))}
      </section>
      <section
        className="workspace-settings-section"
        aria-label="Billing contact"
      >
        <div className="workspace-settings-section-title">
          <h3>Billing contact</h3>
          <p>
            Receipts, invoices, and billing notices go to the contact set in the
            billing portal.
          </p>
        </div>
      </section>
    </div>
  )
}
