import { ArrowUpRight } from "lucide-react"
import type { Organization } from "@/lib/auth/client"

export function WorkspaceEmailSettings({
  organization,
  onBilling,
  onAccount,
}: {
  organization?: Organization
  onBilling: () => void
  onAccount: () => void
}) {
  return (
    <div className="workspace-settings-account">
      <section
        className="workspace-settings-section"
        aria-label="Always sent email"
      >
        <div className="workspace-settings-section-title">
          <h3>Always sent</h3>
          <p>
            Some email always goes out because accounts depend on it
            {organization ? ` in ${organization.name}` : ""}.
          </p>
        </div>
        <div className="workspace-settings-row">
          <div>
            <strong>Account access</strong>
            <small>
              Verification, password resets, and invitations each use a one-time
              link.
            </small>
          </div>
          <span className="workspace-settings-tag">Always on</span>
        </div>
        <div className="workspace-settings-row">
          <div>
            <strong>Membership changes</strong>
            <small>
              When a person’s role changes or they are removed, we tell that
              person.
            </small>
          </div>
          <span className="workspace-settings-tag">To that member</span>
        </div>
      </section>
      <section
        className="workspace-settings-section"
        aria-label="Billing email"
      >
        <div className="workspace-settings-section-title">
          <h3>Billing</h3>
          <p>
            Payment and renewal notices go to the billing contact, not every
            admin.
          </p>
        </div>
        <button
          className="workspace-settings-action"
          type="button"
          onClick={onBilling}
        >
          Open billing <ArrowUpRight size={14} />
        </button>
      </section>
      <section
        className="workspace-settings-section"
        aria-label="Team call email"
      >
        <div className="workspace-settings-section-title">
          <h3>Team calls</h3>
          <p>
            Each member decides, team by team, whether calls trigger an email.
          </p>
        </div>
        <button
          className="workspace-settings-action"
          type="button"
          onClick={onAccount}
        >
          Your preferences <ArrowUpRight size={14} />
        </button>
      </section>
    </div>
  )
}
