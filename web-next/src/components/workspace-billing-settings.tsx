import { useState } from "react"
import { useMutation, useQuery } from "@tanstack/react-query"
import { ArrowUpRight } from "lucide-react"
import { Input } from "@/components/ui/input"
import { ApiError } from "@/lib/api/client"
import type { Organization } from "@/lib/auth/client"
import { authErrorMessage } from "@/lib/auth/client"
import { getMembers } from "@/lib/auth/invitations"
import {
  getBillingSummary,
  getPlans,
  openBillingPortal,
  startCheckout,
} from "@/lib/settings/client"
import type { Plan } from "@/lib/settings/client"
import {
  SettingsButton,
  SettingsCard,
  SettingsGroup,
  SettingsMessage,
  SettingsNote,
  SettingsRow,
} from "./settings-kit"

const planName = (key: string) =>
  key === "unpaid"
    ? "Unpaid"
    : key.replace(/[-_]/g, " ").replace(/^./, (c) => c.toUpperCase())
const allowance = (limit: number | null) =>
  limit === null
    ? "Unlimited members"
    : `Up to ${limit} ${limit === 1 ? "member" : "members"}`
const openStatuses = new Set(["active", "past_due", "on_hold", "paused"])
const longDate = new Intl.DateTimeFormat("en-GB", {
  day: "numeric",
  month: "long",
  year: "numeric",
})

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
    <>
      <SettingsRow
        title={current ? `${planName(plan.key)} · current` : planName(plan.key)}
        description={
          plan.member_limit_per_unit
            ? `${plan.member_limit_per_unit} members per seat`
            : allowance(plan.member_limit)
        }
      >
        {canCheckout && plan.member_limit_per_unit && (
          <Input
            className="settings-input settings-seats-input"
            type="number"
            aria-label={`Seats for ${planName(plan.key)}`}
            min={min}
            max={plan.max_quantity}
            value={seats}
            onChange={(event) => setSeats(event.target.value)}
          />
        )}
        {canCheckout && (
          <SettingsButton
            disabled={!valid || checkout.isPending}
            onClick={() => checkout.mutate()}
          >
            {checkout.isPending ? "Opening…" : "Choose"}
          </SettingsButton>
        )}
      </SettingsRow>
      {checkout.isError && (
        <SettingsMessage tone="error">
          {authErrorMessage(checkout.error, "Could not start checkout.")}
        </SettingsMessage>
      )}
    </>
  )
}

/**
 * Settings → Billing: the plan as one card (its name, renewal, seats used
 * against the limit, and Change plan), the plans to choose from while there
 * is no subscription, and payment, which the billing portal holds.
 *
 * Plan prices, the card on file, the billing contact and invoices are not on
 * the billing routes yet, so the card names no price and the portal stands in
 * for the rest.
 */
