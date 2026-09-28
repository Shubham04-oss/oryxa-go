import { isRedirect } from "@tanstack/react-router"
import { describe, expect, it } from "vitest"
import { Route } from "./routes/settings.billing"

// What the route redirects to, from the query billing's BILLING_URL arrives
// with.
function redirectFor(search: Record<string, unknown>) {
  const validate = Route.options.validateSearch as (
    search: Record<string, unknown>
  ) => { organization_id?: string }
  const beforeLoad = Route.options.beforeLoad as unknown as (context: {
    search: { organization_id?: string }
  }) => unknown
  try {
    beforeLoad({ search: validate(search) })
  } catch (thrown) {
    if (isRedirect(thrown)) return thrown.options
    throw thrown
  }
  throw new Error("/settings/billing did not redirect")
}

describe("/settings/billing", () => {
  it("opens the organization's workspace on Billing", () => {
    // Dodo adds its own parameters to the return URL; they are ignored.
    expect(
      redirectFor({
        organization_id: "org-1",
        subscription_id: "sub_1",
        status: "active",
      })
    ).toMatchObject({
      to: "/",
      search: { org: "org-1", view: "billing" },
      replace: true,
    })
  })

  it("opens the member's first workspace when none is named", () => {
    expect(redirectFor({ organization_id: ["org-1"] })).toMatchObject({
      to: "/",
      search: { org: undefined, view: "billing" },
      replace: true,
    })
  })
})
