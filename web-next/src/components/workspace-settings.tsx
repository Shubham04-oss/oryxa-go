import { useState } from "react"
import {
  ArrowLeft,
  Building2,
  CreditCard,
  Mail,
  Network,
  Puzzle,
  Settings2,
  UserRound,
  UsersRound,
  X,
} from "lucide-react"
import type { Organization, Role, Session } from "@/lib/auth/client"
import type { AvatarIdentity } from "./workspace-avatar"
import { PeopleManagement } from "./people-management"
import { WorkspaceAccountSettings } from "./workspace-account-settings"
import { WorkspaceOrganizationSettings } from "./workspace-organization-settings"
import { WorkspaceTeamsSettings } from "./workspace-teams-settings"
import { WorkspaceToolAccessSettings } from "./workspace-tool-access-settings"
import { WorkspaceEmailSettings } from "./workspace-email-settings"
import { WorkspaceBillingSettings } from "./workspace-billing-settings"
import { cn } from "@/lib/utils"

export type SettingsPage =
  | "account"
  | "organization"
  | "people"
  | "teams"
  | "tools"
  | "email"
  | "billing"

const pageTitles: Record<SettingsPage, string> = {
  account: "Account",
  organization: "Organisation",
  people: "People & invitations",
  teams: "Teams",
  tools: "Tool access",
  email: "Email",
  billing: "Billing",
}

export function WorkspaceSettings({
  onClose,
  identity,
  session,
  selectedOrganization,
  organizationRole,
  preview,
  onSignOut,
  signingOut,
  signOutError,
  onOpenTools,
  initialPage = "people",
}: {
  onClose: () => void
  identity: AvatarIdentity
  session?: Session
  selectedOrganization?: Organization
  organizationRole?: Role
  preview: boolean
  onSignOut: () => void
  signingOut: boolean
  signOutError: string
  onOpenTools: () => void
  initialPage?: SettingsPage
}) {
  const [page, setPage] = useState<SettingsPage>(initialPage)
  const organization = selectedOrganization || session?.organizations[0]

  return (
    <div
      className="workspace-settings-backdrop"
      onMouseDown={(event) => {
        if (event.target === event.currentTarget) onClose()
      }}
    >
      <div
        className="workspace-settings-dialog"
        role="dialog"
        aria-modal="true"
        aria-label="Settings"
      >
        <aside
          className="workspace-settings-sidebar"
          aria-label="Settings navigation"
        >
          <div className="workspace-settings-sidebar-head">
            <span className="workspace-settings-icon">
              <Settings2 size={19} strokeWidth={1.7} />
            </span>
            <strong>Settings</strong>
          </div>
          <nav aria-label="Settings sections">
            <button
              type="button"
              className={cn(page === "account" && "is-active")}
              onClick={() => setPage("account")}
            >
              <UserRound size={18} strokeWidth={1.7} /> Account
            </button>
            <button
              type="button"
              className={cn(page === "organization" && "is-active")}
              onClick={() => setPage("organization")}
            >
              <Building2 size={18} strokeWidth={1.7} /> Organisation
            </button>
            <button
              type="button"
              className={cn(page === "people" && "is-active")}
              onClick={() => setPage("people")}
            >
              <UsersRound size={18} strokeWidth={1.7} /> People & invitations
            </button>
            <button
              type="button"
              className={cn(page === "teams" && "is-active")}
              onClick={() => setPage("teams")}
            >
              <Network size={18} strokeWidth={1.7} /> Teams
            </button>
            <button
              type="button"
              className={cn(page === "tools" && "is-active")}
              onClick={() => setPage("tools")}
            >
              <Puzzle size={18} strokeWidth={1.7} /> Tool access
            </button>
            <button
              type="button"
              className={cn(page === "email" && "is-active")}
              onClick={() => setPage("email")}
            >
              <Mail size={18} strokeWidth={1.7} /> Email
            </button>
            <button
              type="button"
              className={cn(page === "billing" && "is-active")}
              onClick={() => setPage("billing")}
            >
              <CreditCard size={18} strokeWidth={1.7} /> Billing
            </button>
          </nav>
          <button
            className="workspace-settings-back"
            type="button"
            onClick={onClose}
          >
            <ArrowLeft size={17} strokeWidth={1.7} /> Back to workspace
          </button>
        </aside>
        <div className="workspace-settings-content">
          <header className="workspace-settings-header">
            <div>
              <h2>{pageTitles[page]}</h2>
            </div>
            <button
              type="button"
              aria-label="Close settings"
              autoFocus
              onClick={onClose}
            >
              <X size={18} strokeWidth={1.7} />
            </button>
          </header>
          <div className="workspace-settings-scroll">
            {page === "people" ? (
              <PeopleManagement
                organizationId={
                  organization?.id ||
                  (preview ? "preview-organization" : undefined)
                }
                role={session ? organizationRole || "member" : "owner"}
                viewerId={session?.user.id}
                preview={preview}
              />
            ) : page === "organization" ? (
              <WorkspaceOrganizationSettings
                organization={organization}
                role={organizationRole}
                preview={preview}
                session={session}
              />
            ) : page === "account" ? (
              <WorkspaceAccountSettings
                identity={identity}
                session={session}
                organization={organization}
                role={organizationRole}
                preview={preview}
                onSignOut={onSignOut}
                signingOut={signingOut}
                signOutError={signOutError}
              />
            ) : page === "teams" ? (
              <WorkspaceTeamsSettings
                organization={organization}
                role={organizationRole}
                preview={preview}
              />
            ) : page === "tools" ? (
              <WorkspaceToolAccessSettings
                organization={organization}
                role={organizationRole}
                preview={preview}
                onOpenTools={onOpenTools}
              />
            ) : page === "email" ? (
              <WorkspaceEmailSettings
                organization={organization}
                onBilling={() => setPage("billing")}
                onAccount={() => setPage("account")}
              />
            ) : (
              <WorkspaceBillingSettings
                organization={organization}
                preview={preview}
              />
            )}
          </div>
        </div>
      </div>
    </div>
  )
}