export function WorkspaceBillingSettings({
  organization,
  preview,
}: {
  organization?: Organization
  preview: boolean
}) {
  const live = !preview && Boolean(organization)
  const [choosing, setChoosing] = useState(false)
  const summary = useQuery({
    queryKey: ["billing", "summary", organization?.id],
    queryFn: () => getBillingSummary(organization!.id),
    enabled: live,
    retry: false,
  })
  const plans = useQuery({
    queryKey: ["billing", "plans"],
    queryFn: getPlans,
    enabled: live,
    staleTime: 5 * 60_000,
    retry: false,
  })
  const members = useQuery({
    queryKey: ["organizations", organization?.id, "members"],
    queryFn: () => getMembers(organization!.id),
    enabled: live,
    retry: false,
  })
  const portal = useMutation({
    mutationFn: () => openBillingPortal(organization!.id),
    onSuccess: (url) => window.location.assign(url),
  })

  if (preview || !organization) {
    return (
      <SettingsGroup label="Current plan">
        <SettingsNote>
          Layout preview. Sign in to load the real plan and subscription.
        </SettingsNote>
      </SettingsGroup>
    )
  }
  if (summary.isPending) return <SettingsNote>Loading billing…</SettingsNote>
  if (summary.isError) {
    return (
      <SettingsGroup label="Current plan">
        <SettingsMessage tone="error">
          {summary.error instanceof ApiError && summary.error.status === 404
            ? "Billing is not available in this environment."
            : authErrorMessage(summary.error, "Could not load billing.")}{" "}
          <SettingsButton onClick={() => void summary.refetch()}>
            Retry
          </SettingsButton>
        </SettingsMessage>
      </SettingsGroup>
    )
  }

  const data = summary.data
  const subscription = data.subscription
  const hasSubscription = Boolean(
    subscription && openStatuses.has(subscription.status)
  )
  const canCheckout = data.can_manage && !hasSubscription
  const limit = data.entitlements.member_limit
  const used = Array.isArray(members.data) ? members.data.length : undefined
  const status = data.entitlements.status.replace(/_/g, " ")
  const renewal = subscription?.renews_at
    ? `Renews ${longDate.format(new Date(subscription.renews_at))}`
    : subscription?.ends_at
      ? `Ends ${longDate.format(new Date(subscription.ends_at))}`
      : undefined
  const planLine = [
    renewal,
    subscription && subscription.quantity > 1
      ? `${subscription.quantity} seats`
      : undefined,
    status !== "active" ? status : undefined,
  ]
    .filter(Boolean)
    .join(" · ")
  const showPlans = canCheckout || choosing || !data.can_manage

  return (
    <>
      <SettingsCard className="settings-plan">
        <div className="settings-plan-scene" aria-hidden="true" />
        <div className="settings-plan-text">
          <small>Current plan</small>
          <strong>{planName(data.entitlements.plan)}</strong>
          <span>{planLine || allowance(limit)}</span>
        </div>
        {limit !== null && used !== undefined && (
          <div className="settings-seats">
            <small>
              {used} of {limit} seats
            </small>
            <div
              className="settings-seats-bar"
              role="meter"
              aria-label="Seats used"
              aria-valuemin={0}
              aria-valuemax={limit}
              aria-valuenow={Math.min(used, limit)}
            >
              <i style={{ width: `${Math.min(100, (used / limit) * 100)}%` }} />
            </div>
          </div>
        )}
        {data.can_manage && (
          <SettingsButton
            size="default"
            tone="primary"
            disabled={portal.isPending}
            onClick={() =>
              hasSubscription ? portal.mutate() : setChoosing(true)
            }
          >
            {hasSubscription
              ? portal.isPending
                ? "Opening…"
                : "Change plan"
              : "Choose a plan"}
          </SettingsButton>
        )}
      </SettingsCard>
      {limit !== null && planLine && (
        <SettingsNote>{allowance(limit)}</SettingsNote>
      )}
      {data.entitlements.grace_ends_at && (
        <SettingsMessage tone="error">
          Payment is being retried until{" "}
          {longDate.format(new Date(data.entitlements.grace_ends_at))}.
        </SettingsMessage>
      )}
      {portal.isError && (
        <SettingsMessage tone="error">
          {authErrorMessage(portal.error, "Could not open the billing portal.")}
        </SettingsMessage>
      )}

      {showPlans && (
        <SettingsGroup label="Plans">
          <SettingsRow
            description={
              hasSubscription
                ? "Change plans in the billing portal."
                : data.can_manage
                  ? "Checkout opens securely with the payment provider."
                  : "An organisation owner can choose a plan."
            }
          />
          {plans.isPending && <SettingsNote>Loading plans…</SettingsNote>}
          {plans.isError && (
            <SettingsMessage tone="error">
              {authErrorMessage(plans.error, "Could not load plans.")}{" "}
              <SettingsButton onClick={() => void plans.refetch()}>
                Retry
              </SettingsButton>
            </SettingsMessage>
          )}
          {plans.data?.plans.length === 0 && (
            <SettingsNote>No paid plans are available yet.</SettingsNote>
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
        </SettingsGroup>
      )}

      <SettingsGroup label="Payment">
        <SettingsRow
          title="Card, billing contact and invoices"
          description="Managed in the billing portal, where receipts and invoices go to your billing contact."
        >
          {data.can_manage && hasSubscription && (
            <SettingsButton
              disabled={portal.isPending}
              onClick={() => portal.mutate()}
            >
              Open portal <ArrowUpRight />
            </SettingsButton>
          )}
        </SettingsRow>
      </SettingsGroup>
    </>
  )
}
